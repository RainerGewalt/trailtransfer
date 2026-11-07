package evidence

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeKeyPair(t *testing.T, dir string) (keyPath, pubPath string) {
	t.Helper()
	priv, pub, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(dir, "k.key")
	pubPath = filepath.Join(dir, "k.pub")
	if err := os.WriteFile(keyPath, priv, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubPath, pub, 0o644); err != nil {
		t.Fatal(err)
	}
	return keyPath, pubPath
}

func signedSealer(t *testing.T, dir, path string) (*Sealer, ed25519.PublicKey) {
	t.Helper()
	keyPath, pubPath := writeKeyPair(t, dir)
	signer, err := LoadSigner(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestSealer(t, path)
	s.SetSigner(signer)
	if !s.Signed() {
		t.Fatal("sealer must report signing")
	}
	return s, pub
}

func TestSignatureIsOverChainHashASCIIBase64URL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	s, pub := signedSealer(t, dir, path)
	raw, err := s.Seal("transfer.job.result", "job-1", map[string]any{"status": "completed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	r := decode(t, raw)
	if r.Signature == nil || r.Signature.Alg != "ed25519" || !strings.HasPrefix(r.Signature.KeyID, "ed25519:") {
		t.Fatalf("signature member wrong: %+v", r.Signature)
	}
	if strings.ContainsAny(r.Signature.Sig, "+/=") {
		t.Fatalf("sig must be base64url without padding: %s", r.Signature.Sig)
	}
	sig, err := base64.RawURLEncoding.DecodeString(r.Signature.Sig)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, []byte(r.ChainHash), sig) {
		t.Fatal("signature must verify over the ASCII bytes of the chain_hash string")
	}
	if ed25519.Verify(pub, mustHex(t, r.ChainHash), sig) {
		t.Fatal("signature must not be over the decoded digest bytes")
	}
	if r.Signature.KeyID != KeyID(pub) {
		t.Fatalf("key_id %s does not identify the public key %s", r.Signature.KeyID, KeyID(pub))
	}
}

func TestSignedChainVerifies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	s, pub := signedSealer(t, dir, path)
	for i := 0; i < 3; i++ {
		if _, err := s.Seal("transfer.job.result", "", map[string]any{"status": "completed", "n": i}, nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	rep, err := Verify(path, pub)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || !rep.Signed || rep.SignaturesChecked != 3 {
		t.Fatalf("expected 3 verified signatures on an intact chain, got %+v", rep)
	}
	// Without a key the chain still verifies and reports that it is signed.
	rep, _ = Verify(path, nil)
	if !rep.OK || !rep.Signed || rep.SignaturesChecked != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestSignatureInvalidOnForgedRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	s, pub := signedSealer(t, dir, path)
	_, _ = s.Seal("transfer.job.result", "", map[string]any{"status": "completed", "amount": 1}, nil)
	_, _ = s.Seal("transfer.job.result", "", map[string]any{"status": "completed", "amount": 2}, nil)
	s.Close()

	// Forge: change the payload and re-seal both hashes so unsigned checks
	// pass; only the signature (no private key) exposes it. Record 1 must be
	// re-linked as well, as a journal-writing attacker would do.
	reseal(t, path, 0, func(r *Record) { r.Payload = []byte(`{"amount":999999,"status":"completed"}`) })
	first := decode(t, readRecords(t, path)[0])
	reseal(t, path, 1, func(r *Record) { r.PrevHash = first.ChainHash })

	if rep, _ := Verify(path, nil); !rep.OK {
		t.Fatalf("unsigned verify unexpectedly failed on a self-consistent forgery: %+v", rep)
	}
	rep, _ := Verify(path, pub)
	if rep.OK || rep.Count(CodeSignatureInvalid) != 2 || rep.Count(CodeHashMismatch) != 0 {
		t.Fatalf("both re-sealed records must fail signature verification: %+v %s", rep, codes(rep))
	}
}

func TestSignatureInvalidWithWrongKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	s, _ := signedSealer(t, dir, path)
	_, _ = s.Seal("transfer.job.result", "", map[string]any{"k": 1}, nil)
	s.Close()

	otherPub, _, _ := ed25519.GenerateKey(nil)
	rep, _ := Verify(path, otherPub)
	if rep.OK || rep.Count(CodeSignatureInvalid) != 1 {
		t.Fatalf("%+v", rep)
	}
}

func TestVerifySignatureRejectsBadEncoding(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	for _, sig := range []*Signature{
		nil,
		{Alg: "rsa", KeyID: "k", Sig: "AA"},
		{Alg: "ed25519", KeyID: "k", Sig: "not base64url!"},
		{Alg: "ed25519", KeyID: "k", Sig: base64.StdEncoding.EncodeToString(make([]byte, 64))},
		{Alg: "ed25519", KeyID: "k", Sig: base64.RawURLEncoding.EncodeToString(make([]byte, 10))},
	} {
		if err := VerifySignature(pub, GenesisHash, sig); err == nil {
			t.Fatalf("signature %+v must be rejected", sig)
		}
	}
}

func TestLoadSignerRejectsForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.key")
	os.WriteFile(path, []byte("not a pem\n"), 0o600)
	if _, err := LoadSigner(path); err == nil {
		t.Fatal("non-PEM key must be rejected")
	}
	if _, err := LoadPublicKey(path); err == nil {
		t.Fatal("non-PEM public key must be rejected")
	}
}

func mustHex(t *testing.T, h string) []byte {
	t.Helper()
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		var b byte
		if _, err := fmtSscanf(h[7+2*i:9+2*i], &b); err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

func fmtSscanf(s string, b *byte) (int, error) {
	v, err := strconv.ParseUint(s, 16, 8)
	*b = byte(v)
	return 1, err
}

// With a key every record must be signed. A journal written, or rewritten,
// without signatures must not pass with the operator's key.
func TestVerifyWithAKeyFailsAnUnsignedJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	sealN(t, path, 3)
	pub, _, _ := ed25519.GenerateKey(nil)

	rep, err := Verify(path, pub)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Unsigned != 3 || rep.SignaturesChecked != 0 || rep.Count(CodeSignatureInvalid) != 3 {
		t.Fatalf("an unsigned journal must fail with a key: %+v", rep)
	}
	for _, f := range rep.Findings {
		if f.Code == CodeSignatureInvalid && f.Message != "record is not signed" {
			t.Fatalf("a missing signature must be reported as missing: %+v", f)
		}
	}
	// Without a key the same journal is a valid unsigned chain.
	if rep, _ := Verify(path, nil); !rep.OK || rep.Unsigned != 0 {
		t.Fatalf("an unsigned journal must pass without a key: %+v", rep)
	}
}

// Signing switched on after records were written: the earlier records cannot
// pass a signature check, the later ones are verified.
func TestVerifyWithAKeyFailsAPartiallySignedJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	sealN(t, path, 2)
	s, pub := signedSealer(t, dir, path)
	for i := 0; i < 2; i++ {
		if _, err := s.Seal("transfer.job.result", "", map[string]any{"n": i}, nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	rep, _ := Verify(path, pub)
	if rep.OK || rep.Unsigned != 2 || rep.SignaturesChecked != 2 || rep.Count(CodeSignatureInvalid) != 2 {
		t.Fatalf("two unsigned and two signed records: %+v %s", rep, codes(rep))
	}
	if rep, _ := Verify(path, nil); !rep.OK || !rep.Signed {
		t.Fatalf("without a key the chain is intact: %+v", rep)
	}
}

// The rewrite the check closes: a journal writer changes a record, removes
// its signature and re-seals the hashes. The chain stays consistent; only the
// key exposes it.
func TestVerifyWithAKeyFailsARecordWhoseSignatureWasRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	s, pub := signedSealer(t, dir, path)
	_, _ = s.Seal("transfer.job.result", "", map[string]any{"amount": 1}, nil)
	_, _ = s.Seal("transfer.job.result", "", map[string]any{"amount": 2}, nil)
	s.Close()

	reseal(t, path, 1, func(r *Record) {
		r.Payload = []byte(`{"amount":999999}`)
		r.Signature = nil
	})
	if rep, _ := Verify(path, nil); !rep.OK {
		t.Fatalf("the re-sealed chain must pass without a key: %+v %s", rep, codes(rep))
	}
	rep, _ := Verify(path, pub)
	if rep.OK || rep.Unsigned != 1 || rep.SignaturesChecked != 1 || rep.Count(CodeHashMismatch) != 0 {
		t.Fatalf("only the missing signature may expose the rewrite: %+v %s", rep, codes(rep))
	}
}
