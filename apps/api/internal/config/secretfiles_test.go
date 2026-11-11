package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandFileEnvAndPassword(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(f, []byte("s3cret\n"), 0o600)
	t.Setenv("WISE_TEST_TOKEN", "")
	t.Setenv("WISE_TEST_TOKEN_FILE", f)
	t.Setenv("OTHER_TOOL_FILE", "/nonexistent") // not ours: ignored
	t.Setenv("WISE_KEEP", "explicit")
	t.Setenv("WISE_KEEP_FILE", f)
	if err := ExpandFileEnv(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("WISE_TEST_TOKEN") != "s3cret" || os.Getenv("WISE_KEEP") != "explicit" {
		t.Fatalf("got %q %q", os.Getenv("WISE_TEST_TOKEN"), os.Getenv("WISE_KEEP"))
	}
	t.Setenv("WISE_POSTGRES_PASSWORD", "p@ss/word")
	if got := withPassword("postgres://wise@postgres:5432/wise?sslmode=disable"); got != "postgres://wise:p%40ss%2Fword@postgres:5432/wise?sslmode=disable" {
		t.Fatalf("url: %s", got)
	}
	if got := withPassword("postgres://wise:other@db/wise"); got != "postgres://wise:other@db/wise" {
		t.Fatalf("explicit password replaced: %s", got)
	}
}
