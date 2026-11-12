package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RainerGewalt/trailtransfer/internal/commands"
)

// Targets that leave the allowed target path, or spell it so that the
// comparison and rclone could disagree about where it points (audit S1).
var targetsOutsideTheBoundary = []string{
	// the forms from the audit probe
	"sftp-demo:/upload/../../etc",
	"sftp-demo:/upload/..",
	"s3-demo:trailtransfer/../other-bucket",
	"sftp-demo:/upload/./../home",
	// other spellings of a parent segment
	"sftp-demo:/upload/sub/../../etc",
	"sftp-demo:/upload//..//etc",
	"sftp-demo:/upload/...",
	"sftp-demo:/upload/.. /etc",
	"sftp-demo:/upload/．．/etc",
	// encoded separators and dots
	"sftp-demo:/upload/%2e%2e/etc",
	"sftp-demo:/upload/%2E%2E%2Fetc",
	"sftp-demo:/upload/..%2fetc",
	"sftp-demo:/upload/%252e%252e/etc",
	"sftp-demo:/upload/x%5c..%5cetc",
	"sftp-demo:/upload\\..\\etc",
	// a different place that only looks similar
	"sftp-demo:upload",
	"sftp-demo:/Upload",
	"SFTP-demo:/upload",
	"sftp-demo:/uploadx",
	"s3-demo:trailtransfer2",
	"s3-demo:/trailtransfer",
	// another backend configuration than the allowlisted alias
	"sftp-demo,host=other.example:/upload",
	":sftp,host=other.example:/upload",
	":local:/upload",
	// control characters
	"sftp-demo:/upload/a\x00b",
	"sftp-demo:/upload/a\nb",
}

func TestEvaluateRefusesTargetsOutsideTheAllowedPath(t *testing.T) {
	for _, target := range targetsOutsideTheBoundary {
		if d := testPolicy().Evaluate(cmd(commands.ActionCopy, "/data/input", target)); d.Allowed {
			t.Errorf("target %q must be refused", target)
		}
	}
}

var sourcesOutsideTheBoundary = []string{
	"/data/input/../../etc",
	"/data/input/..",
	"/data/input/./../reports",
	"/data/input//..//etc",
	"/data/input/%2e%2e/etc",
	"/data/input/..%2f..%2fetc",
	"/data/input\\..\\etc",
	"/data/input/.../etc",
	"/data/input-other",
	"/DATA/input",
	"data/input",
	"./data/input",
	"/data/input/a\x00b",
}

func TestEvaluateRefusesSourcesOutsideTheAllowedRoot(t *testing.T) {
	for _, source := range sourcesOutsideTheBoundary {
		if d := testPolicy().Evaluate(cmd(commands.ActionCopy, source, "sftp-demo:/upload")); d.Allowed {
			t.Errorf("source %q must be refused", source)
		}
	}
}

// "report..2026" is a name, not a parent segment.
func TestEvaluateAllowsDotsInsideAName(t *testing.T) {
	if d := testPolicy().Evaluate(cmd(commands.ActionCopy, "/data/input/report..2026", "sftp-demo:/upload/report..2026")); !d.Allowed {
		t.Fatalf("dots inside a name must be allowed, got %+v", d)
	}
}

// symlinkTree builds root/{sub,inside -> sub,link -> ../outside} and
// outside/secret.txt under a temporary directory.
func symlinkTree(t *testing.T) (dir, root string) {
	t.Helper()
	dir = t.TempDir()
	root = filepath.Join(dir, "input")
	for _, d := range []string{filepath.Join(root, "sub"), filepath.Join(dir, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "outside", "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for link, to := range map[string]string{
		filepath.Join(root, "link"):    filepath.Join(dir, "outside"),
		filepath.Join(root, "inside"):  filepath.Join(root, "sub"),
		filepath.Join(dir, "rootlink"): root,
	} {
		if err := os.Symlink(to, link); err != nil {
			t.Fatal(err)
		}
	}
	return dir, root
}

// rclone follows a symbolic link at the root of a local source, so a link
// under an allowed root that points elsewhere must not pass the policy.
func TestEvaluateResolvesSymlinksInLocalSources(t *testing.T) {
	dir, root := symlinkTree(t)
	p := testPolicy()
	p.AllowedSources = []string{root}
	for _, source := range []string{root + "/link", root + "/link/secret.txt", root + "/sub/../link"} {
		if d := p.Evaluate(cmd(commands.ActionCopy, source, "sftp-demo:/upload")); d.Allowed {
			t.Errorf("source %q leaves the root through a symbolic link and must be refused", strings.TrimPrefix(source, dir))
		}
	}
	for _, source := range []string{root, root + "/sub", root + "/inside", root + "/not-there-yet/deeper"} {
		if d := p.Evaluate(cmd(commands.ActionCopy, source, "sftp-demo:/upload")); !d.Allowed {
			t.Errorf("source %q stays inside the root and must be allowed, got %+v", strings.TrimPrefix(source, dir), d)
		}
	}
	// An allowed root that is itself a symbolic link is compared by what it
	// points to.
	p.AllowedSources = []string{filepath.Join(dir, "rootlink")}
	if d := p.Evaluate(cmd(commands.ActionCopy, filepath.Join(dir, "rootlink", "sub"), "sftp-demo:/upload")); !d.Allowed {
		t.Errorf("a source under a symlinked root must be allowed, got %+v", d)
	}
	if d := p.Evaluate(cmd(commands.ActionCopy, filepath.Join(dir, "rootlink", "link"), "sftp-demo:/upload")); d.Allowed {
		t.Error("a link under a symlinked root that points outside it must be refused")
	}
}

func TestEvaluateResolvesSymlinksInLocalTargets(t *testing.T) {
	dir, root := symlinkTree(t)
	p := testPolicy()
	p.AllowAbsoluteTargets = true
	p.AllowedTargets = []string{root}
	if d := p.Evaluate(cmd(commands.ActionCopy, "/data/input", root+"/link/x")); d.Allowed {
		t.Errorf("local target %q leaves the allowed target through a symbolic link and must be refused", strings.TrimPrefix(root+"/link/x", dir))
	}
	if d := p.Evaluate(cmd(commands.ActionCopy, "/data/input", root+"/new/x")); !d.Allowed {
		t.Errorf("a local target that does not exist yet under the allowed target must be allowed, got %+v", d)
	}
}

// move deletes the source after the transfer; allow_delete: false must stop
// it (audit S2).
func TestEvaluateRefusesMoveWhileAllowDeleteIsFalse(t *testing.T) {
	p := testPolicy() // allows move; AllowDelete false
	d := p.Evaluate(cmd(commands.ActionMove, "/data/input", "sftp-demo:/upload"))
	if d.Allowed || !strings.Contains(d.Detail, "allow_delete") {
		t.Fatalf("move must be refused with allow_delete in the detail while allow_delete is false, got %+v", d)
	}
	p.AllowDelete = true
	if d := p.Evaluate(cmd(commands.ActionMove, "/data/input", "sftp-demo:/upload")); !d.Allowed {
		t.Fatalf("move must be allowed once allow_delete is true, got %+v", d)
	}
}

// The form that was checked is the form rclone receives.
func TestEvaluateHandsOnTheNormalisedForm(t *testing.T) {
	for target, want := range map[string]string{
		"sftp-demo:/upload":              "sftp-demo:/upload",
		"sftp-demo:/upload/":             "sftp-demo:/upload",
		"sftp-demo://upload//sub/./x/":   "sftp-demo:/upload/sub/x",
		"s3-demo:trailtransfer/./a//b":   "s3-demo:trailtransfer/a/b",
		"sftp-demo:/upload/report..2026": "sftp-demo:/upload/report..2026",
		"sftp-demo:/upload/100%":         "sftp-demo:/upload/100%",
	} {
		d := testPolicy().Evaluate(cmd(commands.ActionCopy, "/data/input//sub/./", target))
		if !d.Allowed || d.Target != want || d.Source != "/data/input/sub" {
			t.Errorf("target %q: want allowed with target %q and source /data/input/sub, got %+v", target, want, d)
		}
	}
}
