package evidence

import (
	"encoding/json"
	"fmt"
)

// Schema is the record format every sealed record declares. It follows the
// Trail Evidence Record v1 specification shared across the Trail projects:
// the major version lives in the name, minor changes are additive only.
const Schema = "trail.evidence/1"

// GenesisHash is the prev_hash of the first record in a stream and the
// anchor_hash of a chain restart whose predecessor is unknown.
const GenesisHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// Producer kinds defined by the specification.
const (
	ProducerTransferWorker = "transfer-worker"
)

// Actor authentication levels defined by the specification.
const (
	ActorAuthBrokerAuthenticated = "broker-authenticated"
	ActorAuthSelfDeclared        = "self-declared"
	ActorAuthOperatorToken       = "operator-token"
	ActorAuthNone                = "none"
)

// Time sources defined by the specification.
const (
	TimeSourceSystem = "system"
)

// PolicyKindTransfer marks a TrailTransfer worker policy.
const PolicyKindTransfer = "transfer-policy"

// Producer identifies the instance that sealed a record.
type Producer struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Time carries the sealing time and, where known, the time the fact happened.
type Time struct {
	RecordedAt string `json:"recorded_at"`
	OccurredAt string `json:"occurred_at,omitempty"`
	ReadAt     string `json:"read_at,omitempty"`
	Source     string `json:"source"`
}

// Actor records who caused a record to exist. TrailTransfer fills it from the
// command's self-declared operator field: MQTT 3.1.1 does not forward the
// publisher's authenticated identity to subscribers, so strong attribution
// stays a broker or governance responsibility.
type Actor struct {
	Auth  string   `json:"auth"`
	ID    string   `json:"id,omitempty"`
	Roles []string `json:"roles,omitempty"`
}

// Policy identifies the ruleset that governed the recorded event.
type Policy struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
	Status  string `json:"status,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// NoPolicy is the value the specification prescribes when no policy applies.
var NoPolicy = Policy{ID: "none", Version: "0", Hash: GenesisHash}

// Signature is an optional Ed25519 signature over the ASCII bytes of the
// chain_hash string. sig is base64url without padding.
type Signature struct {
	Alg   string `json:"alg"`
	KeyID string `json:"key_id"`
	Sig   string `json:"sig"`
}

// Redaction lists payload fields that were masked before hashing.
type Redaction struct {
	Fields []string `json:"fields"`
	Method string   `json:"method"`
}

// Anchor references an external anchoring of the chain (TrailMQ, TSA, WORM).
type Anchor struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Record is one Trail Evidence Record v1. The chain hash commits to the core
// members (see coreKeys); context is informational and never hashed.
type Record struct {
	Schema      string          `json:"schema"`
	Producer    Producer        `json:"producer"`
	StreamID    string          `json:"stream_id"`
	Seq         uint64          `json:"seq"`
	PrevHash    string          `json:"prev_hash"`
	EventType   string          `json:"event_type"`
	Time        Time            `json:"time"`
	Actor       Actor           `json:"actor"`
	Policy      Policy          `json:"policy"`
	Payload     json.RawMessage `json:"payload"`
	ContentHash string          `json:"content_hash"`
	ChainHash   string          `json:"chain_hash"`

	ChainRestart bool   `json:"chain_restart,omitempty"`
	AnchorHash   string `json:"anchor_hash,omitempty"`

	Signature     *Signature      `json:"signature,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	JobID         string          `json:"job_id,omitempty"`
	DecisionID    string          `json:"decision_id,omitempty"`
	Context       json.RawMessage `json:"context,omitempty"`
	Redaction     *Redaction      `json:"redaction,omitempty"`
	Anchor        *Anchor         `json:"anchor,omitempty"`
}

// coreKeys are the members the chain hash covers, exactly as the
// specification lists them. chain_restart and anchor_hash are included only
// when present in the record.
var coreKeys = []string{
	"schema", "producer", "stream_id", "seq", "prev_hash", "event_type",
	"time", "actor", "policy", "content_hash", "chain_restart", "anchor_hash",
}

// requiredKeys must be present in every record.
var requiredKeys = []string{
	"schema", "producer", "stream_id", "seq", "prev_hash", "event_type",
	"time", "actor", "policy", "payload", "content_hash", "chain_hash",
}

// rawRecord is a record as it appears on the wire, member by member. Working
// on the raw members keeps seal and verify byte-identical and lets verify
// handle records from any producer, including members this build does not
// model.
type rawRecord map[string]json.RawMessage

// CoreJCS returns the canonical bytes of the record core that chain_hash
// commits to.
func CoreJCS(raw map[string]json.RawMessage) ([]byte, error) {
	core := make(map[string]json.RawMessage, len(coreKeys))
	for _, k := range coreKeys {
		if v, ok := raw[k]; ok {
			core[k] = v
		}
	}
	for _, k := range coreKeys[:10] {
		if _, ok := core[k]; !ok {
			return nil, fmt.Errorf("core member %q missing", k)
		}
	}
	b, err := json.Marshal(core)
	if err != nil {
		return nil, err
	}
	return canonicalizeJSON(b)
}

// ChainHashOf computes the chain hash of a raw record from its core members.
func ChainHashOf(raw map[string]json.RawMessage) (string, error) {
	b, err := CoreJCS(raw)
	if err != nil {
		return "", err
	}
	return sum(b), nil
}

// ContentHashOf computes sha256 over JCS(payload). The payload must be valid
// JSON.
func ContentHashOf(payload []byte) (string, error) {
	b, err := canonicalizeJSON(payload)
	if err != nil {
		return "", fmt.Errorf("payload is not valid JSON: %w", err)
	}
	return sum(b), nil
}

// raw serialises the record and splits it into its top-level members.
func (r *Record) raw() (rawRecord, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var m rawRecord
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// computeChainHash returns the chain hash over this record's core.
func (r *Record) computeChainHash() (string, error) {
	m, err := r.raw()
	if err != nil {
		return "", err
	}
	return ChainHashOf(m)
}
