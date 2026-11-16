package evidence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// JournalEntry is one record of a journal as the worker needs it to recover
// its job state and to publish records it has not delivered yet. It is not a
// verification: use Verify for that.
type JournalEntry struct {
	Seq       uint64          `json:"seq"`
	EventType string          `json:"event_type"`
	JobID     string          `json:"job_id"`
	Payload   json.RawMessage `json:"payload"`
	// Raw is the record exactly as it stands in the journal, without the
	// line ending. Offset and End are its byte positions in the file; End
	// is the position after the line ending.
	Raw    []byte `json:"-"`
	Offset int64  `json:"-"`
	End    int64  `json:"-"`
}

// ErrPartialLine reports a last line without a line ending: a write that was
// cut off, for example by a power loss. The journal is not changed.
var ErrPartialLine = errors.New("evidence journal ends in a partial line")

// ScanJournal calls fn for every record from byte position from to the end
// of the journal. A missing journal has no records. A line that is not a
// record, or a last line without a line ending, stops the scan with an error
// that names the byte position.
func ScanJournal(path string, from int64, fn func(JournalEntry) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if from > 0 {
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return err
		}
	}
	r := bufio.NewReaderSize(f, 64*1024)
	pos := from
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(bytes.TrimSpace(line)) > 0 {
				return fmt.Errorf("%w: %s, byte %d", ErrPartialLine, path, pos)
			}
			return nil
		}
		if err != nil {
			return err
		}
		start := pos
		pos += int64(len(line))
		raw := bytes.TrimSpace(line)
		if len(raw) == 0 {
			continue
		}
		e := JournalEntry{Offset: start, End: pos}
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("evidence journal %s, byte %d: not a record: %w", path, start, err)
		}
		e.Raw = append([]byte(nil), raw...)
		if err := fn(e); err != nil {
			return err
		}
	}
}
