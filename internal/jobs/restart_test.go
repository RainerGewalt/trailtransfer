package jobs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RainerGewalt/trailtransfer/internal/commands"
	"github.com/RainerGewalt/trailtransfer/internal/config"
	"github.com/RainerGewalt/trailtransfer/internal/events"
	"github.com/RainerGewalt/trailtransfer/internal/evidence"
	"github.com/RainerGewalt/trailtransfer/internal/policy"
	"github.com/RainerGewalt/trailtransfer/internal/rclone"
)

// These tests cover issue #1: what a worker with a journal knows after a
// restart. Each "process" is a fresh sealer, outbox and manager on the same
// directory; a kill is a manager that is simply abandoned.

const copyCmd = `{"job_id":"%ID%","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer","dry_run":false}`

func cmdFor(id string) []byte { return []byte(strings.ReplaceAll(copyCmd, "%ID%", id)) }

// blockingRunner counts its runs and blocks until its context ends or
// release is closed.
type blockingRunner struct {
	mu      sync.Mutex
	runs    int
	started chan struct{}
	release chan struct{}
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (b *blockingRunner) Run(ctx context.Context, _ *commands.Command, _ bool, _ func(rclone.Progress)) (rclone.Outcome, error) {
	b.mu.Lock()
	b.runs++
	b.mu.Unlock()
	b.started <- struct{}{}
	select {
	case <-ctx.Done():
		return rclone.Outcome{Canceled: true, ExitCode: -1}, ctx.Err()
	case <-b.release:
		return rclone.Outcome{FilesTotal: 1, FilesTransferred: 1, BytesTransferred: 10}, nil
	}
}

func (b *blockingRunner) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.runs
}

type process struct {
	mgr    *Manager
	rec    *recorder
	sealer *evidence.Sealer
	outbox *events.Outbox
	sealed int // interrupted records sealed by Recover
}

func journalPath(dir string) string { return filepath.Join(dir, "evidence.jsonl") }
func statePath(dir string) string   { return journalPath(dir) + ".jobs" }

// startProcess is what cmdRun does between loading the config and
// subscribing, without the broker.
func startProcess(t *testing.T, dir string, runner Runner) *process {
	t.Helper()
	cfg := &config.Config{WorkerID: "worker-01", TopicPrefix: "trailtransfer"}
	pol := &policy.Policy{
		PolicyVersion: "1", AllowedActions: []string{"copy"}, AllowedSources: []string{"/data/input"},
		AllowedTargets: []string{"s3-demo:trailtransfer"}, MaxParallelJobs: 2, RequireJobID: true, DryRunDefault: true,
	}
	sealer, err := evidence.NewSealer(evidence.Options{ProducerID: cfg.WorkerID, ProducerVersion: "test", JournalPath: journalPath(dir)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sealer.Close() })
	rec := &recorder{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub := events.NewPublisher(rec, cfg, "test", sealer)
	outbox, err := events.NewOutbox(journalPath(dir), rec, cfg.JobResultTopic, log)
	if err != nil {
		t.Fatal(err)
	}
	pub.SetOutbox(outbox)
	mgr := NewManager(pol, pub, runner, log)
	n, err := mgr.Recover(journalPath(dir), statePath(dir))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return &process{mgr: mgr, rec: rec, sealer: sealer, outbox: outbox, sealed: n}
}

type journalRecord struct {
	Seq       uint64 `json:"seq"`
	EventType string `json:"event_type"`
	JobID     string `json:"job_id"`
	Payload   struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
		Detail string `json:"detail"`
	} `json:"payload"`
}

func readJournal(t *testing.T, dir string) []journalRecord {
	t.Helper()
	var out []journalRecord
	err := evidence.ScanJournal(journalPath(dir), 0, func(e evidence.JournalEntry) error {
		var r journalRecord
		if err := json.Unmarshal(e.Raw, &r); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func waitJournal(t *testing.T, dir string, n int) []journalRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		recs := readJournal(t, dir)
		if len(recs) >= n {
			return recs
		}
		if time.Now().After(deadline) {
			t.Fatalf("journal has %d record(s), want %d", len(recs), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func mustVerify(t *testing.T, dir string) evidence.VerifyReport {
	t.Helper()
	rep, err := evidence.Verify(journalPath(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("journal does not verify: %+v", rep.Findings)
	}
	return rep
}

func stateJobs(t *testing.T, dir string) map[string]runningJob {
	t.Helper()
	st, err := openState(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return st.snapshot()
}

// Acceptance 1: a command that is delivered again after a restart does not
// run a second time, and the answer names the stored outcome.
func TestRedeliveredCommandAfterRestartDoesNotRunAgain(t *testing.T) {
	dir := t.TempDir()
	r1 := newBlockingRunner()
	p1 := startProcess(t, dir, r1)
	p1.mgr.Handle(cmdFor("job-1"))
	<-r1.started
	close(r1.release)
	waitJournal(t, dir, 1)
	p1.mgr.Shutdown(time.Second) // the first process has ended before the next starts

	r2 := newBlockingRunner()
	p2 := startProcess(t, dir, r2)
	if p2.sealed != 0 {
		t.Fatalf("a finished job must not be sealed as interrupted, got %d", p2.sealed)
	}
	p2.mgr.Handle(cmdFor("job-1"))
	recs := waitJournal(t, dir, 2)
	if r2.count() != 0 {
		t.Fatalf("redelivered command ran %d time(s) after the restart", r2.count())
	}
	last := recs[1]
	if last.EventType != events.EvidenceJobRejected || last.Payload.Reason != "duplicate_job_id" || !strings.Contains(last.Payload.Detail, "last status: completed") {
		t.Fatalf("want a duplicate rejection that names the stored outcome, got %+v", last)
	}

	p2.mgr.Handle([]byte(`{"job_id":"q-1","action":"status","target_job_id":"job-1"}`))
	deadline := time.Now().Add(2 * time.Second)
	for {
		p2.rec.mu.Lock()
		found := false
		for _, m := range p2.rec.msgs {
			if strings.HasSuffix(m.topic, "jobs/q-1/status") && strings.Contains(string(m.payload), `"target_status":"completed"`) {
				found = true
			}
		}
		p2.rec.mu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status query after the restart did not answer completed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mustVerify(t, dir)
}

// Acceptance 2: a worker that is killed during a transfer leaves exactly one
// transfer.job.interrupted record, sealed by the next start, and the journal
// verifies. A third start seals nothing more.
func TestKilledWorkerLeavesExactlyOneInterruptedRecord(t *testing.T) {
	dir := t.TempDir()
	r1 := newBlockingRunner()
	p1 := startProcess(t, dir, r1)
	p1.mgr.Handle(cmdFor("job-kill"))
	<-r1.started
	if _, ok := stateJobs(t, dir)["job-kill"]; !ok {
		t.Fatal("a running job must be in the state file before rclone runs")
	}
	// The process dies here: no shutdown, no record.
	p1.sealer.Close()

	p2 := startProcess(t, dir, newBlockingRunner())
	if p2.sealed != 1 {
		t.Fatalf("want 1 interrupted record from the restart, got %d", p2.sealed)
	}
	p3 := startProcess(t, dir, newBlockingRunner())
	if p3.sealed != 0 {
		t.Fatalf("a second restart sealed %d more interrupted record(s)", p3.sealed)
	}
	recs := readJournal(t, dir)
	if len(recs) != 1 {
		t.Fatalf("want exactly one record, got %d: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.EventType != events.EvidenceJobInterrupted || r.JobID != "job-kill" || r.Payload.Status != events.StatusInterrupted || r.Payload.Reason != events.ReasonWorkerRestart {
		t.Fatalf("unexpected record: %+v", r)
	}
	if len(stateJobs(t, dir)) != 0 {
		t.Fatal("state file still names the job after it was sealed")
	}
	rep := mustVerify(t, dir)
	if rep.Restarts != 1 {
		t.Fatalf("want 1 restart notice, got %d", rep.Restarts)
	}

	// The interrupted job is not run again under its job_id.
	r4 := newBlockingRunner()
	p4 := startProcess(t, dir, r4)
	p4.mgr.Handle(cmdFor("job-kill"))
	recs = waitJournal(t, dir, 2)
	if r4.count() != 0 || recs[1].Payload.Reason != "duplicate_job_id" || !strings.Contains(recs[1].Payload.Detail, "last status: interrupted") {
		t.Fatalf("interrupted job_id was not refused as a duplicate: runs=%d record=%+v", r4.count(), recs[1])
	}
}

// The outcome is in the journal but the worker died before it cleared the
// state file: the restart must not seal a second record for the job.
func TestOutcomeInJournalWinsOverStateFile(t *testing.T) {
	dir := t.TempDir()
	r1 := newBlockingRunner()
	p1 := startProcess(t, dir, r1)
	p1.mgr.Handle(cmdFor("job-done"))
	<-r1.started
	left, err := os.ReadFile(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	close(r1.release)
	waitJournal(t, dir, 1)
	p1.mgr.Shutdown(time.Second)
	// Put the state back as it was while the job ran.
	if err := os.WriteFile(statePath(dir), left, 0o600); err != nil {
		t.Fatal(err)
	}

	p2 := startProcess(t, dir, newBlockingRunner())
	if p2.sealed != 0 {
		t.Fatalf("a job with an outcome in the journal was sealed again (%d)", p2.sealed)
	}
	if recs := readJournal(t, dir); len(recs) != 1 || recs[0].Payload.Status != "completed" {
		t.Fatalf("journal changed: %+v", recs)
	}
	if len(stateJobs(t, dir)) != 0 {
		t.Fatal("stale state entry was not cleared")
	}
}

// SIGTERM path: Shutdown ends rclone, seals the job as interrupted with
// reason worker_shutdown and admits nothing new.
func TestShutdownSealsRunningJobAsInterrupted(t *testing.T) {
	dir := t.TempDir()
	r := newBlockingRunner()
	p := startProcess(t, dir, r)
	p.mgr.Handle(cmdFor("job-term"))
	<-r.started
	if n := p.mgr.Shutdown(3 * time.Second); n != 1 {
		t.Fatalf("Shutdown reported %d running job(s), want 1", n)
	}
	recs := readJournal(t, dir)
	if len(recs) != 1 || recs[0].EventType != events.EvidenceJobInterrupted || recs[0].Payload.Reason != events.ReasonWorkerShutdown {
		t.Fatalf("want one interrupted record with reason worker_shutdown, got %+v", recs)
	}
	if len(stateJobs(t, dir)) != 0 {
		t.Fatal("state file not cleared after the record was sealed")
	}

	p.mgr.Handle(cmdFor("job-late"))
	recs = waitJournal(t, dir, 2)
	if recs[1].EventType != events.EvidenceJobRejected || recs[1].Payload.Reason != "worker_stopping" || r.count() != 1 {
		t.Fatalf("a command during shutdown must be rejected and not run: %+v runs=%d", recs[1], r.count())
	}

	// The next start has nothing left to seal.
	if p2 := startProcess(t, dir, newBlockingRunner()); p2.sealed != 0 {
		t.Fatalf("restart after a clean shutdown sealed %d record(s)", p2.sealed)
	}
	mustVerify(t, dir)
}

// A user's cancel stays a cancel; only a shutdown makes a job interrupted.
func TestCancelIsNotInterrupted(t *testing.T) {
	dir := t.TempDir()
	r := newBlockingRunner()
	p := startProcess(t, dir, r)
	p.mgr.Handle(cmdFor("job-c"))
	<-r.started
	p.mgr.Handle([]byte(`{"job_id":"c-1","action":"cancel","target_job_id":"job-c"}`))
	recs := waitJournal(t, dir, 1)
	if recs[0].EventType != events.EvidenceJobResult || recs[0].Payload.Status != "cancelled" {
		t.Fatalf("want a cancelled result, got %+v", recs[0])
	}
}

// Without a writable state file the job is refused before rclone runs.
func TestJobIsRefusedWhenStateCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	r := newBlockingRunner()
	p := startProcess(t, dir, r)
	// Make the state path unusable: a directory where the temp file goes.
	if err := os.Mkdir(statePath(dir)+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	p.mgr.Handle(cmdFor("job-x"))
	recs := waitJournal(t, dir, 1)
	if recs[0].EventType != events.EvidenceJobRejected || recs[0].Payload.Reason != "state_unavailable" {
		t.Fatalf("want a state_unavailable rejection, got %+v", recs[0])
	}
	if r.count() != 0 {
		t.Fatal("rclone ran although the job state could not be written")
	}
	if p.mgr.ActiveCount() != 0 {
		t.Fatal("refused job still counts as running")
	}
	// The job_id is free again once the state can be written.
	if err := os.Remove(statePath(dir) + ".tmp"); err != nil {
		t.Fatal(err)
	}
	p.mgr.Handle(cmdFor("job-x"))
	<-r.started
	close(r.release)
	recs = waitJournal(t, dir, 2)
	if recs[1].Payload.Status != "completed" {
		t.Fatalf("retry after the state came back: %+v", recs[1])
	}
}

// A journal that ends in a cut-off line stops the start instead of sealing on
// top of it, and the journal bytes stay as they are.
func TestRecoverRefusesJournalWithPartialLastLine(t *testing.T) {
	dir := t.TempDir()
	r1 := newBlockingRunner()
	p1 := startProcess(t, dir, r1)
	p1.mgr.Handle(cmdFor("job-1"))
	<-r1.started
	close(r1.release)
	waitJournal(t, dir, 1)
	p1.sealer.Close()
	f, err := os.OpenFile(journalPath(dir), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema":"trail.evidence/1","seq":1,"chain_ha`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	before, _ := os.ReadFile(journalPath(dir))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{WorkerID: "worker-01", TopicPrefix: "trailtransfer"}
	if _, err := evidence.NewSealer(evidence.Options{ProducerID: cfg.WorkerID, JournalPath: journalPath(dir)}); err == nil {
		t.Fatal("sealer opened a journal that ends in a cut-off line")
	}
	sealer, _ := evidence.NewSealer(evidence.Options{ProducerID: cfg.WorkerID})
	mgr := NewManager(&policy.Policy{PolicyVersion: "1"}, events.NewPublisher(&recorder{}, cfg, "test", sealer), newBlockingRunner(), log)
	if _, err := mgr.Recover(journalPath(dir), statePath(dir)); err == nil {
		t.Fatal("Recover accepted a journal that ends in a cut-off line")
	}
	after, _ := os.ReadFile(journalPath(dir))
	if string(before) != string(after) {
		t.Fatal("the journal was changed")
	}
}
