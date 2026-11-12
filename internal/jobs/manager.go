// Package jobs turns validated commands into governed, deduplicated, bounded
// job executions and emits the full status/progress/result lifecycle.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
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
	// handled: accepted, started, then completed, failed or cancelled; or
	// rejected. It answers status queries and, like seen, lives only as long
	// as the process.
	outcome map[string]string
	running int

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
	}
	go m.dispatch()
	return m
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
	if _, dup := m.seen[cmd.JobID]; dup {
		m.mu.Unlock()
		m.reject(cmd.JobID, "duplicate_job_id", "job_id already processed", cmdHash, actorFor(cmd))
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
	m.mu.Unlock()

	go m.run(&run, cmdHash)
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

func (m *Manager) run(c *commands.Command, cmdHash string) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.active[c.JobID] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.active, c.JobID)
		m.running--
		m.mu.Unlock()
	}()

	m.pub.Status(c.JobID, "accepted", "")
	m.mu.Lock()
	m.outcome[c.JobID] = "started"
	m.mu.Unlock()
	m.pub.Status(c.JobID, "started", "")

	dryRun := m.pol.DryRunDefault
	if c.DryRun != nil {
		dryRun = *c.DryRun
	}
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

	status := "completed"
	switch {
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
		m.sealFailed(c.JobID, err)
		notes = append(notes, notSealed)
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
