package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCLIEnv is runCLI with additional environment variables.
func runCLIEnv(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(append(os.Environ(), "TRAILTRANSFER_TEST_MAIN=1"), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exitErr):
		return out.String(), exitErr.ExitCode()
	default:
		t.Fatalf("running trailtransfer %v: %v", args, err)
		return "", -1
	}
}

func validateConfigWithPolicy(t *testing.T, policy string) (string, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	return runCLIEnv(t, []string{
		"TRAILTRANSFER_CONFIG=",
		"TRAILTRANSFER_WORKER_ID=worker-01",
		"TRAILTRANSFER_MQTT_HOST=localhost",
		"TRAILTRANSFER_POLICY_FILE=" + path,
		"TRAILTRANSFER_RCLONE_CONFIG=/config/rclone.conf",
	}, "validate-config")
}

const cliPolicy = `policy_version: "1"
allowed_actions: [copy, check]
allowed_sources: [/data/input]
allowed_targets: [s3-demo:trailtransfer]
dry_run_default: true
`

// validate-config reported "ok" for a policy with dry_run_defualt and for
// allowed_actions [copy, delete, rm] (audit S4).
func TestValidateConfigFailsOnAPolicyTypo(t *testing.T) {
	for name, policy := range map[string]string{
		"misspelt key":   strings.Replace(cliPolicy, "dry_run_default", "dry_run_defualt", 1),
		"unknown action": strings.Replace(cliPolicy, "[copy, check]", "[copy, delete, rm]", 1),
	} {
		out, code := validateConfigWithPolicy(t, policy)
		if code != 1 || !strings.HasPrefix(out, "invalid:") {
			t.Errorf("%s: exit code %d, want 1 with 'invalid:'; output:\n%s", name, code, out)
		}
	}
}

func TestValidateConfigAcceptsAValidPolicy(t *testing.T) {
	out, code := validateConfigWithPolicy(t, cliPolicy)
	if code != 0 || !strings.HasPrefix(out, "ok:") {
		t.Fatalf("exit code %d, want 0 with 'ok:'; output:\n%s", code, out)
	}
}
