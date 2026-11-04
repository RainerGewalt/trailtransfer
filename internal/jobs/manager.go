// Package jobs turns validated commands into governed, deduplicated, bounded
// job executions and emits the full status/progress/result lifecycle.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
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

	mu      sync.Mutex
	active  map[string]context.CancelFunc
	seen    map[string]struct{}
	running int
}

// NewManager wires the manager to its policy, publisher and runner.
func NewManager(pol *policy.Policy, pub *events.Publisher, runner Runner, log *slog.Logger) *Manager {
	return &Manager{
		pol:    pol,
		pub:    pub,
		runner: runner,
		log:    log,
		active: map[string]context.CancelFunc{},
		seen:   map[string]struct{}{},
	}
}

// ActiveCount reports the number of currently running jobs.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// SupportedActions is advertised in health/capabilities.
func (m *Manager) SupportedActions() []string { return m.pol.SupportedActions() }

// Handle processes one raw command payload from the command topic.
func (m *Manager) Handle(payload []byte) {
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

	if dec := m.pol.Evaluate(cmd); !dec.Allowed {
		m.log.Warn("command rejected by policy", "job_id", cmd.JobID, "reason", dec.Reason, "detail", dec.Detail)
		m.reject(cmd.JobID, dec.Reason, dec.Detail, cmdHash, actorFor(cmd))
		return
	}

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
	m.running++
	m.mu.Unlock()

	go m.run(cmd, cmdHash)
}

func (m *Manager) handleSchemaError(payload []byte, err error) {
	jobID := commands.PeekJobID(payload)
	if jobID == "" {
		m.log.Warn("dropping invalid command with no job_id", "error", err)
		return
	}
	cmdHash, _ := evidence.HashBytes(payload)
	m.log.Warn("command rejected: schema violation", "job_id", jobID, "error", err)
	m.pub.Status(jobID, "rejected", err.Error())
	m.pub.Result(events.ResultEvent{
		JobID:         jobID,
		Status:        "rejected",
		Reason:        "schema_violation",
		Detail:        err.Error(),
		PolicyVersion: m.pol.PolicyVersion,
		PolicyHash:    m.pol.Hash,
		CommandHash:   cmdHash,
	}, nil)
}

func (m *Manager) reject(jobID, reason, detail, cmdHash string, actor *evidence.Actor) {
	m.pub.Status(jobID, "rejected", detail)
	m.pub.Result(events.ResultEvent{
		JobID:         jobID,
		Status:        "rejected",
		Reason:        reason,
		Detail:        detail,
		PolicyVersion: m.pol.PolicyVersion,
		PolicyHash:    m.pol.Hash,
		CommandHash:   cmdHash,
	}, actor)
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
		FilesTotal:       out.FilesTotal,
		FilesTransferred: out.FilesTransferred,
		BytesTransferred: out.BytesTransferred,
		Errors:           out.Errors,
		PolicyVersion:    m.pol.PolicyVersion,
		PolicyHash:       m.pol.Hash,
		CommandHash:      cmdHash,
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
	m.pub.Result(res, actorFor(c))
	m.pub.Status(c.JobID, status, "")
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

func (m *Manager) handleStatus(c *commands.Command) {
	m.mu.Lock()
	_, active := m.active[c.TargetJobID]
	_, seen := m.seen[c.TargetJobID]
	m.mu.Unlock()
	switch {
	case active:
		m.pub.Status(c.TargetJobID, "started", "job is running")
	case seen:
		m.pub.Status(c.TargetJobID, "completed", "job already finished")
	default:
		m.pub.Status(c.TargetJobID, "unknown", "no such job")
	}
	m.pub.Status(c.JobID, "completed", "status reported for "+c.TargetJobID)
}
