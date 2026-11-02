package evidence

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mutate decodes record i of the journal, lets fn change it, re-encodes it
// without recomputing any hash, and writes the journal back.
func mutate(t *testing.T, path string, i int, fn func(r *Record)) {
	t.Helper()
	lines := readRecords(t, path)
	r := decode(t, lines[i])
	fn(&r)
	out, err := json.Marshal(&r)
	if err != nil {
		t.Fatal(err)
	}
	lines[i] = out
	writeRecords(t, path, lines)
}

// reseal recomputes content_hash and chain_hash of record i so that only
// chain-level checks can expose the change.
func reseal(t *testing.T, path string, i int, fn func(r *Record)) {
	t.Helper()
	mutate(t, path, i, func(r *Record) {
		fn(r)
		r.ContentHash, _ = ContentHashOf(r.Payload)
		r.ChainHash, _ = r.computeChainHash()
	})
}

func codes(rep VerifyReport) string {
	var b strings.Builder
	for _, f := range rep.Findings {
		b.WriteString(f.Code)
		b.WriteString(" ")
	}
	return b.String()
}

func TestVerifyIntactChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 5)
	rep, err := Verify(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Entries != 5 || rep.Restarts != 1 || rep.Count(CodeRestart) != 1 || len(rep.Findings) != 1 {
		t.Fatalf("expected OK with exactly the genesis restart, got %+v", rep)
	}
}

func TestVerifyEmptyJournalIsOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _ := Verify(path, nil)
	if !rep.OK || rep.Entries != 0 {
		t.Fatalf("empty journal: %+v", rep)
	}
}

func TestVerifyHashMismatchOnPayloadTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 3)
	data, _ := os.ReadFile(path)
	data = bytes.Replace(data, []byte(`"n":1`), []byte(`"n":9`), 1)
	os.WriteFile(path, data, 0o600)

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeHashMismatch) != 1 || rep.Count(CodeChainBreak) != 0 {
		t.Fatalf("payload tamper must be exactly one hash_mismatch: %+v %s", rep, codes(rep))
	}
	if rep.Findings[1].Seq != 1 || !strings.Contains(rep.Findings[1].Message, "content_hash") {
		t.Fatalf("finding must name the record and the content hash: %+v", rep.Findings)
	}
}

func TestVerifyHashMismatchOnCoreTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 2)
	mutate(t, path, 1, func(r *Record) { r.Actor.ID = "mallory" })

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeHashMismatch) != 1 {
		t.Fatalf("core tamper must be a hash_mismatch: %+v", rep)
	}
	if !strings.Contains(rep.Findings[1].Message, "chain_hash") {
		t.Fatalf("finding must name the chain hash: %+v", rep.Findings)
	}
}

func TestVerifyChainBreakAndSeqGapOnDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 4)
	lines := readRecords(t, path)
	writeRecords(t, path, [][]byte{lines[0], lines[1], lines[3]}) // drop seq 2

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeChainBreak) != 1 || rep.Count(CodeSeqGap) != 1 || rep.Count(CodeHashMismatch) != 0 {
		t.Fatalf("a removed record is one chain_break plus one seq_gap: %+v %s", rep, codes(rep))
	}
	if rep.Entries != 3 {
		t.Fatalf("entries %d", rep.Entries)
	}
}

func TestVerifyChainBreakOnReorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 3)
	lines := readRecords(t, path)
	writeRecords(t, path, [][]byte{lines[0], lines[2], lines[1]})

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeChainBreak) != 2 || rep.Count(CodeSeqGap) != 2 {
		t.Fatalf("reordering two records breaks both links: %+v %s", rep, codes(rep))
	}
}

func TestVerifySeqGapWithoutHashBreak(t *testing.T) {
	// A gap whose links were re-sealed by someone holding the journal: seq
	// jumps but prev_hash is consistent. Only seq_gap must fire.
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 3)
	reseal(t, path, 2, func(r *Record) { r.Seq = 7 })

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeSeqGap) != 1 || rep.Count(CodeChainBreak) != 0 || rep.Count(CodeHashMismatch) != 0 {
		t.Fatalf("expected exactly one seq_gap: %+v %s", rep, codes(rep))
	}
}

func TestVerifyDoesNotCascadeAfterABreak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 6)
	lines := readRecords(t, path)
	writeRecords(t, path, append(lines[:2], lines[3:]...)) // drop one in the middle

	rep, _ := Verify(path, nil)
	if rep.Count(CodeChainBreak) != 1 || rep.Count(CodeSeqGap) != 1 {
		t.Fatalf("records after the break are intact and must not be reported: %+v %s", rep, codes(rep))
	}
}

func TestVerifyRestartIsNotABreak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 2)
	sealN(t, path, 2) // second process: restart anchored at record 1
	sealN(t, path, 1) // third process

	rep, _ := Verify(path, nil)
	if !rep.OK || rep.Restarts != 3 || rep.Count(CodeRestart) != 3 || rep.Count(CodeChainBreak) != 0 || rep.Count(CodeSeqGap) != 0 {
		t.Fatalf("restarts must be reported separately and keep OK: %+v %s", rep, codes(rep))
	}
}

func TestVerifyRestartWithWrongAnchorIsABreak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 2)
	sealN(t, path, 1)
	// Forge the restart record so that it anchors at genesis instead of the
	// real predecessor: a whole segment is being hidden.
	reseal(t, path, 2, func(r *Record) { r.AnchorHash = GenesisHash; r.PrevHash = GenesisHash })

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeChainBreak) != 1 || rep.Count(CodeRestart) != 2 {
		t.Fatalf("a restart that does not continue the file is a chain_break: %+v %s", rep, codes(rep))
	}
}

func TestVerifyRestartPrevHashMustEqualAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 1)
	sealN(t, path, 1)
	reseal(t, path, 1, func(r *Record) { r.PrevHash = GenesisHash }) // anchor stays correct

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeChainBreak) != 1 {
		t.Fatalf("prev_hash != anchor_hash on a restart is a chain_break: %+v %s", rep, codes(rep))
	}
}

func TestVerifyRestartAllowsAnySeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 2)
	sealN(t, path, 1)
	reseal(t, path, 2, func(r *Record) { r.Seq = 0 })

	rep, _ := Verify(path, nil)
	if !rep.OK || rep.Count(CodeSeqGap) != 0 {
		t.Fatalf("seq may take any value on a restart: %+v %s", rep, codes(rep))
	}
}

func TestVerifySchemaInvalid(t *testing.T) {
	cases := map[string]func(r *Record){
		"wrong schema":      func(r *Record) { r.Schema = "trail.evidence/2" },
		"bad producer kind": func(r *Record) { r.Producer.Kind = "worker" },
		"bad event type":    func(r *Record) { r.EventType = "result" },
		"bad time source":   func(r *Record) { r.Time.Source = "wallclock" },
		"bad actor auth":    func(r *Record) { r.Actor.Auth = "unknown" },
		"bad hash format":   func(r *Record) { r.PrevHash = "deadbeef" },
		"bad time":          func(r *Record) { r.Time.RecordedAt = "2026-11-02 10:00:00" },
		"anchor w/o flag":   func(r *Record) { r.AnchorHash = GenesisHash },
		"empty policy":      func(r *Record) { r.Policy = Policy{Hash: GenesisHash} },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.jsonl")
			sealN(t, path, 2)
			reseal(t, path, 1, fn)
			rep, _ := Verify(path, nil)
			if rep.OK || rep.Count(CodeSchemaInvalid) == 0 {
				t.Fatalf("%s must be schema_invalid: %+v %s", name, rep, codes(rep))
			}
			if rep.Count(CodeHashMismatch) != 0 {
				t.Fatalf("%s: hashes of a schema-invalid record are not judged: %s", name, codes(rep))
			}
		})
	}
}

func TestVerifyMissingMemberIsSchemaInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 1)
	data, _ := os.ReadFile(path)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(bytes.TrimSpace(data), &m)
	delete(m, "policy")
	out, _ := json.Marshal(m)
	os.WriteFile(path, append(out, '\n'), 0o600)

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeSchemaInvalid) != 1 || !strings.Contains(rep.Findings[0].Message, "policy") {
		t.Fatalf("%+v", rep)
	}
}

func TestVerifyInvalidJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 2)
	lines := readRecords(t, path)
	writeRecords(t, path, [][]byte{lines[0], []byte(`{"schema": `), lines[1]})

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeSchemaInvalid) != 1 || rep.Entries != 3 {
		t.Fatalf("%+v %s", rep, codes(rep))
	}
	if rep.Findings[1].Seq != -1 {
		t.Fatalf("unreadable record has no seq: %+v", rep.Findings[1])
	}
}

func TestVerifyLegacyEnvelopeIsSchemaInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	legacy := `{"schema_version":"1","worker_id":"w","seq":1,"prev_hash":"` + GenesisHash + `","event_type":"result","recorded_at":"2026-01-01T00:00:00Z","time_source":"system","payload":{},"content_hash":"` + GenesisHash + `","chain_hash":"` + GenesisHash + `"}`
	os.WriteFile(path, []byte(legacy+"\n"), 0o600)

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeSchemaInvalid) != 1 || !strings.Contains(rep.Findings[0].Message, "pre-release") {
		t.Fatalf("%+v", rep)
	}
}

func TestVerifyStreamIDChangeIsABreak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 2)
	reseal(t, path, 1, func(r *Record) { r.StreamID = "worker-02" })

	rep, _ := Verify(path, nil)
	if rep.OK || rep.Count(CodeChainBreak) != 1 {
		t.Fatalf("%+v %s", rep, codes(rep))
	}
}

func TestVerifyAcceptsForeignMembers(t *testing.T) {
	// A record from another producer may carry members this build does not
	// model; they are outside the core and must not affect verification.
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 1)
	lines := readRecords(t, path)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(lines[0], &m)
	m["context"] = json.RawMessage(`{"host":"edge-7"}`)
	m["future_member"] = json.RawMessage(`true`)
	out, _ := json.Marshal(m)
	writeRecords(t, path, [][]byte{out})

	rep, _ := Verify(path, nil)
	if !rep.OK {
		t.Fatalf("non-core members must not break verification: %+v", rep)
	}
}

func TestFindingString(t *testing.T) {
	f := Finding{Index: 3, Seq: 7, Code: CodeSeqGap, Message: "seq 9, expected 8"}
	if got := f.String(); got != "record #3 (seq 7): seq_gap: seq 9, expected 8" {
		t.Fatalf("%q", got)
	}
	f.Seq = -1
	if got := f.String(); !strings.HasPrefix(got, "record #3: seq_gap") {
		t.Fatalf("%q", got)
	}
}
