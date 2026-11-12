package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RainerGewalt/trailtransfer/internal/evidence"
)

// "OK" alone was read as more than it is (audit U1): the output has to say
// what verify did not check, as clearly as what it did.
func TestVerifyNamesWhatItDidNotCheck(t *testing.T) {
	out, code := runCLI(t, "verify", unsignedJournal(t))
	if code != 0 || !strings.HasPrefix(out, "OK: 3 record(s)") {
		t.Fatalf("exit code %d, want 0 and OK; output:\n%s", code, out)
	}
	for _, want := range []string{"not checked:", "signatures: the records are not signed", "after the last record", "file manifest"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestVerifyWithTheKeyListsSignaturesAsChecked(t *testing.T) {
	journal, rightPub, _ := signedJournal(t)
	out, code := runCLI(t, "verify", journal, "--pubkey", rightPub)
	if code != 0 || !strings.Contains(out, "3 signature(s) verified") {
		t.Fatalf("exit code %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "not checked:") || strings.Contains(out, "signatures:") {
		t.Fatalf("with the key, signatures are checked and must not be listed as unchecked; output:\n%s", out)
	}
}

func TestVerifyDoesNotCallAnEmptyJournalOK(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, "verify", empty)
	if code != 1 || !strings.HasPrefix(out, "EMPTY") || strings.HasPrefix(out, "OK") {
		t.Fatalf("exit code %d, want 1 and EMPTY for a journal without records; output:\n%s", code, out)
	}
}

// A journal cut off at its start up to a restart still has an intact chain;
// verify has to say that the earlier segment is missing from the file.
func TestVerifySaysThatThePreviousSegmentIsNotInTheJournal(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal.jsonl")
	for run := 1; run <= 2; run++ {
		s, err := evidence.NewSealer(evidence.Options{ProducerID: "worker-01", ProducerVersion: "test", JournalPath: journal})
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 2; i++ {
			if _, err := s.Seal("transfer.job.result", fmt.Sprintf("job-%d-%d", run, i), map[string]any{"status": "completed"}, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	cut := filepath.Join(dir, "cut.jsonl")
	if err := os.WriteFile(cut, []byte(strings.Join(lines[2:], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, "verify", cut)
	if code != 0 || !strings.HasPrefix(out, "OK: 2 record(s), 1 restart(s)") {
		t.Fatalf("exit code %d, want 0 for an intact chain; output:\n%s", code, out)
	}
	if !strings.Contains(out, "previous segment not in this journal") {
		t.Fatalf("output must say that the previous segment is not in this journal:\n%s", out)
	}
	out, _ = runCLI(t, "verify", journal)
	if strings.Contains(out, "previous segment not in this journal") {
		t.Fatalf("a journal that starts at genesis has no missing segment:\n%s", out)
	}
}
