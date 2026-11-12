package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"regexp"
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

// recordingRunner captures the command and dryRun flag it was invoked with.
type recordingRunner struct {
	called bool
	dryRun bool
	cmd    commands.Command
	out    rclone.Outcome
}

func (r *recordingRunner) Run(_ context.Context, c *commands.Command, dryRun bool, _ func(rclone.Progress)) (rclone.Outcome, error) {
	r.called = true
	r.dryRun = dryRun
	r.cmd = *c
	return r.out, nil
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

// blockingMQTT never completes a publish, like a client whose broker
// connection is gone and whose reconnect waits for the message callback.
type blockingMQTT struct{ release chan struct{} }

func (b *blockingMQTT) Publish(string, byte, bool, []byte) error {
	<-b.release
	return nil
}

func newManagerWith(mq events.MQTT, runner Runner) *Manager {
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
	sealer, _ := evidence.NewSealer(evidence.Options{ProducerID: cfg.WorkerID, ProducerVersion: "test"})
	pub := events.NewPublisher(mq, cfg, "test", sealer)
	return NewManager(pol, pub, runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Handle is called from the MQTT client's message callback. Paho must not be
// blocked there: a callback that waits for a publish kept the client from
// reconnecting, and one command took the worker off the broker (audit E1).
func TestHandleNeverWaitsForAPublish(t *testing.T) {
	mq := &blockingMQTT{release: make(chan struct{})}
	defer close(mq.release)
	m := newManagerWith(mq, fakeRunner{})
	done := make(chan struct{})
	go func() {
		m.Handle([]byte(`{"job_id":"job-1","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`))
		m.Handle([]byte(`{"job_id":"job-2","action":"exec"}`))
		m.Handle([]byte(`{"job_id":"job-3","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle waited for a publish; the MQTT message callback must never block on one")
	}
}

var safeTopic = regexp.MustCompile(`^trailtransfer/worker-01/jobs/[A-Za-z0-9._-]{1,128}/(status|progress|result|logs)$`)

// A command whose job_id or target_job_id is not a safe topic level is
// dropped and logged, never answered on a topic built from it.
func TestInvalidJobIDsArePublishedNowhere(t *testing.T) {
	m, rec := newTestManager(fakeRunner{out: rclone.Outcome{FilesTotal: 1, FilesTransferred: 1}})
	for _, p := range []string{
		`{"job_id":"bad#id","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`,
		`{"job_id":"run#id","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`,
		`{"job_id":"bad+id","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`,
		`{"job_id":"a/b","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`,
		`{"job_id":"bad#schema","action":"exec"}`,
		`{"job_id":"q-1","action":"status","target_job_id":"x#y"}`,
		`{"job_id":"q-2","action":"cancel","target_job_id":"x/y"}`,
	} {
		m.Handle([]byte(p))
	}
	// Commands are handled in order, so once this result is there every
	// command above has been handled.
	m.Handle([]byte(`{"job_id":"job-after","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`))
	rec.waitResult(t, "job-after")

	m.mu.Lock()
	_, admitted := m.seen["run#id"]
	m.mu.Unlock()
	if admitted {
		t.Error("a command with job_id run#id was admitted for execution")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, msg := range rec.msgs {
		if !safeTopic.MatchString(msg.topic) {
			t.Errorf("published on a topic built from an invalid id: %q", msg.topic)
		}
	}
}

// rclone receives, and the record names, the source and target the policy
// checked, not another spelling of them.
func TestRunUsesTheNormalisedSourceAndTarget(t *testing.T) {
	rr := &recordingRunner{}
	m, rec := newTestManager(rr)
	m.Handle([]byte(`{"job_id":"job-n","action":"copy","source":"/data/input//sub/./","target":"s3-demo:trailtransfer//x/./","dry_run":false}`))
	p := payloadOf(t, rec.waitResult(t, "job-n"))
	if rr.cmd.Source != "/data/input/sub" || rr.cmd.Target != "s3-demo:trailtransfer/x" {
		t.Fatalf("rclone got %q -> %q, want /data/input/sub -> s3-demo:trailtransfer/x", rr.cmd.Source, rr.cmd.Target)
	}
	if p["source"] != "/data/input/sub" || p["target"] != "s3-demo:trailtransfer/x" {
		t.Fatalf("record names %v -> %v, want the normalised form", p["source"], p["target"])
	}
}

// newTestManagerWith is newTestManager with a policy adjustment.
func newTestManagerWith(runner Runner, adjust func(*policy.Policy)) (*Manager, *recorder) {
	m, rec := newTestManager(runner)
	adjust(m.pol)
	return m, rec
}

// messages returns the payloads published on one job topic, in order.
func (r *recorder) messages(jobID, suffix string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, m := range r.msgs {
		if strings.HasSuffix(m.topic, "jobs/"+jobID+"/"+suffix) {
			var v map[string]any
			_ = json.Unmarshal(m.payload, &v)
			out = append(out, v)
		}
	}
	return out
}

// waitStatus waits until the job's status topic carries the given status.
func (r *recorder) waitStatus(t *testing.T, jobID, status string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range r.messages(jobID, "status") {
			if s["status"] == status {
				return s
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no status %q for %s; got %v", status, jobID, r.messages(jobID, "status"))
	return nil
}

// A dry run used to be recorded like a transfer, with the counters rclone
// reports for what it would have done (audit E2).
func TestDryRunIsRecordedAsADryRun(t *testing.T) {
	rr := &recordingRunner{out: rclone.Outcome{FilesTotal: 1, FilesTransferred: 1, BytesTotal: 6, BytesTransferred: 6}}
	m, rec := newTestManager(rr) // dry_run_default: true
	m.Handle([]byte(`{"job_id":"job-dry","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))
	p := payloadOf(t, rec.waitResult(t, "job-dry"))
	if p["dry_run"] != true {
		t.Fatalf("record must say dry_run true, got %v", p["dry_run"])
	}
	if p["files_transferred"].(float64) != 0 || p["bytes_transferred"].(float64) != 0 {
		t.Fatalf("a dry run transfers nothing; record says files %v, bytes %v", p["files_transferred"], p["bytes_transferred"])
	}
	if p["files_total"].(float64) != 1 {
		t.Fatalf("files_total must keep what rclone would have transferred, got %v", p["files_total"])
	}
	if s := rec.waitStatus(t, "job-dry", "completed"); !strings.Contains(fmt.Sprint(s["detail"]), "dry run") {
		t.Fatalf("the completed status must say it was a dry run, got %v", s)
	}
}

func TestRealRunIsRecordedAsNoDryRun(t *testing.T) {
	rr := &recordingRunner{out: rclone.Outcome{FilesTotal: 1, FilesTransferred: 1, BytesTotal: 6, BytesTransferred: 6}}
	m, rec := newTestManager(rr)
	m.Handle([]byte(`{"job_id":"job-real","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer","dry_run":false}`))
	p := payloadOf(t, rec.waitResult(t, "job-real"))
	if p["dry_run"] != false || p["files_transferred"].(float64) != 1 || p["bytes_transferred"].(float64) != 6 {
		t.Fatalf("a real run must say dry_run false and keep its counters, got %+v", p)
	}
}

// Files over max_file_size_mb are excluded by rclone without an error; the
// record used to say completed with nothing that pointed at the limit (E7).
func TestSizeLimitIsNamedInTheRecord(t *testing.T) {
	m, rec := newTestManagerWith(fakeRunner{}, func(p *policy.Policy) { p.MaxFileSizeMB = 1 })
	m.Handle([]byte(`{"job_id":"job-big","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer","dry_run":false}`))
	p := payloadOf(t, rec.waitResult(t, "job-big"))
	if p["max_file_size_mb"] != float64(1) {
		t.Fatalf("record must name max_file_size_mb 1, got %v", p["max_file_size_mb"])
	}
	if s := rec.waitStatus(t, "job-big", "completed"); !strings.Contains(fmt.Sprint(s["detail"]), "max_file_size_mb") {
		t.Fatalf("the completed status must mention the size limit, got %v", s)
	}

	m, rec = newTestManager(fakeRunner{})
	m.Handle([]byte(`{"job_id":"job-nolimit","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer","dry_run":false}`))
	if p := payloadOf(t, rec.waitResult(t, "job-nolimit")); p["max_file_size_mb"] != nil {
		t.Fatalf("without a limit the record must not name one, got %v", p["max_file_size_mb"])
	}
}

// A status query reported "completed" for a failed job and published on the
// queried job's topic (audit E8).
func TestStatusQueryReportsTheStoredOutcomeOnItsOwnTopic(t *testing.T) {
	m, rec := newTestManager(fakeRunner{out: rclone.Outcome{ExitCode: 1, Errors: []string{"boom"}}})
	m.Handle([]byte(`{"job_id":"job-f","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer","dry_run":false}`))
	rec.waitStatus(t, "job-f", "failed")
	m.Handle([]byte(`{"job_id":"job-r","action":"copy","source":"/etc","target":"s3-demo:trailtransfer"}`))
	rec.waitStatus(t, "job-r", "rejected")
	// A repeated job_id is refused as a duplicate; that must not replace the
	// outcome of the job that ran.
	m.Handle([]byte(`{"job_id":"job-f","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer","dry_run":false}`))
	before := len(rec.messages("job-f", "status"))

	for q, want := range map[string]struct{ target, status string }{
		"q-1": {"job-f", "failed"},
		"q-2": {"job-r", "rejected"},
		"q-3": {"never-seen", "unknown"},
	} {
		m.Handle([]byte(`{"job_id":"` + q + `","action":"status","target_job_id":"` + want.target + `"}`))
		s := rec.waitStatus(t, q, "completed")
		if s["target_job_id"] != want.target || s["target_status"] != want.status {
			t.Errorf("status query %s for %s: want target_status %q, got %v", q, want.target, want.status, s)
		}
	}
	// The duplicate rejection itself is on job-f's topic; the queries must
	// have added nothing there.
	if after := len(rec.messages("job-f", "status")); after != before+1 && after != before {
		t.Errorf("the status query published on the queried job's topic: %v", rec.messages("job-f", "status"))
	}
	for _, s := range rec.messages("job-f", "status") {
		if s["status"] == "completed" {
			t.Errorf("a failed job must never be reported completed on its topic: %v", s)
		}
	}
	if got := rec.messages("never-seen", "status"); len(got) != 0 {
		t.Errorf("a query for an unknown job wrote to that job's topic: %v", got)
	}
}

// gateRunner blocks every run until release is closed.
type gateRunner struct{ release chan struct{} }

func (g gateRunner) Run(context.Context, *commands.Command, bool, func(rclone.Progress)) (rclone.Outcome, error) {
	<-g.release
	return rclone.Outcome{}, nil
}

func TestParallelJobsAreBoundedByThePolicy(t *testing.T) {
	gate := gateRunner{release: make(chan struct{})}
	m, rec := newTestManagerWith(gate, func(p *policy.Policy) { p.MaxParallelJobs = 1 })
	m.Handle([]byte(`{"job_id":"job-a","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))
	rec.waitStatus(t, "job-a", "started")
	m.Handle([]byte(`{"job_id":"job-b","action":"copy","source":"/data/input","target":"s3-demo:trailtransfer"}`))
	if p := payloadOf(t, rec.waitResult(t, "job-b")); p["status"] != "rejected" || p["reason"] != "too_many_jobs" {
		t.Fatalf("a job beyond max_parallel_jobs must be rejected as too_many_jobs, got %+v", p)
	}
	close(gate.release)
	rec.waitResult(t, "job-a")
}
