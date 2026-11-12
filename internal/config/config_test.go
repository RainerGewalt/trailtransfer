package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimal = `worker_id: worker-01
mqtt:
  host: localhost
policy_file: /config/worker-policy.yaml
rclone_config: /config/rclone.conf
`

// A misspelt key used to be ignored: "evidence_jounral" left the journal
// off and "signing_kye" left the records unsigned, without a word.
func TestLoadRejectsUnknownKeys(t *testing.T) {
	for _, line := range []string{
		"evidence_jounral: /data/evidence/journal.jsonl",
		"signing_kye: /config/worker-01.key",
		"mqtt_host: broker",
	} {
		key := strings.SplitN(line, ":", 2)[0]
		if _, err := Load(writeConfig(t, minimal+line+"\n")); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("config with %q: want an error naming %s, got %v", line, key, err)
		}
	}
	if _, err := Load(writeConfig(t, strings.Replace(minimal, "  host:", "  hots:", 1))); err == nil {
		t.Error("a misspelt key inside mqtt must be an error")
	}
}

func TestLoadAcceptsTheExampleConfig(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "config", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkerID != "worker-01" || c.EvidenceJournal == "" || c.CommandTopic() != "trailtransfer/worker-01/commands" {
		t.Fatalf("example config read wrongly: %+v", c)
	}
}

func TestEnvironmentOverridesTheFile(t *testing.T) {
	t.Setenv("TRAILTRANSFER_WORKER_ID", "worker-02")
	t.Setenv("TRAILTRANSFER_MQTT_PORT", "15999")
	c, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkerID != "worker-02" || c.MQTT.Port != 15999 || c.JobStatusTopic("j-1") != "trailtransfer/worker-02/jobs/j-1/status" {
		t.Fatalf("environment not applied: %+v", c)
	}
}
