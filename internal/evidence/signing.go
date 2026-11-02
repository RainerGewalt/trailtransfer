package evidence

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
)

// Signer holds an Ed25519 private key used to sign each record's chain hash.
// Signatures make the chain non-repudiable and defeat a whole-suffix rewrite
// (an actor without the key cannot re-sign forged records).
type Signer struct {
	priv  ed25519.PrivateKey
	keyID string
}

// KeyID derives a short, stable identifier from an Ed25519 public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:8])
}

// ID returns this signer's key id.
func (s *Signer) ID() string { return s.keyID }

// LoadSigner reads a PEM PKCS#8 Ed25519 private key.
func LoadSigner(path string) (*Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return nil, fmt.Errorf("no PEM block in %q", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key in %q is not an ed25519 private key", path)
	}
	return &Signer{priv: priv, keyID: KeyID(priv.Public().(ed25519.PublicKey))}, nil
}

// sign produces a detached signature over the ASCII bytes of the chain_hash
// string, encoded as base64url without padding, as the specification requires.
func (s *Signer) sign(chainHash string) *Signature {
	sig := ed25519.Sign(s.priv, []byte(chainHash))
	return &Signature{Alg: "ed25519", KeyID: s.keyID, Sig: base64.RawURLEncoding.EncodeToString(sig)}
}

// VerifySignature checks sig over the ASCII bytes of chainHash with pub.
func VerifySignature(pub ed25519.PublicKey, chainHash string, sig *Signature) error {
	if sig == nil {
		return fmt.Errorf("no signature")
	}
	if sig.Alg != "ed25519" {
		return fmt.Errorf("unsupported signature alg %q", sig.Alg)
	}
	sb, err := base64.RawURLEncoding.DecodeString(sig.Sig)
	if err != nil {
		return fmt.Errorf("signature is not base64url without padding: %w", err)
	}
	if len(sb) != ed25519.SignatureSize {
		return fmt.Errorf("signature has %d bytes, expected %d", len(sb), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, []byte(chainHash), sb) {
		return fmt.Errorf("signature does not verify with key %s", sig.KeyID)
	}
	return nil
}

// LoadPublicKey reads a PEM PKIX Ed25519 public key.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return nil, fmt.Errorf("no PEM block in %q", path)
	}
	key, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key in %q is not an ed25519 public key", path)
	}
	return pub, nil
}

// GenerateKeyPair returns a fresh Ed25519 key pair as PEM (PKCS#8 / PKIX).
func GenerateKeyPair() (privPEM, pubPEM []byte, keyID string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, "", err
	}
	pkix, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, nil, "", err
	}
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkix})
	return privPEM, pubPEM, KeyID(pub), nil
}
