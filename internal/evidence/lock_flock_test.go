//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package evidence

import (
	"path/filepath"
	"testing"
)

func TestSecondWorkerOnTheSameJournalIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	release, err := LockJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockJournal(path); err == nil {
		t.Fatal("a second lock on the same journal was granted")
	}
	release()
	again, err := LockJournal(path)
	if err != nil {
		t.Fatalf("lock not free after release: %v", err)
	}
	again()
}
