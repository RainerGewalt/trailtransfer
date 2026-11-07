package evidence

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// Finding codes reported by Verify. restart is informational; every other
// code marks the stream as not OK.
const (
	CodeHashMismatch     = "hash_mismatch"
	CodeChainBreak       = "chain_break"
	CodeSeqGap           = "seq_gap"
	CodeRestart          = "restart"
	CodeSignatureInvalid = "signature_invalid"
	CodeSchemaInvalid    = "schema_invalid"
)

// Finding is one observation about one record.
type Finding struct {
	Index   int    // 0-based line index of the record in the journal
	Seq     int64  // seq of the record, -1 when unreadable
	Code    string // one of the Code* constants
	Message string
}

func (f Finding) String() string {
	if f.Seq < 0 {
		return fmt.Sprintf("record #%d: %s: %s", f.Index, f.Code, f.Message)
	}
	return fmt.Sprintf("record #%d (seq %d): %s: %s", f.Index, f.Seq, f.Code, f.Message)
}

// VerifyReport is the outcome of checking an evidence journal.
type VerifyReport struct {
	Entries           int  // records seen
	OK                bool // no finding other than restarts
	Signed            bool // at least one record carried a signature
	SignaturesChecked int  // records whose signature was verified with pub
	Unsigned          int  // records without a signature, counted only when pub is given
	Restarts          int  // records with chain_restart=true
	Findings          []Finding
}

// Count returns the number of findings with the given code.
func (r *VerifyReport) Count(code string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Code == code {
			n++
		}
	}
	return n
}

var hashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var eventTypePattern = regexp.MustCompile(`^[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+$`)

var producerKinds = map[string]bool{"broker": true, "transfer-worker": true, "source-bridge": true, "ml-pipeline": true, "persistence": true, "other": true}
var timeSources = map[string]bool{"system": true, "ntp": true, "tsa": true, "seq-reconstructed": true, "unknown": true}
var actorAuths = map[string]bool{"broker-authenticated": true, "self-declared": true, "operator-token": true, "none": true}

// Verify independently re-checks an evidence journal following the Trail
// Evidence Record v1 algorithm: for each record in file order it checks the
// schema, recomputes content_hash and chain_hash, checks prev_hash against the
// previous chain_hash (or anchor_hash on a restart), checks seq continuity,
// and, when pub is given, verifies the signature. It needs nothing but the
// file.
//
// Without pub, unsigned records are fine and present signatures are not
// checked. With pub, every record must carry a signature that verifies with
// it: a missing signature is signature_invalid with the message "record is not
// signed", the same code and message the trail-evidence verifiers use with
// --require-signatures, and it is counted in Unsigned so that the report can
// tell missing from invalid signatures.
//
// Every record is checked; one damaged record does not hide later ones. After
// a record the walk continues from that record's own chain_hash and seq, so a
// single break produces a single finding.
//
// An unsigned chain detects modification, deletion and reordering. An actor
// who can rewrite the whole suffix of the journal can still forge a consistent
// chain; verifying with pub closes that gap, because the forger has no key to
// sign it and cannot leave the signatures out. Records cut off at the end of
// the journal need an anchor of the latest chain_hash outside the journal.
func Verify(journalPath string, pub ed25519.PublicKey) (VerifyReport, error) {
	f, err := os.Open(journalPath)
	if err != nil {
		return VerifyReport{}, err
	}
	defer f.Close()
	return VerifyReader(f, pub)
}

// VerifyReader verifies JSON Lines records read from r.
func VerifyReader(r io.Reader, pub ed25519.PublicKey) (VerifyReport, error) {
	rep := VerifyReport{OK: true}
	prev := GenesisHash
	var expectedSeq uint64
	streamID := ""

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ln := bytes.TrimSpace(sc.Bytes())
		if len(ln) == 0 {
			continue
		}
		idx := rep.Entries
		rep.Entries++

		var raw rawRecord
		if err := json.Unmarshal(ln, &raw); err != nil {
			rep.add(idx, -1, CodeSchemaInvalid, "invalid JSON: "+err.Error())
			continue
		}
		var rec Record
		if err := json.Unmarshal(ln, &rec); err != nil {
			rep.add(idx, -1, CodeSchemaInvalid, "record does not match trail.evidence/1 types: "+err.Error())
			continue
		}
		seq := int64(rec.Seq)

		if problems := schemaProblems(raw, &rec); len(problems) > 0 {
			for _, p := range problems {
				rep.add(idx, seq, CodeSchemaInvalid, p)
			}
			// Without a sound core the hashes cannot be judged. Re-sync on
			// whatever this record claims so the next one is judged on its own.
			if rec.ChainHash != "" {
				prev = rec.ChainHash
			}
			expectedSeq = rec.Seq + 1
			continue
		}

		if got, err := ContentHashOf(rec.Payload); err != nil {
			rep.add(idx, seq, CodeSchemaInvalid, err.Error())
		} else if got != rec.ContentHash {
			rep.add(idx, seq, CodeHashMismatch, "content_hash does not match payload")
		}
		if want, err := ChainHashOf(raw); err != nil {
			rep.add(idx, seq, CodeSchemaInvalid, err.Error())
		} else if want != rec.ChainHash {
			rep.add(idx, seq, CodeHashMismatch, "chain_hash does not match record core")
		}

		if streamID == "" {
			streamID = rec.StreamID
		} else if rec.StreamID != streamID {
			rep.add(idx, seq, CodeChainBreak, fmt.Sprintf("stream_id changed from %q to %q", streamID, rec.StreamID))
		}

		if rec.ChainRestart {
			rep.Restarts++
			rep.Findings = append(rep.Findings, Finding{Index: idx, Seq: seq, Code: CodeRestart,
				Message: fmt.Sprintf("chain restart anchored at %s", shortHash(rec.AnchorHash))})
			switch {
			case rec.PrevHash != rec.AnchorHash:
				rep.add(idx, seq, CodeChainBreak, "prev_hash differs from anchor_hash on a restart")
			case idx > 0 && rec.AnchorHash != prev:
				rep.add(idx, seq, CodeChainBreak, fmt.Sprintf("anchor_hash %s does not continue the previous record %s", shortHash(rec.AnchorHash), shortHash(prev)))
			}
		} else {
			if rec.PrevHash != prev {
				rep.add(idx, seq, CodeChainBreak, fmt.Sprintf("prev_hash %s, expected %s (record removed, inserted or reordered)", shortHash(rec.PrevHash), shortHash(prev)))
			}
			if idx > 0 && rec.Seq != expectedSeq {
				rep.add(idx, seq, CodeSeqGap, fmt.Sprintf("seq %d, expected %d", rec.Seq, expectedSeq))
			}
		}

		switch {
		case rec.Signature != nil:
			rep.Signed = true
			if pub != nil {
				if err := VerifySignature(pub, rec.ChainHash, rec.Signature); err != nil {
					rep.add(idx, seq, CodeSignatureInvalid, err.Error())
				} else {
					rep.SignaturesChecked++
				}
			}
		case pub != nil:
			// Asked to check signatures, an unsigned record cannot pass: a
			// journal rewritten without signatures would otherwise verify
			// with the operator's key.
			rep.Unsigned++
			rep.add(idx, seq, CodeSignatureInvalid, "record is not signed")
		}

		prev = rec.ChainHash
		expectedSeq = rec.Seq + 1
	}
	if err := sc.Err(); err != nil {
		return rep, err
	}
	return rep, nil
}

// schemaProblems checks the structural rules of trail.evidence/1 that do not
// need the chain state.
func schemaProblems(raw rawRecord, rec *Record) []string {
	var out []string
	if _, legacy := raw["schema_version"]; legacy && raw["schema"] == nil {
		return []string{"pre-release TrailTransfer envelope (schema_version); not a trail.evidence/1 record, see docs/MIGRATION.md"}
	}
	for _, k := range requiredKeys {
		if _, ok := raw[k]; !ok {
			out = append(out, "missing member "+k)
		}
	}
	if len(out) > 0 {
		return out
	}
	if rec.Schema != Schema {
		out = append(out, fmt.Sprintf("schema %q, expected %q", rec.Schema, Schema))
	}
	if !producerKinds[rec.Producer.Kind] {
		out = append(out, fmt.Sprintf("producer.kind %q is not a known kind", rec.Producer.Kind))
	}
	if rec.Producer.ID == "" {
		out = append(out, "producer.id is empty")
	}
	if rec.StreamID == "" {
		out = append(out, "stream_id is empty")
	}
	for name, h := range map[string]string{"prev_hash": rec.PrevHash, "content_hash": rec.ContentHash, "chain_hash": rec.ChainHash, "policy.hash": rec.Policy.Hash} {
		if !hashPattern.MatchString(h) {
			out = append(out, name+" is not sha256:<64 hex>")
		}
	}
	if !eventTypePattern.MatchString(rec.EventType) {
		out = append(out, fmt.Sprintf("event_type %q is not <domain>.<noun>.<verb>", rec.EventType))
	}
	if _, err := time.Parse(time.RFC3339Nano, rec.Time.RecordedAt); err != nil || !strings.HasSuffix(rec.Time.RecordedAt, "Z") {
		out = append(out, "time.recorded_at is not RFC 3339 UTC")
	}
	if !timeSources[rec.Time.Source] {
		out = append(out, fmt.Sprintf("time.source %q is not a known source", rec.Time.Source))
	}
	if !actorAuths[rec.Actor.Auth] {
		out = append(out, fmt.Sprintf("actor.auth %q is not a known level", rec.Actor.Auth))
	}
	if rec.Policy.ID == "" || rec.Policy.Version == "" {
		out = append(out, "policy.id and policy.version are required")
	}
	if rec.ChainRestart && !hashPattern.MatchString(rec.AnchorHash) {
		out = append(out, "chain_restart without a valid anchor_hash")
	}
	if !rec.ChainRestart && rec.AnchorHash != "" {
		out = append(out, "anchor_hash present without chain_restart")
	}
	if rec.Signature != nil {
		if rec.Signature.Alg != "ed25519" || rec.Signature.KeyID == "" || rec.Signature.Sig == "" {
			out = append(out, "signature must carry alg ed25519, key_id and sig")
		}
	}
	return out
}

func (r *VerifyReport) add(index int, seq int64, code, msg string) {
	r.OK = false
	r.Findings = append(r.Findings, Finding{Index: index, Seq: seq, Code: code, Message: msg})
}

func shortHash(h string) string {
	if len(h) > 19 {
		return h[:19] + "…"
	}
	return h
}
