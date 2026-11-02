package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
)

// CanonicalJCS serialises v and returns its RFC 8785 (JSON Canonicalization
// Scheme) form. Canonicalisation is delegated to github.com/gowebpki/jcs, the
// same library the trail-evidence reference implementation uses, so both
// produce byte-identical output.
//
// RFC 8785 uses ES6 number semantics: integers outside ±2^53 lose precision
// and floating point values are re-rendered. Payload values whose exact
// decimal rendering matters must therefore travel as strings.
func CanonicalJCS(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return canonicalizeJSON(raw)
}

// canonicalizeJSON canonicalises already-encoded JSON bytes.
func canonicalizeJSON(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("jcs: empty input")
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("jcs: %w", err)
	}
	return out, nil
}
