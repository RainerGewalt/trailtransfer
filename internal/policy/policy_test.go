package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validPolicy = `policy_version: "1"
allowed_actions: [copy, check]
allowed_sources: [/data/input]
allowed_targets: [s3-demo:trailtransfer]
dry_run_default: true
`

// A misspelt key used to be ignored, so a typo in dry_run_default turned every
// command without dry_run into a real transfer (audit S4).
func TestLoadRejectsUnknownKeys(t *testing.T) {
	for _, typo := range []string{"dry_run_defualt: true", "allowed_target: [s3-demo:x]", "allow_delete: false\nallow_delet: true"} {
		_, err := Load(writePolicy(t, validPolicy+typo+"\n"))
		key := strings.SplitN(strings.Split(typo, "\n")[len(strings.Split(typo, "\n"))-1], ":", 2)[0]
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("policy with %q: want an error naming %s, got %v", typo, key, err)
		}
	}
}

func TestLoadRejectsUnknownActions(t *testing.T) {
	for _, actions := range []string{"[copy, delete, rm]", "[copy, Copy]", "[copy, status]", "[copy, cancel]"} {
		body := strings.Replace(validPolicy, "[copy, check]", actions, 1)
		if _, err := Load(writePolicy(t, body)); err == nil {
			t.Errorf("allowed_actions %s: want an error", actions)
		}
	}
}

func TestLoadRejectsRootsAndTargetsWithParentSegments(t *testing.T) {
	for _, line := range []string{
		"allowed_sources: [/data/input/../etc]",
		"allowed_sources: [relative/input]",
		"allowed_sources: ['']",
		"allowed_targets: [s3-demo:trailtransfer/../x]",
		"allowed_targets: [':local:/x']",
	} {
		key := strings.SplitN(line, ":", 2)[0]
		body := validPolicy
		for _, l := range strings.Split(validPolicy, "\n") {
			if strings.HasPrefix(l, key+":") {
				body = strings.Replace(body, l, line, 1)
			}
		}
		if _, err := Load(writePolicy(t, body)); err == nil {
			t.Errorf("policy with %s: want an error", line)
		}
	}
}

func TestLoadAcceptsTheExamplePolicy(t *testing.T) {
	p, err := Load(filepath.Join("..", "..", "config", "worker-policy.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.DryRunDefault || p.AllowDelete || len(p.AllowedActions) != 4 {
		t.Fatalf("example policy read wrongly: %+v", p)
	}
}
