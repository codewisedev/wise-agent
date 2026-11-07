package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func newKey(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	rand.Read(k)
	return base64.StdEncoding.EncodeToString(k)
}

func TestSealOpenRoundTrip(t *testing.T) {
	b, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal([]byte("acct-1"), []byte("sk-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "sk-secret") {
		t.Fatal("ciphertext leaks plaintext")
	}
	pt, err := b.Open([]byte("acct-1"), sealed)
	if err != nil || string(pt) != "sk-secret-value" {
		t.Fatalf("round trip: %q %v", pt, err)
	}
	if again, _ := b.Seal([]byte("acct-1"), []byte("sk-secret-value")); again == sealed {
		t.Fatal("nonces must differ")
	}
}

func TestOpenRejectsOtherOwnerAndKey(t *testing.T) {
	b, _ := New(newKey(t))
	sealed, _ := b.Seal([]byte("acct-1"), []byte("x"))
	if _, err := b.Open([]byte("acct-2"), sealed); err == nil {
		t.Fatal("a ciphertext must not open for another owner")
	}
	other, _ := New(newKey(t))
	if _, err := other.Open([]byte("acct-1"), sealed); err == nil {
		t.Fatal("a ciphertext must not open with another key")
	}
}

func TestKeyValidationAndNilBox(t *testing.T) {
	if b, err := New(""); b != nil || err != nil {
		t.Fatal("empty key means disabled, not an error")
	}
	if _, err := New(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("short keys must be rejected")
	}
	var b *Box
	if _, err := b.Seal(nil, []byte("x")); !errors.Is(err, ErrNoKey) || b.Enabled() {
		t.Fatal("nil box must report ErrNoKey")
	}
	if Hint("sk-abcdefghijklmnop") != "…mnop" || Hint("short") != "" {
		t.Fatal("hint")
	}
}
