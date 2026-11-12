package evidence

import (
	"os"
	"path/filepath"
	"testing"
)

// After a failed journal write the journal may end in a partial line and the
// in-memory chain no longer matches the file. The sealer must not seal
// anything on top of that, even if a later write would succeed (audit E6).
func TestSealerSealsNothingAfterAJournalWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	s, err := NewSealer(Options{ProducerID: "worker-01", ProducerVersion: "test", JournalPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Seal("transfer.job.result", "j-1", map[string]any{"status": "completed"}, nil); err != nil {
		t.Fatal(err)
	}
	_ = s.journal.Close() // the next write fails
	if _, err := s.Seal("transfer.job.result", "j-2", map[string]any{"status": "completed"}, nil); err == nil {
		t.Fatal("want an error when the journal cannot be written")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.journal = f // writable again
	defer s.Close()
	if _, err := s.Seal("transfer.job.result", "j-3", map[string]any{"status": "completed"}, nil); err == nil {
		t.Fatal("the sealer resumed after a failed journal write")
	}
	rep, err := Verify(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Entries != 1 || !rep.OK {
		t.Fatalf("journal must still hold exactly the one record sealed before the failure: %+v", rep)
	}
}
