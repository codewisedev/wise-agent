package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	// APITokenPrefix marks Wise personal API tokens: wise_<id>_<secret>.
	APITokenPrefix = "wise_"
	tokenIDLen     = 12 // characters of the public lookup prefix
)

var b64 = base64.RawURLEncoding

// randomString returns n random bytes, URL-safe base64 encoded.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64.EncodeToString(b), nil
}

// hashToken returns SHA-256(token). Tokens are high-entropy, so a fast hash is appropriate.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewSessionToken returns an opaque session token.
func NewSessionToken() (string, error) { return randomString(32) }

// newAPIToken returns (lookupPrefix, fullToken).
func newAPIToken() (string, string, error) {
	id, err := randomAlnum(tokenIDLen)
	if err != nil {
		return "", "", err
	}
	secret, err := randomString(32)
	if err != nil {
		return "", "", err
	}
	return id, APITokenPrefix + id + "_" + secret, nil
}

var errMalformedToken = errors.New("malformed api token")

// parseAPIToken extracts the lookup prefix from a full token.
func parseAPIToken(token string) (string, error) {
	rest, ok := strings.CutPrefix(token, APITokenPrefix)
	if !ok {
		return "", errMalformedToken
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok || len(id) != tokenIDLen || secret == "" || !isAlnum(id) {
		return "", errMalformedToken
	}
	return id, nil
}

func tokenHashEqual(token string, hash []byte) bool {
	return subtle.ConstantTimeCompare(hashToken(token), hash) == 1
}

const alnum = "abcdefghijklmnopqrstuvwxyz0123456789"

func randomAlnum(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b), nil
}

func isAlnum(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune(alnum, r) {
			return false
		}
	}
	return true
}
