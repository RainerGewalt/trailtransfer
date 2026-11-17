// Package jobs turns validated commands into governed, deduplicated, bounded
// job executions and emits the full status/progress/result lifecycle.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RainerGewalt/trailtransfer/internal/commands"
	"github.com/RainerGewalt/trailtransfer/internal/events"
	"github.com/RainerGewalt/trailtransfer/internal/evidence"
	"github.com/RainerGewalt/trailtransfer/internal/logging"
	"github.com/RainerGewalt/trailtransfer/internal/policy"
	"github.com/RainerGewalt/trailtransfer/internal/rclone"
)

// Runner executes a transfer command. Abstracted so the manager can be tested
// without a real rclone binary.
type Runner interface {
	Run(ctx context.Context, c *commands.Command, dryRun bool, onProgress func(rclone.Progress)) (rclone.Outcome, error)
}

// Manager enforces dedupe and parallelism and drives each job's lifecycle.
type Manager struct {
	pol    *policy.Policy
	pub    *events.Publisher
	runner Runner
	log    *slog.Logger

	mu     sync.Mutex
	active map[string]context.CancelFunc
	seen   map[string]struct{}
	// outcome is the last known status of every job_id this process has
	// handled: accepted, started, then completed, failed, cancelled or
	// interrupted; or rejected. It answers status queries. Recover rebuilds
	// seen and outcome from the journal, so both survive a restart when a
	// journal is configured.
	outcome map[string]string
	running int

	// state holds the running jobs on disk, stopping is set by Shutdown,
	// and wg counts the job goroutines Shutdown waits for.
	state    *stateStore
	stopping bool
	wg       sync.WaitGroup
	// cancelRequested holds the running jobs a cancel command was sent for,
	// so that a shutdown right after it does not turn the cancel into an
	// interruption.
	cancelRequested map[string]struct{}

	// queue decouples the MQTT message callback from command handling, see
	// Handle.
	queue chan []byte
}

// commandQueueSize bounds the commands waiting to be handled.
const commandQueueSize = 256

// NewManager wires the manager to its policy, publisher and runner and starts
// the goroutine that handles queued commands in arrival order.
func NewManager(pol *policy.Policy, pub *events.Publisher, runner Runner, log *slog.Logger) *Manager {
	m := &Manager{
		pol:     pol,
		pub:     pub,
		runner:  runner,
		log:     log,
		active:  map[string]context.CancelFunc{},
		seen:    map[string]struct{}{},
		outcome: map[string]string{},
		queue:   make(chan []byte, commandQueueSize),

		cancelRequested: map[string]struct{}{},
	}
	m.state, _ = openState("")
	go m.dispatch()
	return m
}

// interruptedDetail says what an interrupted record does not know.
const interruptedDetail = "the worker did not see this job to its end; rclone may have transferred all, some or none of the files, check the target before sending the job again under a new job_id"

// Recover rebuilds the job state after a start. Call it before the worker
// subscribes to commands.
//
// The journal is the record of every job that ended: its job IDs are refused
// as duplicates again and its outcomes answer status queries. The state file
// names the jobs that were running when the worker last stopped. Each one
// that has no outcome in the journal is sealed as transfer.job.interrupted
// with reason worker_restart, exactly once: a job is removed from the state
// file only after its record is in the journal, and a job with a record in
// the journal is never sealed again. No job is resumed or run a second time.
//
// It returns the number of interrupted records it sealed.
func (m *Manager) Recover(journalPath, statePath string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if journalPath != "" {
		err := evidence.ScanJournal(journalPath, 0, func(e evidence.JournalEntry) error {
			if e.JobID == "" {
				return nil
			}
			var p struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			switch e.EventType {
			case events.EvidenceJobResult, events.EvidenceJobInterrupted:
				m.seen[e.JobID] = struct{}{}
				if p.Status != "" {
					m.outcome[e.JobID] = p.Status
				}
			case events.EvidenceJobRejected:
				if _, known := m.outcome[e.JobID]; !known {
					m.outcome[e.JobID] = "rejected"
				}
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	st, err := openState(statePath)
	if err != nil {
		return 0, err
	}
	m.state = st
	left := st.snapshot()
	ids := make([]string, 0, len(left))
	for id := range left {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sealed := 0
	for _, id := range ids {
		j := left[id]
		if _, ended := m.seen[id]; !ended {
			dry := j.DryRun
			actor := &evidence.Actor{Auth: evidence.ActorAuthNone}
			if j.Operator != "" {
				actor = &evidence.Actor{Auth: evidence.ActorAuthSelfDeclared, ID: j.Operator}
			}
			if err := m.pub.Result(events.ResultEvent{
				JobID:         id,
				Action:        j.Action,
				Source:        j.Source,
				Target:        j.Target,
				Status:        events.StatusInterrupted,
				Reason:        events.ReasonWorkerRestart,
				Detail:        interruptedDetail,
				Justification: j.Reason,
				StartedAt:     j.StartedAt,
				DryRun:        &dry,
				PolicyVersion: m.pol.PolicyVersion,
				PolicyHash:    m.pol.Hash,
				CommandHash:   j.CommandHash,
			}, actor); err != nil {
				return sealed, fmt.Errorf("seal interrupted job %s: %w", id, err)
			}
			m.seen[id] = struct{}{}
			m.outcome[id] = events.StatusInterrupted
			sealed++
			m.log.Warn("job was running when the worker last stopped; sealed as interrupted", "job_id", id, "started_at", j.StartedAt)
		}
		if err := st.remove(id); err != nil {
			return sealed, fmt.Errorf("job state %s: %w", statePath, err)
		}
	}
	return sealed, nil
}

// Shutdown stops the worker's jobs in order: no new job is admitted, every
// running rclone is ended, and each of those jobs is sealed as
// transfer.job.interrupted with reason worker_shutdown. It waits up to
// timeout for the records and returns the number of jobs that were running.
// A job that is not sealed in time stays in the state file and is sealed by
// the next start.
func (m *Manager) Shutdown(timeout time.Duration) int {
	m.mu.Lock()
	m.stopping = true
	n := m.running
	for _, cancel := range m.active {
		cancel()
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		m.log.Error("jobs did not end in time; they are sealed as interrupted by the next start", "timeout", timeout.String())
	}
	return n
}

// ActiveCount reports the number of currently running jobs.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// SupportedActions is advertised in health/capabilities.
func (m *Manager) SupportedActions() []string { return m.pol.SupportedActions() }

// Health reports "healthy", or "unhealthy" once the evidence journal could
// not be written: from then on the worker refuses every job.
func (m *Manager) Health() (status, detail string) {
	if err := m.pub.EvidenceErr(); err != nil {
		return "unhealthy", "evidence journal write failed, no jobs are accepted until the worker is restarted: " + err.Error()
	}
	return "healthy", ""
}

// Handle accepts one raw command payload from the command topic and queues it
// for handling. It never blocks. The MQTT client calls it from its message
// callback, and a callback that waits for a publish can keep the client from
// processing acknowledgements and from reconnecting. Commands are handled one
// at a time in arrival order; when the queue is full, the command is dropped
// and logged as an error.
func (m *Manager) Handle(payload []byte) {
	select {
	case m.queue <- append([]byte(nil), payload...):
	default:
		m.log.Error("command queue full, command dropped", "job_id", commands.PeekJobID(payload), "queue_size", commandQueueSize)
	}
}

func (m *Manager) dispatch() {
	for payload := range m.queue {
		m.handle(payload)
	}
}

// handle processes one command payload.
func (m *Manager) handle(payload []byte) {
	cmd, err := commands.Decode(payload)
	if err != nil {
		m.handleSchemaError(payload, err)
		return
	}
	cmdHash, _ := evidence.Hash(cmd)

	switch cmd.Action {
	case commands.ActionCancel:
		m.handleCancel(cmd)
		return
	case commands.ActionStatus:
		m.handleStatus(cmd)
		return
	}

	// Without a working journal no outcome can be sealed, so nothing runs.
	if err := m.pub.EvidenceErr(); err != nil {
		m.log.Error("command refused: evidence journal unavailable", "job_id", cmd.JobID, "error", err)
		m.pub.Status(cmd.JobID, "rejected", "evidence journal unavailable, the worker accepts no jobs until it is restarted")
		return
	}

	dec := m.pol.Evaluate(cmd)
	if !dec.Allowed {
		m.log.Warn("command rejected by policy", "job_id", cmd.JobID, "reason", dec.Reason, "detail", dec.Detail)
		m.reject(cmd.JobID, dec.Reason, dec.Detail, cmdHash, actorFor(cmd))
		return
	}
	// Run exactly what the policy checked: the normalised source and target.
	// command_hash stays the hash of the command as received.
	run := *cmd
	run.Source, run.Target = dec.Source, dec.Target

	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		m.reject(cmd.JobID, "worker_stopping", "the worker is shutting down and starts no job; send the command again after it is back", cmdHash, actorFor(cmd))
		return
	}
	if _, dup := m.seen[cmd.JobID]; dup {
		last := m.outcome[cmd.JobID]
		m.mu.Unlock()
		detail := "job_id already processed"
		if last != "" {
			detail += ", last status: " + last
		}
		m.reject(cmd.JobID, "duplicate_job_id", detail, cmdHash, actorFor(cmd))
		return
	}
	if m.running >= m.pol.MaxParallelJobs {
		m.mu.Unlock()
		m.reject(cmd.JobID, "too_many_jobs", fmt.Sprintf("max_parallel_jobs (%d) reached", m.pol.MaxParallelJobs), cmdHash, actorFor(cmd))
		return
	}
	m.seen[cmd.JobID] = struct{}{}
	m.outcome[cmd.JobID] = "accepted"
	m.running++
	m.wg.Add(1)
	m.mu.Unlock()

	dryRun := m.pol.DryRunDefault
	if cmd.DryRun != nil {
		dryRun = *cmd.DryRun
	}
	// On disk before rclone starts: after a crash this entry is the only
	// trace that the job began (Recover).
	if err := m.state.add(cmd.JobID, runningJob{
		CommandHash: cmdHash,
		Action:      string(run.Action),
		Source:      run.Source,
		Target:      run.Target,
		Operator:    cmd.Operator,
		Reason:      cmd.Reason,
		DryRun:      dryRun,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		m.mu.Lock()
		delete(m.seen, cmd.JobID)
		delete(m.outcome, cmd.JobID)
		m.running--
		m.mu.Unlock()
		m.wg.Done()
		m.log.Error("job state not written, job not started", "job_id", cmd.JobID, "error", err)
		m.reject(cmd.JobID, "state_unavailable", "the job state could not be written, the job was not started", cmdHash, actorFor(cmd))
		return
	}

	go m.run(&run, cmdHash, dryRun)
}

func (m *Manager) handleSchemaError(payload []byte, err error) {
	jobID := commands.PeekJobID(payload)
	if jobID == "" {
		m.log.Warn("dropping invalid command without a usable job_id", "error", err)
		return
	}
	cmdHash, _ := evidence.HashBytes(payload)
	m.log.Warn("command rejected: schema violation", "job_id", jobID, "error", err)
	m.reject(jobID, "schema_violation", err.Error(), cmdHash, nil)
}

// reject seals and publishes a rejection, then its status. A duplicate
// job_id does not replace the outcome of the job that ran under it.
func (m *Manager) reject(jobID, reason, detail, cmdHash string, actor *evidence.Actor) {
	if reason != "duplicate_job_id" {
		m.mu.Lock()
		if _, known := m.outcome[jobID]; !known {
			m.outcome[jobID] = "rejected"
		}
		m.mu.Unlock()
	}
	statusDetail := detail
	if err := m.pub.Result(events.ResultEvent{
		JobID:         jobID,
		Status:        "rejected",
		Reason:        reason,
		Detail:        detail,
		PolicyVersion: m.pol.PolicyVersion,
		PolicyHash:    m.pol.Hash,
		CommandHash:   cmdHash,
	}, actor); err != nil {
		m.sealFailed(jobID, err)
		statusDetail += "; " + notSealed
	}
	m.pub.Status(jobID, "rejected", statusDetail)
}

const notSealed = "the outcome was not sealed and not published: evidence journal write failed"

// sealFailed logs a result that could not be sealed. The sealer refuses
// every later record, health turns unhealthy and new jobs are refused.
func (m *Manager) sealFailed(jobID string, err error) {
	m.log.Error("evidence journal write failed: result not sealed and not published; the worker accepts no jobs until it is restarted", "job_id", jobID, "error", err)
}

// actorFor derives the evidence actor from a command's self-declared operator.
func actorFor(c *commands.Command) *evidence.Actor {
	if c != nil && c.Operator != "" {
		return &evidence.Actor{Auth: evidence.ActorAuthSelfDeclared, ID: c.Operator}
	}
	return &evidence.Actor{Auth: evidence.ActorAuthNone}
}

func (m *Manager) run(c *commands.Command, cmdHash string, dryRun bool) {
	defer m.wg.Done()
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.active[c.JobID] = cancel
	if m.stopping {
		// Shutdown ran between admission and this point.
		cancel()
	}
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.active, c.JobID)
		delete(m.cancelRequested, c.JobID)
		m.running--
		m.mu.Unlock()
	}()

	m.pub.Status(c.JobID, "accepted", "")
	m.mu.Lock()
	m.outcome[c.JobID] = "started"
	m.mu.Unlock()
	m.pub.Status(c.JobID, "started", "")

	m.pub.Log(c.JobID, "INFO", fmt.Sprintf("starting %s %s -> %s (dry_run=%v)", c.Action, c.Source, c.Target, dryRun))

	started := time.Now()
	out, runErr := m.runner.Run(ctx, c, dryRun, func(p rclone.Progress) {
		pct := 0.0
		if p.BytesTotal > 0 {
			pct = float64(p.BytesTransferred) / float64(p.BytesTotal) * 100
		}
		m.pub.Progress(c.JobID, events.ProgressEvent{
			FilesTotal:       p.FilesTotal,
			FilesTransferred: p.FilesTransferred,
			BytesTotal:       p.BytesTotal,
			BytesTransferred: p.BytesTransferred,
			Percent:          pct,
		})
	})
	finished := time.Now()

	m.mu.Lock()
	stopping := m.stopping
	_, userCancel := m.cancelRequested[c.JobID]
	m.mu.Unlock()
	// A run that ended on its own keeps its outcome, also when the shutdown
	// or a cancel arrived just after it: only a run that was cut short is
	// cancelled or interrupted.
	cutShort := out.Canceled || (runErr != nil && ctx.Err() != nil)
	status := "completed"
	switch {
	case cutShort && userCancel:
		status = "cancelled"
	case cutShort && stopping:
		status = events.StatusInterrupted
	case out.Canceled:
		status = "cancelled"
	case runErr != nil || out.ExitCode != 0:
		status = "failed"
	}

	res := events.ResultEvent{
		JobID:            c.JobID,
		Action:           string(c.Action),
		Source:           c.Source,
		Target:           c.Target,
		Status:           status,
		Justification:    c.Reason,
		StartedAt:        started.UTC().Format(time.RFC3339),
		FinishedAt:       finished.UTC().Format(time.RFC3339),
		DurationMS:       finished.Sub(started).Milliseconds(),
		DryRun:           &dryRun,
		FilesTotal:       out.FilesTotal,
		FilesTransferred: out.FilesTransferred,
		BytesTransferred: out.BytesTransferred,
		Errors:           out.Errors,
		PolicyVersion:    m.pol.PolicyVersion,
		PolicyHash:       m.pol.Hash,
		CommandHash:      cmdHash,
	}
	var notes []string
	if status == events.StatusInterrupted {
		res.Reason = events.ReasonWorkerShutdown
		res.Detail = interruptedDetail
		notes = append(notes, "worker shutdown")
	}
	if dryRun {
		// rclone counts what it would have transferred; nothing was.
		res.FilesTotal = max(out.FilesTotal, out.FilesTransferred)
		res.FilesTransferred, res.BytesTransferred = 0, 0
		notes = append(notes, "dry run, nothing was transferred")
	}
	if m.pol.MaxFileSizeMB > 0 {
		res.MaxFileSizeMB = m.pol.MaxFileSizeMB
		notes = append(notes, fmt.Sprintf("files larger than %d MiB are excluded by max_file_size_mb and not counted", m.pol.MaxFileSizeMB))
	}
	// The exit code is evidence only if rclone ran: a clean exit, or an exit
	// with a code. When it could not start (binary missing, bad arguments),
	// there is no code, and a 0 would read as success in a failed record.
	var exitErr *exec.ExitError
	if runErr == nil || errors.As(runErr, &exitErr) {
		exit := out.ExitCode
		res.RcloneExitCode = &exit
	}
	if runErr != nil && len(res.Errors) == 0 {
		res.Errors = []string{logging.Redact(runErr.Error())}
	}
	m.mu.Lock()
	m.outcome[c.JobID] = status
	m.mu.Unlock()
	if err := m.pub.Result(res, actorFor(c)); err != nil {
		// The job stays in the state file: the next start seals it as
		// interrupted, because no outcome is on record.
		m.sealFailed(c.JobID, err)
		notes = append(notes, notSealed)
	} else if err := m.state.remove(c.JobID); err != nil {
		// Harmless for the evidence: Recover finds the outcome in the
		// journal and only clears the entry.
		m.log.Error("job state not updated after the outcome was sealed", "job_id", c.JobID, "error", err)
	}
	m.pub.Status(c.JobID, status, strings.Join(notes, "; "))
}

func (m *Manager) handleCancel(c *commands.Command) {
	m.mu.Lock()
	cancel, ok := m.active[c.TargetJobID]
	m.mu.Unlock()
	if !ok {
		m.pub.Status(c.JobID, "rejected", "no active job "+c.TargetJobID)
		return
	}
	m.mu.Lock()
	m.cancelRequested[c.TargetJobID] = struct{}{}
	m.mu.Unlock()
	m.pub.Log(c.TargetJobID, "INFO", "cancellation requested by "+c.JobID)
	cancel()
	m.pub.Status(c.JobID, "completed", "cancel signal sent to "+c.TargetJobID)
}

// handleStatus answers on the query's own topic with the target job's last
// known status; it never publishes on the target job's topics.
func (m *Manager) handleStatus(c *commands.Command) {
	m.mu.Lock()
	st, known := m.outcome[c.TargetJobID]
	m.mu.Unlock()
	if !known {
		st = "unknown"
	}
	m.pub.StatusAnswer(c.JobID, c.TargetJobID, st)
}
