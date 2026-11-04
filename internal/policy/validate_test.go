package policy

import (
	"testing"

	"github.com/RainerGewalt/trailtransfer/internal/commands"
)

func testPolicy() *Policy {
	return &Policy{
		PolicyVersion:   "1",
		AllowedActions:  []string{"copy", "move", "check"},
		AllowedSources:  []string{"/data/input", "/data/reports"},
		AllowedTargets:  []string{"sftp-demo:/upload", "s3-demo:trailtransfer"},
		MaxParallelJobs: 2,
		RequireJobID:    true,
	}
}

func cmd(action commands.Action, source, target string) *commands.Command {
	return &commands.Command{JobID: "j", Action: action, Source: source, Target: target}
}

func TestEvaluateAllows(t *testing.T) {
	if d := testPolicy().Evaluate(cmd(commands.ActionCopy, "/data/input", "sftp-demo:/upload")); !d.Allowed {
		t.Fatalf("expected allow, got %+v", d)
	}
	// sub-path under an allowed root/target
	if d := testPolicy().Evaluate(cmd(commands.ActionCopy, "/data/input/sub", "sftp-demo:/upload/nested")); !d.Allowed {
		t.Fatalf("expected allow for sub-paths, got %+v", d)
	}
}

func TestEvaluateDeniesAction(t *testing.T) {
	if d := testPolicy().Evaluate(cmd(commands.ActionSync, "/data/input", "sftp-demo:/upload")); d.Allowed {
		t.Fatal("sync must be denied (not in allowed_actions)")
	}
}

func TestEvaluateDeniesSource(t *testing.T) {
	if d := testPolicy().Evaluate(cmd(commands.ActionCopy, "/etc", "sftp-demo:/upload")); d.Allowed {
		t.Fatal("source /etc must be denied")
	}
}

func TestEvaluateDeniesTraversal(t *testing.T) {
	if d := testPolicy().Evaluate(cmd(commands.ActionCopy, "/data/input/../../etc", "sftp-demo:/upload")); d.Allowed {
		t.Fatal("path traversal must be denied")
	}
}

func TestEvaluateDeniesTarget(t *testing.T) {
	if d := testPolicy().Evaluate(cmd(commands.ActionCopy, "/data/input", "sftp-prod:/etc")); d.Allowed {
		t.Fatal("target not in allowlist must be denied")
	}
}

func TestSyncNeedsAllowSyncDelete(t *testing.T) {
	p := testPolicy()
	p.AllowedActions = append(p.AllowedActions, "sync")
	if d := p.Evaluate(cmd(commands.ActionSync, "/data/input", "sftp-demo:/upload")); d.Allowed {
		t.Fatal("sync must still be denied while allow_sync_delete is false")
	}
	p.AllowSyncDelete = true
	if d := p.Evaluate(cmd(commands.ActionSync, "/data/input", "sftp-demo:/upload")); !d.Allowed {
		t.Fatalf("sync should be allowed once allow_sync_delete is true, got %+v", d)
	}
}

func TestRequireJobID(t *testing.T) {
	p := testPolicy() // RequireJobID: true
	c := &commands.Command{Action: commands.ActionCopy, Source: "/data/input", Target: "s3-demo:trailtransfer"}
	if d := p.Evaluate(c); d.Allowed {
		t.Fatal("empty job_id must be denied when require_job_id is true")
	}
}

func TestAbsoluteLocalTargetDenied(t *testing.T) {
	p := testPolicy()
	p.AllowedTargets = append(p.AllowedTargets, "/mnt/out")
	if d := p.Evaluate(cmd(commands.ActionCopy, "/data/input", "/mnt/out")); d.Allowed {
		t.Fatal("absolute local target must be denied while allow_absolute_targets is false")
	}
	p.AllowAbsoluteTargets = true
	if d := p.Evaluate(cmd(commands.ActionCopy, "/data/input", "/mnt/out")); !d.Allowed {
		t.Fatalf("absolute local target should be allowed once permitted, got %+v", d)
	}
}
