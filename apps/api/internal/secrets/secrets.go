// Package secrets encrypts credentials at rest with AES-256-GCM.
//
// The key comes from WISE_SECRETS_KEY (32 bytes, base64). Each ciphertext is
// bound to its owner through additional authenticated data (e.g. the account
// id), so a ciphertext copied onto another row fails to decrypt.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const prefix = "v1:"

// ErrNoKey means WISE_SECRETS_KEY is not configured.
var ErrNoKey = errors.New("secret storage is disabled: set WISE_SECRETS_KEY (32 random bytes, base64) in .env")

// Box seals and opens secrets. A nil *Box has no key: every call returns ErrNoKey.
type Box struct{ aead cipher.AEAD }

// New parses a base64 key; an empty key returns (nil, nil) so callers can run
// without secret storage.
func New(keyB64 string) (*Box, error) {
	keyB64 = strings.TrimSpace(keyB64)
	if keyB64 == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		if key, err = base64.RawURLEncoding.DecodeString(keyB64); err != nil {
			return nil, errors.New("WISE_SECRETS_KEY must be base64")
		}
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("WISE_SECRETS_KEY must decode to 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Enabled reports whether a key is configured.
func (b *Box) Enabled() bool { return b != nil }

// Seal encrypts plaintext bound to aad.
func (b *Box) Seal(aad, plaintext []byte) (string, error) {
	if b == nil {
		return "", ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, plaintext, aad)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal with the same aad.
func (b *Box) Open(aad []byte, sealed string) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}
	raw, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return nil, errors.New("unknown secret format")
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("corrupt secret")
	}
	n := b.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("corrupt secret")
	}
	pt, err := b.aead.Open(nil, data[:n], data[n:], aad)
	if err != nil {
		return nil, errors.New("secret cannot be decrypted (wrong key or owner)")
	}
	return pt, nil
}

// Hint returns the last 4 characters for display ("…abcd"), or "" for short secrets.
func Hint(secret string) string {
	s := strings.TrimSpace(secret)
	if len(s) < 12 {
		return ""
	}
	return "…" + s[len(s)-4:]
}
