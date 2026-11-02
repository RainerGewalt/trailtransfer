package evidence

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var updateVectors = flag.Bool("update", false, "rewrite testdata/vectors.json from this implementation")

// vector is one canonicalisation test vector in the shape the Trail Evidence
// Record v1 specification asks reference implementations to publish.
type vector struct {
	Name                string          `json:"name"`
	Record              json.RawMessage `json:"record"`
	ExpectedContentHash string          `json:"expected_content_hash"`
	ExpectedChainHash   string          `json:"expected_chain_hash"`
	ExpectedCoreJCS     string          `json:"expected_core_jcs"`
}

// buildVectors seals a fixed set of records with a frozen clock so that the
// output is reproducible byte for byte.
func buildVectors(t *testing.T) []vector {
	t.Helper()
	s, err := NewSealer(Options{
		ProducerID:      "worker-01",
		ProducerVersion: "0.1.0",
		Policy:          Policy{ID: "worker-policy.yaml", Version: "1", Hash: "sha256:" + hexN("ab", 32), Kind: PolicyKindTransfer},
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 2, 10, 0, 12, 500_000_000, time.UTC)
	s.now = func() time.Time { return clock }

	type step struct {
		name      string
		eventType string
		jobID     string
		payload   any
		actor     *Actor
	}
	steps := []step{
		{"genesis-restart", "transfer.job.result", "", map[string]any{"b": 1, "a": "x"}, &Actor{Auth: ActorAuthNone}},
		{"second-record-unicode", "transfer.job.result", "job-002", map[string]any{"ü": "ß", "z": []any{1, "two", nil, true}, "n": int64(9007199254740993)}, &Actor{Auth: ActorAuthSelfDeclared, ID: "alice"}},
		{"rejected", "transfer.job.rejected", "job-003", map[string]any{"status": "rejected", "reason": "policy_violation", "errors": []string{}}, &Actor{Auth: ActorAuthSelfDeclared, ID: "bob", Roles: []string{"operator"}}},
	}
	var out []vector
	for _, st := range steps {
		raw, err := s.Seal(st.eventType, st.jobID, st.payload, st.actor)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		core, err := CoreJCS(m)
		if err != nil {
			t.Fatal(err)
		}
		var ch, co string
		_ = json.Unmarshal(m["content_hash"], &co)
		_ = json.Unmarshal(m["chain_hash"], &ch)
		out = append(out, vector{Name: st.name, Record: raw, ExpectedContentHash: co, ExpectedChainHash: ch, ExpectedCoreJCS: string(core)})
		clock = clock.Add(time.Second)
	}
	return out
}

func TestVectorsMatchCommittedFile(t *testing.T) {
	path := filepath.Join("testdata", "vectors.json")
	got := buildVectors(t)

	if *updateVectors {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to generate)", err)
	}
	var want []vector
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("vector count %d, committed %d", len(got), len(want))
	}
	for i := range want {
		if want[i].ExpectedContentHash != got[i].ExpectedContentHash ||
			want[i].ExpectedChainHash != got[i].ExpectedChainHash ||
			want[i].ExpectedCoreJCS != got[i].ExpectedCoreJCS {
			t.Fatalf("vector %q drifted from the committed file:\n got  %s %s\n want %s %s", want[i].Name, got[i].ExpectedChainHash, got[i].ExpectedCoreJCS, want[i].ExpectedChainHash, want[i].ExpectedCoreJCS)
		}
		// The committed record must itself verify: hashes recomputed from the
		// record's own members equal the expected values.
		var m map[string]json.RawMessage
		if err := json.Unmarshal(want[i].Record, &m); err != nil {
			t.Fatal(err)
		}
		if ch, _ := ChainHashOf(m); ch != want[i].ExpectedChainHash {
			t.Fatalf("vector %q: chain_hash recomputed from record is %s", want[i].Name, ch)
		}
		if co, _ := ContentHashOf(m["payload"]); co != want[i].ExpectedContentHash {
			t.Fatalf("vector %q: content_hash recomputed from record is %s", want[i].Name, co)
		}
	}
	// Vector 1 of the specification: payload {"b":1,"a":"x"} canonicalises
	// to {"a":"x","b":1}.
	if want[0].ExpectedContentHash != sum([]byte(`{"a":"x","b":1}`)) {
		t.Fatal("vector 1 content_hash is not sha256 of the JCS payload")
	}
}

func hexN(pair string, n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += pair
	}
	return s
}
