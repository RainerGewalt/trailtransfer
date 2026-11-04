package jobs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os/exec"
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

// recorder is a fake MQTT sink that captures every published message.
type recorder struct {
	mu   sync.Mutex
	msgs []struct {
		topic   string
		payload []byte
	}
}

func (r *recorder) Publish(topic string, _ byte, _ bool, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, struct {
		topic   string
		payload []byte
	}{topic, append([]byte(nil), payload...)})
	return nil
}

// waitResult polls for the result event on the given job and returns it parsed.
func (r *recorder) waitResult(t *testing.T, jobID string) map[string]any {
	t.Helper()
	suffix := "jobs/" + jobID + "/result"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, m := range r.msgs {
			if strings.HasSuffix(m.topic, suffix) {
				var out map[string]any
				_ = json.Unmarshal(m.payload, &out)
				r.mu.Unlock()
				return out
			}
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no result event for %s", jobID)
	return nil
}

type fakeRunner struct {
	out rclone.Outcome
	err error
}

func (f fakeRunner) Run(_ context.Context, _ *commands.Command, _ bool, onProgress func(rclone.Progress)) (rclone.Outcome, error) {
	if onProgress != nil {
		onProgress(rclone.Progress{FilesTotal: 1, FilesTransferred: 1, BytesTotal: 100, BytesTransferred: 100})
	}
	return f.out, f.err
}

// recordingRunner captures the dryRun flag it was invoked with.
type recordingRunner struct {
	called bool
	dryRun bool
}

func (r *recordingRunner) Run(_ context.Context, _ *commands.Command, dryRun bool, _ func(rclone.Progress)) (rclone.Outcome, error) {
	r.called = true
	r.dryRun = dryRun
	return rclone.Outcome{}, nil
}

func newTestManager(runner Runner) (*Manager, *recorder) {
	cfg := &config.Config{WorkerID: "worker-01", TopicPrefix: "trailtransfer"}
	pol := &policy.Policy{
		PolicyVersion:   "1",
		AllowedActions:  []string{"copy"},
		AllowedSources:  []string{"/data/input"},
		AllowedTargets:  []string{"s3-demo:trailtransfer"},
		MaxParallelJobs: 2,
		RequireJobID:    true,
		DryRunDefault:   true,
	}
	rec := &recorder{}
	sealer, _ := evidence.NewSealer(evidence.Options{ProducerID: cfg.WorkerID, ProducerVersion: "test"}) // in-memory chain for tests
	pub := events.NewPublisher(rec, cfg, "test", sealer)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewManager(pol, pub, runner, log), rec
}

// payloadOf extracts the domain payload from a sealed result envelope.
func payloadOf(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	p, ok := env["payload"].(map[string]any)
	if !ok {
		t.Fatalf("result envelope has no payload object: %v", env)
	}
	return p
}

func TestHandleCompletesValidCopy(t *testing.T) {
	m, rec := newTestManager(fakeRunner{out: rclone.Outcome{FilesTotal: 1, FilesTransferred: 1, BytesTransferred: 100}})
	m.Handle([]byte(`{"job_id":"job-001","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))

	env := rec.waitResult(t, "job-001")
	if env["schema"] != evidence.Schema || env["event_type"] != "transfer.job.result" {
		t.Fatalf("expected a trail.evidence/1 transfer.job.result record, got %v / %v", env["schema"], env["event_type"])
	}
	if env["seq"].(float64) != 0 || env["prev_hash"] != evidence.GenesisHash || env["chain_restart"] != true {
		t.Fatalf("first record must be seq 0 off genesis with chain_restart: %+v", env)
	}
	if env["job_id"] != "job-001" || env["stream_id"] != "worker-01" {
		t.Fatalf("job_id/stream_id missing: %+v", env)
	}
	if !strings.HasPrefix(env["content_hash"].(string), "sha256:") || !strings.HasPrefix(env["chain_hash"].(string), "sha256:") {
		t.Fatalf("missing chain hashes: %+v", env)
	}
	p := payloadOf(t, env)
	if p["status"] != "completed" {
		t.Fatalf("expected completed, got %v", p["status"])
	}
	if !strings.HasPrefix(p["command_hash"].(string), "sha256:") {
		t.Fatalf("missing command_hash: %v", p["command_hash"])
	}
	if p["policy_version"] != "1" || p["worker_version"] != "test" {
		t.Fatalf("evidence fields wrong: %+v", p)
	}
}

func TestHandleRejectsDisallowedSource(t *testing.T) {
	m, rec := newTestManager(fakeRunner{})
	m.Handle([]byte(`{"job_id":"job-002","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`))
	p := payloadOf(t, rec.waitResult(t, "job-002"))
	if p["status"] != "rejected" || p["reason"] != "policy_violation" {
		t.Fatalf("expected policy rejection, got %+v", p)
	}
	if _, ran := p["rclone_exit_code"]; ran {
		t.Fatal("rejected job must not have run rclone")
	}
}

func TestHandleRejectsFailedExit(t *testing.T) {
	m, rec := newTestManager(fakeRunner{out: rclone.Outcome{ExitCode: 1, Errors: []string{"boom"}}})
	m.Handle([]byte(`{"job_id":"job-003","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))
	p := payloadOf(t, rec.waitResult(t, "job-003"))
	if p["status"] != "failed" {
		t.Fatalf("nonzero rclone exit must fail the job, got %v", p["status"])
	}
}

func TestHandleOmitsExitCodeWhenRcloneDidNotStart(t *testing.T) {
	m, rec := newTestManager(fakeRunner{err: &exec.Error{Name: "rclone", Err: exec.ErrNotFound}})
	m.Handle([]byte(`{"job_id":"job-004","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))
	p := payloadOf(t, rec.waitResult(t, "job-004"))
	if p["status"] != "failed" {
		t.Fatalf("a job whose rclone did not start must fail, got %v", p["status"])
	}
	if code, ok := p["rclone_exit_code"]; ok {
		t.Fatalf("rclone never ran, so the record must not carry an exit code, got %v", code)
	}
	if errs, _ := p["errors"].([]any); len(errs) == 0 {
		t.Fatal("the start error must be recorded")
	}
}

func TestHandleKeepsExitCodeWhenRcloneRan(t *testing.T) {
	m, rec := newTestManager(fakeRunner{out: rclone.Outcome{FilesTotal: 1, FilesTransferred: 1}})
	m.Handle([]byte(`{"job_id":"job-005","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))
	p := payloadOf(t, rec.waitResult(t, "job-005"))
	if code, ok := p["rclone_exit_code"]; !ok || code.(float64) != 0 {
		t.Fatalf("a clean rclone run must record exit code 0, got %v (present %v)", code, ok)
	}
}

func TestDryRunDefaultApplied(t *testing.T) {
	rr := &recordingRunner{}
	m, rec := newTestManager(rr) // test policy has DryRunDefault: true
	m.Handle([]byte(`{"job_id":"job-dr","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))
	rec.waitResult(t, "job-dr")
	if !rr.called {
		t.Fatal("runner was not invoked")
	}
	if !rr.dryRun {
		t.Fatal("dry_run_default:true must make a command with absent dry_run run as a dry-run")
	}
}

func TestHandleDedupesJobID(t *testing.T) {
	m, rec := newTestManager(fakeRunner{out: rclone.Outcome{FilesTransferred: 1}})
	payload := []byte(`{"job_id":"job-001","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`)
	m.Handle(payload)
	rec.waitResult(t, "job-001")
	// same job_id again -> duplicate rejection (last result on that topic)
	m.Handle(payload)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		var last map[string]any
		for _, msg := range rec.msgs {
			if strings.HasSuffix(msg.topic, "jobs/job-001/result") {
				var env map[string]any
				if json.Unmarshal(msg.payload, &env) == nil {
					if p, ok := env["payload"].(map[string]any); ok {
						last = p
					}
				}
			}
		}
		rec.mu.Unlock()
		if last != nil && last["reason"] == "duplicate_job_id" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected duplicate_job_id rejection on repeat")
}

func TestRejectionIsSealedAsRejectedEvent(t *testing.T) {
	m, rec := newTestManager(fakeRunner{})
	m.Handle([]byte(`{"job_id":"job-010","action":"copy","source":"/etc","target":"s3-demo:trailtransfer","operator":"alice"}`))
	env := rec.waitResult(t, "job-010")
	if env["event_type"] != "transfer.job.rejected" {
		t.Fatalf("rejections must be sealed as transfer.job.rejected, got %v", env["event_type"])
	}
	actor, _ := env["actor"].(map[string]any)
	if actor["auth"] != "self-declared" || actor["id"] != "alice" {
		t.Fatalf("actor must carry the self-declared operator: %v", env["actor"])
	}
	if env["job_id"] != "job-010" {
		t.Fatalf("job_id missing on record: %v", env)
	}
}
