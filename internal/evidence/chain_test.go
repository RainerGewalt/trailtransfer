package evidence

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestSealer(t *testing.T, path string) *Sealer {
	t.Helper()
	s, err := NewSealer(Options{
		ProducerID:      "worker-01",
		ProducerVersion: "test",
		Policy:          Policy{ID: "worker-policy.yaml", Version: "1", Hash: GenesisHash, Kind: PolicyKindTransfer},
		JournalPath:     path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sealN(t *testing.T, path string, n int) {
	t.Helper()
	s := newTestSealer(t, path)
	for i := 0; i < n; i++ {
		if _, err := s.Seal("transfer.job.result", "job-"+string(rune('a'+i)), map[string]any{"status": "completed", "n": i}, &Actor{Auth: ActorAuthNone}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func readRecords(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
}

func writeRecords(t *testing.T, path string, lines [][]byte) {
	t.Helper()
	if err := os.WriteFile(path, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func decode(t *testing.T, line []byte) Record {
	t.Helper()
	var r Record
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSealProducesSpecRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	s := newTestSealer(t, path)
	raw, err := s.Seal("transfer.job.result", "job-1", map[string]any{"b": 1, "a": "x"}, &Actor{Auth: ActorAuthSelfDeclared, ID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	r := decode(t, raw)
	if r.Schema != Schema || r.Producer.Kind != ProducerTransferWorker || r.Producer.ID != "worker-01" || r.Producer.Version != "test" {
		t.Fatalf("producer/schema wrong: %+v", r)
	}
	if r.StreamID != "worker-01" || r.Seq != 0 || r.PrevHash != GenesisHash {
		t.Fatalf("genesis record wrong: %+v", r)
	}
	if !r.ChainRestart || r.AnchorHash != GenesisHash {
		t.Fatalf("first record of a process must be a restart anchored at genesis: %+v", r)
	}
	if r.EventType != "transfer.job.result" || r.JobID != "job-1" {
		t.Fatalf("event_type/job_id wrong: %+v", r)
	}
	if r.Time.Source != TimeSourceSystem {
		t.Fatalf("time.source wrong: %+v", r.Time)
	}
	if ts, err := time.Parse(time.RFC3339Nano, r.Time.RecordedAt); err != nil || !strings.HasSuffix(r.Time.RecordedAt, "Z") || time.Since(ts) > time.Minute {
		t.Fatalf("time.recorded_at must be RFC 3339 UTC: %q", r.Time.RecordedAt)
	}
	if r.Actor.Auth != ActorAuthSelfDeclared || r.Actor.ID != "alice" {
		t.Fatalf("actor wrong: %+v", r.Actor)
	}
	if r.Policy.ID != "worker-policy.yaml" || r.Policy.Kind != PolicyKindTransfer {
		t.Fatalf("policy wrong: %+v", r.Policy)
	}
	// content_hash = sha256(JCS(payload)); JCS of {"b":1,"a":"x"} is {"a":"x","b":1}.
	if want := sum([]byte(`{"a":"x","b":1}`)); r.ContentHash != want {
		t.Fatalf("content_hash %s, want %s", r.ContentHash, want)
	}
	// chain_hash = sha256(JCS(core)); recompute from the raw members.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(readRecords(t, path)[0], &m); err != nil {
		t.Fatal(err)
	}
	core, err := CoreJCS(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"payload", "chain_hash", "job_id", "signature"} {
		if bytes.Contains(core, []byte(`"`+k+`":`)) {
			t.Fatalf("core must not contain %s: %s", k, core)
		}
	}
	for _, k := range []string{"schema", "producer", "stream_id", "seq", "prev_hash", "event_type", "time", "actor", "policy", "content_hash", "chain_restart", "anchor_hash"} {
		if !bytes.Contains(core, []byte(`"`+k+`":`)) {
			t.Fatalf("core must contain %s: %s", k, core)
		}
	}
	if sum(core) != r.ChainHash {
		t.Fatalf("chain_hash does not equal sha256(JCS(core))")
	}
}

func TestSealContinuesWithinProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 3)
	lines := readRecords(t, path)
	for i, ln := range lines {
		r := decode(t, ln)
		if r.Seq != uint64(i) {
			t.Fatalf("seq of record %d is %d", i, r.Seq)
		}
		if i == 0 {
			continue
		}
		if r.ChainRestart || r.AnchorHash != "" {
			t.Fatalf("record %d must not be a restart", i)
		}
		if prev := decode(t, lines[i-1]); r.PrevHash != prev.ChainHash {
			t.Fatalf("record %d is not linked to its predecessor", i)
		}
	}
	rep, err := Verify(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Entries != 3 || rep.Restarts != 1 {
		t.Fatalf("expected 3 intact records with one genesis restart, got %+v", rep)
	}
}

func TestSealerRestartAnchorsOnExistingJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 2)
	lastBefore := decode(t, readRecords(t, path)[1])

	s2 := newTestSealer(t, path)
	raw, err := s2.Seal("transfer.job.result", "job-x", map[string]any{"status": "completed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s2.Seal("transfer.job.result", "job-y", map[string]any{"status": "completed"}, nil)
	s2.Close()

	r := decode(t, raw)
	if !r.ChainRestart {
		t.Fatal("first record after a restart must carry chain_restart")
	}
	if r.AnchorHash != lastBefore.ChainHash || r.PrevHash != lastBefore.ChainHash {
		t.Fatalf("restart must anchor at the last known chain_hash: %+v", r)
	}
	if r.Seq != 2 {
		t.Fatalf("seq should continue after the recovered record, got %d", r.Seq)
	}
	if r.Actor.Auth != ActorAuthNone {
		t.Fatalf("nil actor must be recorded as auth none, got %+v", r.Actor)
	}
	rep, _ := Verify(path, nil)
	if !rep.OK || rep.Entries != 4 || rep.Restarts != 2 || rep.Count(CodeChainBreak) != 0 {
		t.Fatalf("restart must verify as a restart, not a break: %+v", rep)
	}
}

func TestSealerRefusesUnreadableJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSealer(Options{ProducerID: "w", JournalPath: path}); err == nil {
		t.Fatal("a corrupt journal must not be silently extended")
	}
}

func TestSealerWithoutJournalRestartsFromGenesis(t *testing.T) {
	s, err := NewSealer(Options{ProducerID: "w"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := s.Seal("transfer.job.result", "", map[string]any{"k": "v"}, nil)
	r := decode(t, raw)
	if r.Seq != 0 || !r.ChainRestart || r.AnchorHash != GenesisHash || r.Policy != NoPolicy {
		t.Fatalf("in-memory sealer must start at genesis with NoPolicy: %+v", r)
	}
}

func TestSealRejectsEmptyProducer(t *testing.T) {
	if _, err := NewSealer(Options{}); err == nil {
		t.Fatal("producer id is required")
	}
}

func TestSealPayloadIsVerbatim(t *testing.T) {
	s, _ := NewSealer(Options{ProducerID: "w"})
	raw, _ := s.Seal("transfer.job.result", "", map[string]any{"amount": 1, "errors": []string{}}, nil)
	r := decode(t, raw)
	if string(r.Payload) != `{"amount":1,"errors":[]}` {
		t.Fatalf("payload altered: %s", r.Payload)
	}
}
