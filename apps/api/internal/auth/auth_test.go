package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash format: %s", h)
	}
	ok, err := VerifyPassword("correct horse battery staple", h)
	if err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong", h)
	if err != nil || ok {
		t.Fatalf("expected mismatch, got ok=%v err=%v", ok, err)
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$bad$c2FsdA$aGFzaA"} {
		if _, err := VerifyPassword("x", h); err == nil {
			t.Errorf("expected error for %q", h)
		}
	}
}

func TestAPITokenRoundTrip(t *testing.T) {
	prefix, full, err := newAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(full, APITokenPrefix+prefix+"_") {
		t.Fatalf("token %q does not embed prefix %q", full, prefix)
	}
	got, err := parseAPIToken(full)
	if err != nil || got != prefix {
		t.Fatalf("parse: got %q err=%v", got, err)
	}
	if !tokenHashEqual(full, hashToken(full)) || tokenHashEqual(full+"x", hashToken(full)) {
		t.Fatal("hash comparison broken")
	}
}

func TestParseAPITokenRejectsMalformed(t *testing.T) {
	for _, tok := range []string{"", "wise_", "wise_abc_def", "nope_abcdefghijkl_secret", "wise_ABCDEFGHIJKL_secret", "wise_abcdefghijkl_"} {
		if _, err := parseAPIToken(tok); err == nil {
			t.Errorf("expected error for %q", tok)
		}
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter(3, time.Minute)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	for range 3 {
		if !l.Allowed("ip") {
			t.Fatal("should be allowed")
		}
		l.Fail("ip")
	}
	if l.Allowed("ip") {
		t.Fatal("should be blocked after 3 failures")
	}
	if !l.Allowed("other") {
		t.Fatal("other keys unaffected")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allowed("ip") {
		t.Fatal("window should have expired")
	}
	l.Fail("ip")
	l.Reset("ip")
	if !l.Allowed("ip") {
		t.Fatal("reset should clear failures")
	}
}
