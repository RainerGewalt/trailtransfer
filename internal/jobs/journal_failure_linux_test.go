//go:build linux

package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/RainerGewalt/trailtransfer/internal/config"
	"github.com/RainerGewalt/trailtransfer/internal/events"
	"github.com/RainerGewalt/trailtransfer/internal/evidence"
	"github.com/RainerGewalt/trailtransfer/internal/health"
	"github.com/RainerGewalt/trailtransfer/internal/policy"
	"github.com/RainerGewalt/trailtransfer/internal/rclone"
)

// syncBuffer is a log sink that is safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// limitFileSize makes every write beyond the current size of path fail, as
// on a full disk, until the returned function restores the limit.
func limitFileSize(t *testing.T, path string) (restore func()) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Skipf("getrlimit: %v", err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	lim := syscall.Rlimit{Cur: uint64(fi.Size()), Max: old.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Skipf("setrlimit: %v", err)
	}
	done := false
	return func() {
		if done {
			return
		}
		done = true
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
			t.Fatalf("restore RLIMIT_FSIZE: %v", err)
		}
		signal.Reset(syscall.SIGXFSZ)
	}
}

// A journal write failure used to fall back to publishing the result
// unsealed, without a log line, while health stayed "healthy" and new jobs
// were accepted (audit E6).
func TestJournalWriteFailureStopsSealingAndRefusesNewJobs(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "journal.jsonl")
	sealer, err := evidence.NewSealer(evidence.Options{ProducerID: "worker-01", ProducerVersion: "test", JournalPath: journal})
	if err != nil {
		t.Fatal(err)
	}
	defer sealer.Close()
	cfg := &config.Config{WorkerID: "worker-01", TopicPrefix: "trailtransfer"}
	pol := &policy.Policy{
		PolicyVersion: "1", AllowedActions: []string{"copy"}, AllowedSources: []string{"/data/input"},
		AllowedTargets: []string{"s3-demo:trailtransfer"}, MaxParallelJobs: 2, RequireJobID: true,
	}
	rec := &recorder{}
	logs := &syncBuffer{}
	pub := events.NewPublisher(rec, cfg, "test", sealer)
	m := NewManager(pol, pub, fakeRunner{out: rclone.Outcome{FilesTotal: 1, FilesTransferred: 1}}, slog.New(slog.NewJSONHandler(logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	health.StartLoop(ctx, pub, pol, m, "rclone", 10*time.Millisecond)

	m.Handle([]byte(`{"job_id":"j-1","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`))
	rec.waitResult(t, "j-1")

	restore := limitFileSize(t, journal)
	defer restore()
	m.Handle([]byte(`{"job_id":"j-2","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`))
	rec.waitStatus(t, "j-2", "rejected")
	restore() // later writes would succeed again; the worker must not resume on top of a failed write
	size := fileSize(t, journal)

	m.Handle([]byte(`{"job_id":"j-3","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer","dry_run":false}`))
	s3 := rec.waitTerminal(t, "j-3")
	if s3["status"] != "rejected" {
		t.Errorf("a job sent after the journal failed must be refused, it ended %v", s3["status"])
	}

	if got := rec.messages("j-2", "result"); len(got) != 0 {
		t.Errorf("a result that could not be sealed was published: %v", got)
	}
	for _, s := range rec.messages("j-3", "status") {
		if s["status"] == "accepted" || s["status"] == "started" {
			t.Errorf("a job was accepted after the journal failed: %v", s)
		}
	}
	if d, _ := s3["detail"].(string); !strings.Contains(d, "journal") {
		t.Errorf("the refusal must name the journal, got %v", s3["detail"])
	}
	if got := rec.messages("j-3", "result"); len(got) != 0 {
		t.Errorf("a result was published for a job refused after the journal failed: %v", got)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Errorf("the journal failure must be logged as an error; log:\n%s", logs.String())
	}
	if got := fileSize(t, journal); got != size {
		t.Errorf("the journal grew from %d to %d bytes after a failed write", size, got)
	}
	if !waitHealth(rec, "unhealthy") {
		t.Errorf("health must leave healthy after a journal failure; last health: %v", lastHealth(rec))
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func lastHealth(r *recorder) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var last map[string]any
	for _, m := range r.msgs {
		if strings.HasSuffix(m.topic, "/health") {
			last = map[string]any{}
			_ = json.Unmarshal(m.payload, &last)
		}
	}
	return last
}

func waitHealth(r *recorder, status string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h := lastHealth(r); h != nil && h["status"] == status {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitTerminal waits for the job's first terminal status.
func (r *recorder) waitTerminal(t *testing.T, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range r.messages(jobID, "status") {
			switch s["status"] {
			case "rejected", "completed", "failed", "cancelled":
				return s
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no terminal status for %s; got %v", jobID, r.messages(jobID, "status"))
	return nil
}
