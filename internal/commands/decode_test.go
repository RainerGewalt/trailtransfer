package commands

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeValidCopy(t *testing.T) {
	c, err := Decode([]byte(`{"job_id":"job-001","action":"copy","source":"/data/input","target":"sftp-demo:/upload","recursive":true,"filters":["*.jpg"],"dry_run":false}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.JobID != "job-001" || c.Action != ActionCopy {
		t.Fatalf("unexpected command: %+v", c)
	}
	if c.DryRun == nil || *c.DryRun != false {
		t.Fatalf("dry_run should be present and false, got %v", c.DryRun)
	}
	if c.Recursive == nil || *c.Recursive != true {
		t.Fatalf("recursive should be present and true")
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	_, err := Decode([]byte(`{"job_id":"j","action":"copy","source":"/a","target":"r:/b","extra_args":"--delete"}`))
	if err == nil {
		t.Fatal("expected rejection of unknown field (no free-form args allowed)")
	}
}

func TestDecodeRequiresJobID(t *testing.T) {
	if _, err := Decode([]byte(`{"action":"copy","source":"/a","target":"r:/b"}`)); err == nil {
		t.Fatal("expected error for missing job_id")
	}
}

func TestDecodeRejectsUnknownAction(t *testing.T) {
	if _, err := Decode([]byte(`{"job_id":"j","action":"exec","source":"/a","target":"r:/b"}`)); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestDecodeCancelRequiresTarget(t *testing.T) {
	if _, err := Decode([]byte(`{"job_id":"c1","action":"cancel"}`)); err == nil {
		t.Fatal("expected cancel to require target_job_id")
	}
	if _, err := Decode([]byte(`{"job_id":"c1","action":"cancel","target_job_id":"job-1"}`)); err != nil {
		t.Fatalf("valid cancel should decode: %v", err)
	}
}

func TestDryRunAbsentIsNil(t *testing.T) {
	c, err := Decode([]byte(`{"job_id":"j","action":"copy","source":"/a","target":"r:/b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.DryRun != nil {
		t.Fatal("absent dry_run must stay nil so policy default can apply")
	}
}

// commandWithJobID builds a copy command with the given job_id, JSON-encoded
// so that control characters reach the decoder as JSON escapes.
func commandWithJobID(t *testing.T, field, id string) []byte {
	t.Helper()
	m := map[string]any{"job_id": "job-1", "action": "copy", "source": "/a", "target": "r:/b"}
	if field == "target_job_id" {
		m["action"] = "status"
		m["target_job_id"] = id
		delete(m, "source")
		delete(m, "target")
	} else {
		m["job_id"] = id
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A job_id becomes one level of the worker's MQTT topics. A value that is not
// a valid topic level made the broker disconnect the worker (audit finding E1).
var invalidJobIDs = []string{
	"bad#id", "bad+id", "a/b", "/", "#", "+", "nul\x00id", "tab\tid", "new\nline",
	"sp ace", "ümlaut", "emoji\U0001F600", strings.Repeat("a", 129),
}

func TestDecodeRejectsJobIDsThatAreNotASafeTopicLevel(t *testing.T) {
	for _, id := range invalidJobIDs {
		if _, err := Decode(commandWithJobID(t, "job_id", id)); err == nil || !strings.Contains(err.Error(), "job_id") {
			t.Errorf("job_id %q: want a schema error naming job_id, got %v", id, err)
		}
	}
}

func TestDecodeRejectsTargetJobIDsThatAreNotASafeTopicLevel(t *testing.T) {
	for _, id := range invalidJobIDs {
		if _, err := Decode(commandWithJobID(t, "target_job_id", id)); err == nil || !strings.Contains(err.Error(), "target_job_id") {
			t.Errorf("target_job_id %q: want a schema error naming target_job_id, got %v", id, err)
		}
	}
}

func TestDecodeAcceptsUsualJobIDs(t *testing.T) {
	for _, id := range []string{"job-001", "demo-2", "2026.11.11_run-7", "A", "a.b_c-D9", strings.Repeat("a", 128)} {
		if _, err := Decode(commandWithJobID(t, "job_id", id)); err != nil {
			t.Errorf("job_id %q: %v", id, err)
		}
		if _, err := Decode(commandWithJobID(t, "target_job_id", id)); err != nil {
			t.Errorf("target_job_id %q: %v", id, err)
		}
	}
}

func TestDecodeRejectsAnOversizedCommand(t *testing.T) {
	big := `{"job_id":"job-1","action":"copy","source":"/a","target":"r:/b","reason":"` + strings.Repeat("x", 64*1024) + `"}`
	if _, err := Decode([]byte(big)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("want a size error for a %d-byte command, got %v", len(big), err)
	}
}

// PeekJobID routes a rejection to a job topic, so it must never hand out a
// job_id that is not a safe topic level.
func TestPeekJobIDIgnoresJobIDsThatAreNotASafeTopicLevel(t *testing.T) {
	for _, id := range invalidJobIDs {
		b, _ := json.Marshal(map[string]any{"job_id": id, "action": "exec"})
		if got := PeekJobID(b); got != "" {
			t.Errorf("PeekJobID with job_id %q = %q, want empty", id, got)
		}
	}
	if got := PeekJobID([]byte(`{"job_id":"job-7","action":"exec"}`)); got != "job-7" {
		t.Fatalf("PeekJobID of a valid job_id = %q, want job-7", got)
	}
}
