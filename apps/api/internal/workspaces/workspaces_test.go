package workspaces

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestBranchNames(t *testing.T) {
	cases := map[string]string{
		"Add CSV export!":                     "wise/add-csv-export-0b8f8a52",
		"  Fix: crash when user is nil (#12)": "wise/fix-crash-when-user-is-nil-12-0b8f8a52",
		"Café résumé":                         "wise/cafe-resume-0b8f8a52",
		"…":                                   "wise/task-0b8f8a52",
	}
	for title, want := range cases {
		if got := BranchName("wise/{slug}-{id}", title, "0b8f8a52", ""); got != want {
			t.Errorf("%q → %q, want %q", title, got, want)
		}
	}
	if got := BranchName("feature/{external}-{slug}", "Login page", "0b8f8a52", "CU-86b4x"); got != "feature/cu-86b4x-login-page" {
		t.Errorf("external: %s", got)
	}
	if long := Slug("a very long title that keeps going and going well past the limit of forty characters", 40); len(long) > 40 {
		t.Errorf("slug too long: %s", long)
	}
	for _, b := range []string{"wise/x", "feature/abc-1"} {
		if _, ok := checkBranch(b); !ok {
			t.Errorf("%s rejected", b)
		}
	}
	for _, b := range []string{"", "-x", "a..b", "a b", "x.lock", "a/.b", "a//b", "a@{1", "x/"} {
		if _, ok := checkBranch(b); ok {
			t.Errorf("%q accepted", b)
		}
	}
}

func TestShippedDefaultsAreValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "configs", "execution", "defaults.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	js, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	st, err := ParseSettings(js)
	if err != nil {
		t.Fatal(err)
	}
	if st.MaxFixRounds != 3 || len(st.EgressAllowlist) == 0 || st.Images["node"] == "" {
		t.Fatalf("defaults: %+v", st)
	}
	bad := st
	bad.EgressAllowlist = []string{"10.0.0.1"}
	bad.BranchTemplate = "static"
	bad.CommitAuthor = Author{Name: "x"}
	b, _ := json.Marshal(bad)
	if _, err := ParseSettings(b); err == nil {
		t.Fatal("invalid settings accepted")
	}
}

func TestCommandsOrder(t *testing.T) {
	c := Commands{Test: "npm test", Format: "npm run fmt"}
	l := c.List()
	if len(l) != 2 || l[0][0] != "format" || l[1][0] != "test" {
		t.Fatalf("%v", l)
	}
}

func TestHeadTailKeepsStartAndEnd(t *testing.T) {
	h := newHeadTail(100)
	for i := 0; i < 50; i++ {
		h.add("0123456789")
	}
	out, trunc := h.text()
	if !trunc || len(out) > 160 || out[:10] != "0123456789" || out[len(out)-10:] != "0123456789" {
		t.Fatalf("%v %q", trunc, out)
	}
	small := newHeadTail(100)
	small.add("short")
	if out, trunc := small.text(); trunc || out != "short" {
		t.Fatalf("%q", out)
	}
}

func TestProtectedPaths(t *testing.T) {
	globs := []string{".github/workflows/**", "**/.env", "*.pem", "infra/*.tf"}
	files := []string{".github/workflows/ci.yml", "src/app.go", "config/.env", ".env", "certs/server.pem", "infra/main.tf", "infra/mod/x.tf", "README.md"}
	got := Protected(globs, files)
	want := []string{".github/workflows/ci.yml", "config/.env", ".env", "certs/server.pem", "infra/main.tf"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}
