package events

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RainerGewalt/trailtransfer/internal/config"
	"github.com/RainerGewalt/trailtransfer/internal/evidence"
)

// broker is a fake MQTT client that can be away.
type broker struct {
	mu        sync.Mutex
	connected bool
	failNext  int // publishes that fail although connected
	got       []string
}

func (b *broker) IsConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected
}

func (b *broker) Publish(topic string, _ byte, _ bool, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.connected {
		return errors.New("not connected")
	}
	if b.failNext > 0 {
		b.failNext--
		return errors.New("not confirmed")
	}
	var r struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(payload, &r)
	b.got = append(b.got, topic+"|"+r.JobID)
	return nil
}

func (b *broker) set(connected bool) {
	b.mu.Lock()
	b.connected = connected
	b.mu.Unlock()
}

func (b *broker) delivered() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.got...)
}

var (
	testCfg = &config.Config{WorkerID: "w", TopicPrefix: "tt"}
	quiet   = slog.New(slog.NewTextHandler(io.Discard, nil))
)

func openSealer(t *testing.T, path string) *evidence.Sealer {
	t.Helper()
	s, err := evidence.NewSealer(evidence.Options{ProducerID: "w", JournalPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func openOutbox(t *testing.T, path string, b *broker) *Outbox {
	t.Helper()
	o, err := NewOutbox(path, fileSize(path), b, testCfg.JobResultTopic, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// fileSize stands in for Sealer.DurableSize where a test has no sealer for
// the journal: everything in the file counts as synced.
func fileSize(path string) func() int64 {
	return func() int64 {
		st, err := os.Stat(path)
		if err != nil {
			return 0
		}
		return st.Size()
	}
}

func seal(t *testing.T, p *Publisher, jobID string) {
	t.Helper()
	if err := p.Result(ResultEvent{JobID: jobID, Status: "completed"}, nil); err != nil {
		t.Fatal(err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Acceptance 3 of issue #1: a result that is sealed while the broker is away
// is published after the connection is back, in journal order, and the
// journal itself is complete throughout.
func TestResultSealedDuringOutageIsPublishedAfterReconnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	b := &broker{connected: true}
	p := NewPublisher(b, testCfg, "test", openSealer(t, path))
	o := openOutbox(t, path, b)
	p.SetOutbox(o)

	seal(t, p, "a")
	if err := o.Drain(); err != nil {
		t.Fatal(err)
	}
	b.set(false)
	seal(t, p, "b")
	seal(t, p, "c")
	if err := o.Drain(); err == nil {
		t.Fatal("Drain must report that records are waiting for the broker")
	}
	if got := b.delivered(); !equal(got, []string{"tt/w/jobs/a/result|a"}) {
		t.Fatalf("published during the outage: %v", got)
	}
	rep, err := evidence.Verify(path, nil)
	if err != nil || !rep.OK || rep.Entries != 3 {
		t.Fatalf("journal during the outage: %+v %v", rep, err)
	}
	if o.Pending() == 0 {
		t.Fatal("Pending must count the records that wait")
	}

	b.set(true)
	if err := o.Drain(); err != nil {
		t.Fatal(err)
	}
	want := []string{"tt/w/jobs/a/result|a", "tt/w/jobs/b/result|b", "tt/w/jobs/c/result|c"}
	if got := b.delivered(); !equal(got, want) {
		t.Fatalf("after reconnect: %v, want %v", got, want)
	}
	if o.Pending() != 0 {
		t.Fatalf("still pending: %d", o.Pending())
	}
}

// The case the in-process queue of the MQTT client cannot cover: the worker
// stops during the outage. The next process publishes what was left.
func TestOutboxContinuesAfterRestartDuringOutage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	b := &broker{connected: true}
	s1 := openSealer(t, path)
	p1 := NewPublisher(b, testCfg, "test", s1)
	o1 := openOutbox(t, path, b)
	p1.SetOutbox(o1)
	seal(t, p1, "a")
	_ = o1.Drain()
	b.set(false)
	seal(t, p1, "b")
	_ = o1.Drain()
	s1.Close() // the process ends with "b" unpublished

	b2 := &broker{connected: true}
	o2 := openOutbox(t, path, b2)
	if err := o2.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := b2.delivered(); !equal(got, []string{"tt/w/jobs/b/result|b"}) {
		t.Fatalf("after the restart: %v, want only b", got)
	}
}

// An unconfirmed publish does not move the cursor: the record is sent again
// and nothing after it overtakes it.
func TestUnconfirmedPublishIsRetriedInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	b := &broker{connected: true, failNext: 1}
	p := NewPublisher(b, testCfg, "test", openSealer(t, path))
	o := openOutbox(t, path, b)
	p.SetOutbox(o)
	seal(t, p, "a")
	seal(t, p, "b")
	if err := o.Drain(); err == nil {
		t.Fatal("want the publish error")
	}
	if got := b.delivered(); len(got) != 0 {
		t.Fatalf("b overtook a: %v", got)
	}
	if err := o.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := b.delivered(); !equal(got, []string{"tt/w/jobs/a/result|a", "tt/w/jobs/b/result|b"}) {
		t.Fatalf("order after retry: %v", got)
	}
}

// A worker that gets an outbox for the first time, or whose cursor is gone
// or does not fit, must not publish its whole history again.
func TestExistingJournalWithoutCursorIsNotRepublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	b := &broker{connected: true}
	s := openSealer(t, path)
	p := NewPublisher(b, testCfg, "test", s) // no outbox: published directly, as before
	seal(t, p, "old-1")
	seal(t, p, "old-2")
	before := len(b.delivered())

	for _, cursor := range []string{"", "not json", `{"offset":999999,"seq":7}`} {
		_ = os.Remove(path + ".outbox")
		if cursor != "" {
			if err := os.WriteFile(path+".outbox", []byte(cursor), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		o := openOutbox(t, path, b)
		if err := o.Drain(); err != nil {
			t.Fatal(err)
		}
		if got := len(b.delivered()); got != before {
			t.Fatalf("cursor %q: history was published again (%d -> %d)", cursor, before, got)
		}
	}

	o := openOutbox(t, path, b)
	p.SetOutbox(o)
	seal(t, p, "new")
	if err := o.Drain(); err != nil {
		t.Fatal(err)
	}
	got := b.delivered()
	if len(got) != before+1 || got[len(got)-1] != "tt/w/jobs/new/result|new" {
		t.Fatalf("new record after the cursor: %v", got)
	}
}

// The outbox publishes nothing beyond what the sealer has synced: a line that
// is in the file but not confirmed (a write in progress, or a sync that
// failed and was reported to the job as "not sealed") stays unpublished.
func TestOutboxPublishesNothingBeyondTheSyncedJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	b := &broker{connected: true}
	s := openSealer(t, path)
	p := NewPublisher(b, testCfg, "test", s)
	limit := int64(0)
	o, err := NewOutbox(path, func() int64 { return limit }, b, testCfg.JobResultTopic, quiet)
	if err != nil {
		t.Fatal(err)
	}
	p.SetOutbox(o)
	seal(t, p, "a")
	afterA := s.DurableSize()
	seal(t, p, "b")
	if s.DurableSize() <= afterA {
		t.Fatal("DurableSize did not grow with a sealed record")
	}

	if err := o.Drain(); err != nil || len(b.delivered()) != 0 {
		t.Fatalf("published beyond the synced journal: %v %v", b.delivered(), err)
	}
	limit = afterA
	if err := o.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := b.delivered(); !equal(got, []string{"tt/w/jobs/a/result|a"}) {
		t.Fatalf("with a synced, b not: %v", got)
	}
	limit = s.DurableSize()
	if err := o.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := b.delivered(); len(got) != 2 {
		t.Fatalf("after b was synced: %v", got)
	}
}

// A cursor that points into the middle of a line (the journal was replaced
// or edited while the worker was stopped) must not stall the outbox.
func TestCursorInsideALineDoesNotStallTheOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	b := &broker{connected: true}
	s := openSealer(t, path)
	p := NewPublisher(b, testCfg, "test", s)
	seal(t, p, "old")
	before := len(b.delivered())
	if err := os.WriteFile(path+".outbox", []byte(`{"offset":17,"seq":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	o := openOutbox(t, path, b)
	p.SetOutbox(o)
	seal(t, p, "new")
	if err := o.Drain(); err != nil {
		t.Fatalf("outbox stalled: %v", err)
	}
	got := b.delivered()
	if len(got) != before+1 || got[len(got)-1] != "tt/w/jobs/new/result|new" {
		t.Fatalf("after a cursor inside a line: %v", got)
	}
}

// Without a journal there is no outbox. A result that ends during a broker
// outage is still published once the broker is back, as long as the worker
// runs.
func TestResultWithoutJournalIsRetriedAfterAnOutage(t *testing.T) {
	old := resultRetryInterval
	resultRetryInterval = 5 * time.Millisecond
	defer func() { resultRetryInterval = old }()

	b := &broker{}
	s, err := evidence.NewSealer(evidence.Options{ProducerID: "w"})
	if err != nil {
		t.Fatal(err)
	}
	p := NewPublisher(b, testCfg, "test", s)
	seal(t, p, "a")
	if len(b.delivered()) != 0 {
		t.Fatal("published without a broker")
	}
	b.set(true)
	deadline := time.Now().Add(2 * time.Second)
	for len(b.delivered()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the result was not published after the broker came back")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.delivered(); !equal(got, []string{"tt/w/jobs/a/result|a"}) {
		t.Fatalf("published: %v", got)
	}
}
