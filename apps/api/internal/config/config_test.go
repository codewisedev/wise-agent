package config

import "testing"

func TestCORSOriginsKeepSchemeAndPort(t *testing.T) {
	t.Setenv("WISE_DATABASE_URL", "postgres://x")
	t.Setenv("WISE_CORS_ORIGINS", "http://localhost:5173, https://wise.example.com/ ,")
	t.Setenv("WISE_REPO_ROOTS", "/a:/b,/c")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://localhost:5173", "https://wise.example.com"}
	if len(c.CORSOrigins) != len(want) || c.CORSOrigins[0] != want[0] || c.CORSOrigins[1] != want[1] {
		t.Fatalf("CORSOrigins = %q, want %q", c.CORSOrigins, want)
	}
	if len(c.RepoRoots) != 3 {
		t.Fatalf("RepoRoots = %q, want 3 entries", c.RepoRoots)
	}
}
