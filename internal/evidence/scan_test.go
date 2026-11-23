package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScanJournalReportsPositionsAndStopsAtPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	if err := ScanJournal(path, 0, func(JournalEntry) error { t.Fatal("no file, no record"); return nil }); err != nil {
		t.Fatalf("a missing journal has no records: %v", err)
	}
	s, err := NewSealer(Options{ProducerID: "w", JournalPath: path})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.Seal("transfer.job.result", id, map[string]any{"status": "completed"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	var all []JournalEntry
	if err := ScanJournal(path, 0, func(e JournalEntry) error { all = append(all, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Offset != 0 || all[1].Offset != all[0].End || all[2].JobID != "c" || all[2].Seq != 2 {
		t.Fatalf("entries: %+v", all)
	}
	st, _ := os.Stat(path)
	if all[2].End != st.Size() {
		t.Fatalf("End of the last record %d, file size %d", all[2].End, st.Size())
	}

	var tail []string
	if err := ScanJournal(path, all[0].End, func(e JournalEntry) error { tail = append(tail, e.JobID); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 || tail[0] != "b" {
		t.Fatalf("scan from an offset: %v", tail)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"seq":3,"cha`)
	f.Close()
	n := 0
	err = ScanJournal(path, 0, func(JournalEntry) error { n++; return nil })
	if !errors.Is(err, ErrPartialLine) || n != 3 {
		t.Fatalf("partial last line: err=%v, records=%d", err, n)
	}

	if err := os.WriteFile(path, []byte("{\"seq\":0}\nnot a record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ScanJournal(path, 0, func(JournalEntry) error { return nil }); err == nil || errors.Is(err, ErrPartialLine) {
		t.Fatalf("a complete line that is not a record must be an error of its own: %v", err)
	}
}
