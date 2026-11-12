package evidence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Options configure a Sealer.
type Options struct {
	// ProducerID identifies the worker instance. It becomes producer.id and
	// stream_id of every record.
	ProducerID string
	// ProducerVersion is the build version reported in producer.version.
	ProducerVersion string
	// Policy is stamped into every record. Zero value means NoPolicy.
	Policy Policy
	// JournalPath is the append-only JSONL journal. Empty keeps the chain in
	// memory only; it then restarts from genesis with every process.
	JournalPath string
	// Signer optionally signs every record.
	Signer *Signer
}

// Sealer assigns each record a place in the worker's hash chain and, when a
// journal path is configured, appends it to an fsync'd append-only file.
//
// Every process start is a chain restart as defined by the specification: the
// first record a Sealer seals carries chain_restart=true and anchor_hash set to
// the last chain_hash found in the journal, or the genesis hash when the
// journal is empty or absent. Sequence numbers continue after the recovered
// record so a gap is still visible to a reader who ignores restarts.
type Sealer struct {
	producer   Producer
	policy     Policy
	timeSource string

	mu             sync.Mutex
	nextSeq        uint64
	lastHash       string
	restartPending bool
	journal        *os.File
	signer         *Signer
	now            func() time.Time

	// failed is the first journal write error. After it the journal may end
	// in a partial line and no longer match the in-memory chain, so every
	// later Seal fails with it until the process restarts.
	failed error
}

// NewSealer builds a sealer. If a journal path is set the journal is opened
// (created if needed) and chain state is recovered from its last record that
// carries a chain_hash.
func NewSealer(opts Options) (*Sealer, error) {
	if opts.ProducerID == "" {
		return nil, errors.New("evidence: producer id is required")
	}
	pol := opts.Policy
	if pol == (Policy{}) {
		pol = NoPolicy
	}
	s := &Sealer{
		producer:       Producer{Kind: ProducerTransferWorker, ID: opts.ProducerID, Version: opts.ProducerVersion},
		policy:         pol,
		timeSource:     TimeSourceSystem,
		lastHash:       GenesisHash,
		restartPending: true,
		signer:         opts.Signer,
		now:            time.Now,
	}
	if opts.JournalPath == "" {
		return s, nil
	}
	if err := s.recover(opts.JournalPath); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(opts.JournalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	s.journal = f
	return s, nil
}

// recover reads the journal and takes the last record's seq and chain_hash as
// the anchor for this process. A journal that cannot be read for any reason
// other than absence is an error: sealing blindly on top of it would hide the
// damage.
func (s *Sealer) recover(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ln := bytes.TrimSpace(sc.Bytes())
		if len(ln) == 0 {
			continue
		}
		var tail struct {
			Seq       uint64 `json:"seq"`
			ChainHash string `json:"chain_hash"`
		}
		if err := json.Unmarshal(ln, &tail); err != nil {
			return fmt.Errorf("evidence journal %q: unreadable line: %w", path, err)
		}
		if tail.ChainHash == "" {
			return fmt.Errorf("evidence journal %q: line without chain_hash", path)
		}
		s.nextSeq = tail.Seq + 1
		s.lastHash = tail.ChainHash
	}
	return sc.Err()
}

// SetSigner enables Ed25519 signing of every sealed record.
func (s *Sealer) SetSigner(signer *Signer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signer = signer
}

// Signed reports whether records are being signed.
func (s *Sealer) Signed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signer != nil
}

// JournalEnabled reports whether records are persisted durably.
func (s *Sealer) JournalEnabled() bool { return s.journal != nil }

// Err returns the journal write error that stopped the sealer, or nil.
func (s *Sealer) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Seal wraps payload in a record, links it into the chain, appends it to the
// journal (if enabled, fsync'd) and returns the marshalled record to publish.
// actor may be nil, which records auth "none". jobID is optional. Once a
// journal write has failed, Seal fails for the rest of the process.
func (s *Sealer) Seal(eventType, jobID string, payload any, actor *Actor) (json.RawMessage, error) {
	pb, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	contentHash, err := ContentHashOf(pb)
	if err != nil {
		return nil, err
	}
	act := Actor{Auth: ActorAuthNone}
	if actor != nil {
		act = *actor
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return nil, fmt.Errorf("evidence journal unavailable since an earlier write failed: %w", s.failed)
	}

	rec := Record{
		Schema:      Schema,
		Producer:    s.producer,
		StreamID:    s.producer.ID,
		Seq:         s.nextSeq,
		PrevHash:    s.lastHash,
		EventType:   eventType,
		Time:        Time{RecordedAt: s.now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"), Source: s.timeSource},
		Actor:       act,
		Policy:      s.policy,
		Payload:     json.RawMessage(pb),
		ContentHash: contentHash,
		JobID:       jobID,
	}
	if s.restartPending {
		rec.ChainRestart = true
		rec.AnchorHash = s.lastHash
	}
	ch, err := rec.computeChainHash()
	if err != nil {
		return nil, err
	}
	rec.ChainHash = ch

	if s.signer != nil {
		rec.Signature = s.signer.sign(rec.ChainHash)
	}

	out, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if s.journal != nil {
		if _, err := s.journal.Write(append(out, '\n')); err != nil {
			s.failed = fmt.Errorf("write %s: %w", s.journal.Name(), err)
			return nil, s.failed
		}
		if err := s.journal.Sync(); err != nil {
			s.failed = fmt.Errorf("sync %s: %w", s.journal.Name(), err)
			return nil, s.failed
		}
	}
	s.nextSeq = rec.Seq + 1
	s.lastHash = rec.ChainHash
	s.restartPending = false
	return out, nil
}

// Close closes the journal file, if any.
func (s *Sealer) Close() error {
	if s.journal != nil {
		return s.journal.Close()
	}
	return nil
}
