package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/RainerGewalt/trailtransfer/internal/evidence"
)

// Outbox publishes sealed records from the journal in journal order and
// remembers in a cursor file how far the broker has confirmed them. The
// journal is the queue: a record that was sealed while the broker was away,
// or shortly before the worker stopped, is published after the next
// connection. Delivery is at least once; a record that is published twice is
// the same bytes with the same seq and chain_hash.
type Outbox struct {
	journal string
	cursor  string
	mq      MQTT
	topic   func(jobID string) string
	log     *slog.Logger

	mu     sync.Mutex
	offset int64
	notify chan struct{}
}

type outboxCursor struct {
	Offset int64  `json:"offset"`
	Seq    uint64 `json:"seq"`
}

// connectionState is implemented by MQTT clients that know whether they are
// connected. The outbox then waits for a connection instead of handing the
// client the same record again and again.
type connectionState interface{ IsConnected() bool }

// NewOutbox opens the cursor next to the journal. Without a cursor file the
// records already in the journal count as delivered: a worker that is
// updated, or whose cursor was removed, does not publish its history again.
// A cursor behind the end of the journal means the journal was replaced; the
// outbox then starts at the end of the journal it finds.
func NewOutbox(journalPath string, mq MQTT, topic func(jobID string) string, log *slog.Logger) (*Outbox, error) {
	o := &Outbox{journal: journalPath, cursor: journalPath + ".outbox", mq: mq, topic: topic, log: log, notify: make(chan struct{}, 1)}
	size := int64(0)
	if st, err := os.Stat(journalPath); err == nil {
		size = st.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw, err := os.ReadFile(o.cursor)
	switch {
	case errors.Is(err, os.ErrNotExist):
		o.offset = size
		if err := o.save(0); err != nil {
			return nil, fmt.Errorf("outbox cursor: %w", err)
		}
	case err != nil:
		return nil, err
	default:
		var c outboxCursor
		if err := json.Unmarshal(raw, &c); err != nil || c.Offset < 0 || c.Offset > size {
			log.Warn("outbox cursor does not fit the journal; records already in the journal are not published again", "cursor", o.cursor, "journal_bytes", size)
			o.offset = size
			if err := o.save(0); err != nil {
				return nil, fmt.Errorf("outbox cursor: %w", err)
			}
		} else {
			o.offset = c.Offset
		}
	}
	return o, nil
}

func (o *Outbox) save(seq uint64) error {
	raw, _ := json.Marshal(outboxCursor{Offset: o.offset, Seq: seq})
	tmp := o.cursor + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, o.cursor)
}

// Notify tells the outbox that the journal has a new record. It never blocks.
func (o *Outbox) Notify() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

// Pending reports the journal bytes that are not confirmed as published.
func (o *Outbox) Pending() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	st, err := os.Stat(o.journal)
	if err != nil {
		return 0
	}
	return st.Size() - o.offset
}

// Run publishes until ctx ends. A failed or unconfirmed publish is tried
// again; the cursor moves only past confirmed records.
func (o *Outbox) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		if err := o.Drain(); err != nil {
			o.log.Warn("result not published yet, it stays in the journal and is tried again", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-o.notify:
		case <-t.C:
		}
	}
}

// Drain publishes every record after the cursor, in order, and stops at the
// first one the broker does not confirm.
func (o *Outbox) Drain() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cs, ok := o.mq.(connectionState); ok && !cs.IsConnected() {
		if st, err := os.Stat(o.journal); err == nil && st.Size() > o.offset {
			return errors.New("no broker connection")
		}
		return nil
	}
	err := evidence.ScanJournal(o.journal, o.offset, func(e evidence.JournalEntry) error {
		if e.JobID != "" {
			if err := o.mq.Publish(o.topic(e.JobID), 1, false, e.Raw); err != nil {
				return fmt.Errorf("seq %d: %w", e.Seq, err)
			}
		}
		o.offset = e.End
		if err := o.save(e.Seq); err != nil {
			o.log.Warn("outbox cursor not saved; after a restart this record is published again", "seq", e.Seq, "error", err)
		}
		return nil
	})
	if errors.Is(err, evidence.ErrPartialLine) {
		// The sealer is in the middle of a write, or the journal ends in a
		// cut-off line. Either way there is nothing complete to publish.
		return nil
	}
	return err
}
