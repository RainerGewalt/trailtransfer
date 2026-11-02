package evidence

import "testing"

func TestCanonicalJCSSortsAndFormats(t *testing.T) {
	b, err := CanonicalJCS(map[string]any{"b": 1, "a": "x", "c": []int{2, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"a":"x","b":1,"c":[2,1]}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestCanonicalJCSEscaping(t *testing.T) {
	b, _ := CanonicalJCS(map[string]any{"k": "a\"b\\c\nd\te"})
	if got, want := string(b), `{"k":"a\"b\\c\nd\te"}`; got != want {
		t.Fatalf("got %s", got)
	}
}

func TestCanonicalJCSSortsByUTF16CodeUnits(t *testing.T) {
	// RFC 8785 sorts members by UTF-16 code units: U+1D11E (surrogate pair
	// starting 0xD834) sorts before U+FF21 (single unit), unlike byte order.
	b, _ := CanonicalJCS(map[string]any{"Ａ": 1, "\U0001d11e": 2, "a": 0})
	if got, want := string(b), "{\"a\":0,\"\U0001d11e\":2,\"Ａ\":1}"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestCanonicalJCSUsesES6NumberSemantics(t *testing.T) {
	// RFC 8785 renders numbers as ES6 does: 2^53+1 is not representable and
	// becomes 9007199254740992. This is why exact large integers travel as
	// strings in payloads (see docs/EVIDENCE_MODEL.md).
	b, err := CanonicalJCS(map[string]any{"bytes": int64(9007199254740993)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"bytes":9007199254740992}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	b, _ = CanonicalJCS(map[string]any{"f": 1.0, "g": 1e21, "h": 0.000001})
	if got, want := string(b), `{"f":1,"g":1e+21,"h":0.000001}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestCanonicalJCSRejectsInvalidJSON(t *testing.T) {
	if _, err := canonicalizeJSON([]byte(`{"a":`)); err == nil {
		t.Fatal("invalid JSON must be rejected")
	}
	if _, err := canonicalizeJSON([]byte("  ")); err == nil {
		t.Fatal("empty input must be rejected")
	}
}
