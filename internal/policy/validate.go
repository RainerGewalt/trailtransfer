package policy

import (
	"fmt"

	"github.com/RainerGewalt/trailtransfer/internal/commands"
)

// Decision is the outcome of evaluating a command against the policy.
type Decision struct {
	Allowed bool
	Reason  string // machine-readable, e.g. "policy_violation"
	Detail  string // human-readable explanation

	// Source and Target are the normalised source and target of an allowed
	// command: the form that was checked, that rclone receives and that the
	// result records.
	Source string
	Target string
}

// Evaluate checks an executable command (copy/move/sync/check) against the
// policy. Control actions (cancel/status) are handled before policy and must
// not be passed here.
func (p *Policy) Evaluate(c *commands.Command) Decision {
	if p.RequireJobID && c.JobID == "" {
		return deny("job_id is required by policy")
	}
	if !p.actionAllowed(c.Action) {
		return deny("action %q is not in allowed_actions", c.Action)
	}
	if c.Action == commands.ActionSync && !p.AllowSyncDelete {
		return deny("sync (which may delete on the target) is not permitted: allow_sync_delete is false")
	}
	if c.Action == commands.ActionMove && !p.AllowDelete {
		return deny("move (which deletes the source after the transfer) is not permitted: allow_delete is false")
	}

	src, err := parseLocation(c.Source)
	if err != nil {
		return deny("source %s %s", quote(c.Source), err)
	}
	if !p.sourceAllowed(src) {
		return deny("source %s is not under any allowed_sources root", quote(c.Source))
	}

	dst, err := parseLocation(c.Target)
	if err != nil {
		return deny("target %s %s", quote(c.Target), err)
	}
	if dst.remote == "" && !p.AllowAbsoluteTargets {
		return deny("absolute local target %s is not permitted: allow_absolute_targets is false", quote(c.Target))
	}
	if !p.targetAllowed(dst) {
		return deny("target %s is not under any allowed_targets entry", quote(c.Target))
	}
	return Decision{Allowed: true, Source: src.String(), Target: dst.String()}
}

func (p *Policy) actionAllowed(a commands.Action) bool {
	for _, x := range p.AllowedActions {
		if x == string(a) {
			return true
		}
	}
	return false
}

// sourceAllowed requires the source to equal or sit beneath one of the
// allowed roots, after normalisation and, for local paths, after resolving
// symbolic links.
func (p *Policy) sourceAllowed(src location) bool {
	return anyContains(p.AllowedSources, src)
}

// targetAllowed permits a target that equals an allowed target or sits beneath
// it (allowed "sftp:/upload" also permits "sftp:/upload/sub"), with the same
// normalisation and link resolution as sources.
func (p *Policy) targetAllowed(dst location) bool {
	return anyContains(p.AllowedTargets, dst)
}

func anyContains(roots []string, loc location) bool {
	for _, r := range roots {
		root, err := parseLocation(r)
		if err != nil {
			continue // Load refuses such entries; a policy built in code may still hold one
		}
		if contained(loc, root) {
			return true
		}
	}
	return false
}

func deny(format string, args ...any) Decision {
	return Decision{Allowed: false, Reason: "policy_violation", Detail: fmt.Sprintf(format, args...)}
}
