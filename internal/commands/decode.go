package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// MaxCommandBytes bounds the size of one command payload. It keeps every
// message the worker publishes in answer to a command small.
const MaxCommandBytes = 64 * 1024

// MaxJobIDLength is the longest accepted job_id and target_job_id.
const MaxJobIDLength = 128

// jobIDPattern is the character set of a job_id: a job_id becomes one level of
// the worker's MQTT topics (…/jobs/{job_id}/status), so it must never contain
// '/', '+', '#', NUL, other control characters or anything else a broker may
// refuse in a topic name. A broker disconnects a client that publishes to an
// invalid topic.
var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ValidJobID reports whether id can be used as a job_id or target_job_id:
// 1 to 128 characters from A-Z, a-z, 0-9, '.', '_' and '-'.
func ValidJobID(id string) bool { return jobIDPattern.MatchString(id) }

const jobIDRule = "1 to 128 characters from A-Z, a-z, 0-9, '.', '_' and '-'"

// Decode parses a command payload with strict schema enforcement: unknown
// fields are rejected, exactly one JSON value is required, job_id must be
// present and a safe topic level, and the action must be known.
func Decode(payload []byte) (*Command, error) {
	if len(payload) > MaxCommandBytes {
		return nil, fmt.Errorf("schema: command larger than %d bytes", MaxCommandBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()

	var c Command
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	if dec.More() {
		return nil, errors.New("schema: unexpected trailing data after command")
	}
	if c.JobID == "" {
		return nil, errors.New("schema: job_id is required")
	}
	if !ValidJobID(c.JobID) {
		return nil, errors.New("schema: job_id must be " + jobIDRule)
	}
	if !c.Action.Valid() {
		return nil, fmt.Errorf("schema: unknown action %q", c.Action)
	}
	if (c.Action == ActionCancel || c.Action == ActionStatus) && c.TargetJobID == "" {
		return nil, fmt.Errorf("schema: %s requires target_job_id", c.Action)
	}
	if c.TargetJobID != "" && !ValidJobID(c.TargetJobID) {
		return nil, errors.New("schema: target_job_id must be " + jobIDRule)
	}
	return &c, nil
}

// PeekJobID best-effort extracts job_id from an otherwise invalid payload so a
// rejection can still be routed to the right job topic. It returns "" when the
// payload has no job_id or one that is not a safe topic level: such a command
// has no topic to be answered on.
func PeekJobID(payload []byte) string {
	var m struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(payload, &m)
	if !ValidJobID(m.JobID) {
		return ""
	}
	return m.JobID
}
