package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RainerGewalt/trailtransfer/internal/evidence"
)

// The verify tests run the real command line: the test binary re-executes
// itself with TRAILTRANSFER_TEST_MAIN=1 and hands its arguments to main().
// Flag parsing and exit codes are what a user relies on, and the evidence
// package tests cannot see them.
func TestMain(m *testing.M) {
	if os.Getenv("TRAILTRANSFER_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runCLI runs "trailtransfer <args>" and returns its combined output and exit
// code.
func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "TRAILTRANSFER_TEST_MAIN=1")
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

func writeKeyPair(t *testing.T, dir, name string) (keyPath, pubPath string) {
	t.Helper()
	priv, pub, _, err := evidence.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(dir, name+".key")
	pubPath = filepath.Join(dir, name+".pub")
	if err := os.WriteFile(keyPath, priv, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubPath, pub, 0o644); err != nil {
		t.Fatal(err)
	}
	return keyPath, pubPath
}

// signedJournal writes a journal of three records signed with one key and
// returns its path, the matching public key and an unrelated public key.
func signedJournal(t *testing.T) (journal, rightPub, wrongPub string) {
	t.Helper()
	dir := t.TempDir()
	keyPath, rightPub := writeKeyPair(t, dir, "worker")
	_, wrongPub = writeKeyPair(t, dir, "other")
	signer, err := evidence.LoadSigner(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	journal = filepath.Join(dir, "journal.jsonl")
	s, err := evidence.NewSealer(evidence.Options{
		ProducerID:      "worker-01",
		ProducerVersion: "test",
		JournalPath:     journal,
		Signer:          signer,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := s.Seal("transfer.job.result", fmt.Sprintf("job-%d", i), map[string]any{"status": "completed"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return journal, rightPub, wrongPub
}

// The 0.1.0 regression: with --pubkey after the path the key was dropped and
// verify reported OK without checking a single signature.
func TestVerifyChecksSignaturesWhenThePubkeyFollowsThePath(t *testing.T) {
	journal, _, wrongPub := signedJournal(t)
	out, code := runCLI(t, "verify", journal, "--pubkey", wrongPub)
	if code != 1 {
		t.Fatalf("exit code %d, want 1 for a journal signed with another key; output:\n%s", code, out)
	}
	if !strings.HasPrefix(out, "FAIL") || !strings.Contains(out, evidence.CodeSignatureInvalid) {
		t.Fatalf("want FAIL with %s, got:\n%s", evidence.CodeSignatureInvalid, out)
	}
}

func TestVerifyChecksSignaturesWhenThePubkeyPrecedesThePath(t *testing.T) {
	journal, _, wrongPub := signedJournal(t)
	out, code := runCLI(t, "verify", "--pubkey", wrongPub, journal)
	if code != 1 || !strings.Contains(out, evidence.CodeSignatureInvalid) {
		t.Fatalf("exit code %d, want 1 with %s; output:\n%s", code, evidence.CodeSignatureInvalid, out)
	}
}

func TestVerifyAcceptsTheSigningKeyOnEitherSideOfThePath(t *testing.T) {
	journal, rightPub, _ := signedJournal(t)
	for _, args := range [][]string{
		{"verify", "--pubkey", rightPub, journal},
		{"verify", journal, "--pubkey", rightPub},
	} {
		out, code := runCLI(t, args...)
		if code != 0 || !strings.Contains(out, "3 signature(s) verified") {
			t.Fatalf("%v: exit code %d, want 0 with 3 verified signatures; output:\n%s", args, code, out)
		}
	}
}

// Without a key the chain is checked but the signatures are not, and the
// output has to say so instead of reading like a full pass.
func TestVerifyWithoutAKeySaysTheSignaturesWereNotChecked(t *testing.T) {
	journal, _, _ := signedJournal(t)
	out, code := runCLI(t, "verify", journal)
	if code != 0 || !strings.Contains(out, "pass --pubkey to verify signatures") {
		t.Fatalf("exit code %d, want 0 and a note that signatures were not checked; output:\n%s", code, out)
	}
}

func TestVerifyRejectsAStrayArgument(t *testing.T) {
	journal, rightPub, _ := signedJournal(t)
	out, code := runCLI(t, "verify", journal, "--pubkey", rightPub, "extra")
	if code != 2 || !strings.Contains(out, `unexpected argument "extra"`) {
		t.Fatalf("exit code %d, want 2 with a usage error; output:\n%s", code, out)
	}
}

func TestVerifyFailsAnEditedRecord(t *testing.T) {
	journal, _, _ := signedJournal(t)
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if !strings.Contains(lines[1], `"status":"completed"`) {
		t.Fatalf("fixture changed; second record:\n%s", lines[1])
	}
	lines[1] = strings.Replace(lines[1], `"status":"completed"`, `"status":"failed"`, 1)
	edited := filepath.Join(t.TempDir(), "edited.jsonl")
	if err := os.WriteFile(edited, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, "verify", edited)
	if code != 1 || !strings.Contains(out, evidence.CodeHashMismatch) {
		t.Fatalf("exit code %d, want 1 with %s; output:\n%s", code, evidence.CodeHashMismatch, out)
	}
}
