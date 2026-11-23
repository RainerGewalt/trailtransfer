package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A second keygen with the same name used to replace the pair, and with it
// the private key that earlier journals were signed with.
func TestKeygenDoesNotOverwriteAnExistingPair(t *testing.T) {
	base := filepath.Join(t.TempDir(), "worker-01")
	if out, code := runCLIEnv(t, nil, "keygen", "--out", base); code != 0 {
		t.Fatalf("first keygen: exit %d: %s", code, out)
	}
	key, err := os.ReadFile(base + ".key")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(base + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(base + ".key"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode: %v, %v", info.Mode().Perm(), err)
	}

	out, code := runCLIEnv(t, nil, "keygen", "--out", base)
	if code != 1 || !strings.Contains(out, "already exists") {
		t.Fatalf("second keygen: want exit 1 naming the existing file, got exit %d: %s", code, out)
	}
	for path, want := range map[string][]byte{base + ".key": key, base + ".pub": pub} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s changed after the refused keygen (err %v)", path, err)
		}
	}
}

// Only one half present: nothing is written, the remaining half stays.
func TestKeygenRefusesWhenOnlyThePublicKeyExists(t *testing.T) {
	base := filepath.Join(t.TempDir(), "worker-01")
	if err := os.WriteFile(base+".pub", []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLIEnv(t, nil, "keygen", "--out", base)
	if code != 1 || !strings.Contains(out, "worker-01.pub already exists") {
		t.Fatalf("want exit 1 naming the public key, got exit %d: %s", code, out)
	}
	if _, err := os.Stat(base + ".key"); !os.IsNotExist(err) {
		t.Fatalf("a private key was written although keygen refused: %v", err)
	}
	if got, _ := os.ReadFile(base + ".pub"); string(got) != "kept" {
		t.Fatalf("the existing public key changed: %q", got)
	}
}
