package workspaces

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
)

// FileReader reads repository files with the secret guard applied.
type FileReader interface {
	ReadFile(ctx context.Context, id uuid.UUID, rel string) ([]byte, int, bool, dbgen.Repository, error)
}

// Detection is a suggested execution setup with the reasons for it.
type Detection struct {
	Settings RepoSettings `json:"settings"`
	Detected []string     `json:"detected"` // languages found, in priority order
	Notes    []string     `json:"notes"`
}

// Detect suggests execution settings from a repository's files (package.json,
// go.mod, pyproject.toml / requirements.txt). Nothing is saved.
func (s *Service) Detect(ctx context.Context, files FileReader, repoID uuid.UUID) (Detection, error) {
	if _, _, err := s.RepoSettings(ctx, repoID); err != nil {
		return Detection{}, err
	}
	read := func(p string) (string, bool) {
		b, _, _, _, err := files.ReadFile(ctx, repoID, p)
		return string(b), err == nil
	}
	images := s.Settings().Images
	d := Detection{Detected: []string{}, Notes: []string{}}
	var pick *RepoSettings

	if pkg, ok := read("package.json"); ok {
		d.Detected = append(d.Detected, "node")
		rs := detectNode(pkg, read)
		rs.Image = images["node"]
		pick = &rs
	}
	if gomod, ok := read("go.mod"); ok {
		d.Detected = append(d.Detected, "go")
		if pick == nil {
			rs := RepoSettings{Image: images["go"], Tools: []string{"go"}, Setup: []string{"go mod download"},
				Commands: Commands{Format: `test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }`, Lint: "go vet ./...", Test: "go test ./..."}}
			if m := regexp.MustCompile(`(?m)^go (\d+\.\d+)`).FindStringSubmatch(gomod); m != nil {
				d.Notes = append(d.Notes, "go.mod asks for Go "+m[1]+"; check the image provides it")
			}
			pick = &rs
		}
	}
	py, hasPyproject := read("pyproject.toml")
	req, hasReq := read("requirements.txt")
	if hasPyproject || hasReq {
		d.Detected = append(d.Detected, "python")
		if pick == nil {
			rs := RepoSettings{Image: images["python"], Tools: []string{"python", "pip"}}
			switch {
			case hasReq:
				rs.Setup = []string{"pip install --user -r requirements.txt"}
			default:
				rs.Setup = []string{"pip install --user -e ."}
			}
			text := py + req
			if strings.Contains(text, "pytest") {
				rs.Commands.Test = "python -m pytest -q"
			}
			if strings.Contains(text, "ruff") {
				rs.Commands.Lint, rs.Commands.Format = "python -m ruff check .", "python -m ruff format --check ."
			}
			if strings.Contains(text, "mypy") {
				rs.Commands.Typecheck = "python -m mypy ."
			}
			pick = &rs
		}
	}
	if _, ok := read("Cargo.toml"); ok {
		d.Detected = append(d.Detected, "rust")
		if pick == nil {
			d.Notes = append(d.Notes, "Rust detected: use an image with cargo and git (e.g. one built FROM rust:1-alpine with git added)")
		}
	}
	if pick == nil {
		d.Notes = append(d.Notes, "no package.json, go.mod, pyproject.toml or requirements.txt at the repository root; fill the settings in by hand")
		return d, nil
	}
	if len(d.Detected) > 1 {
		d.Notes = append(d.Notes, "several languages found; the suggestion uses "+d.Detected[0]+" — add the others' commands if their checks matter")
	}
	if pick.Setup == nil {
		pick.Setup = []string{}
	}
	pick.Env = map[string]string{}
	d.Settings = *pick
	return d, nil
}

func detectNode(pkg string, read func(string) (string, bool)) RepoSettings {
	var p struct {
		Scripts        map[string]string `json:"scripts"`
		PackageManager string            `json:"packageManager"`
	}
	_ = json.Unmarshal([]byte(pkg), &p)
	rs := RepoSettings{Tools: []string{"node", "npm"}}
	run := "npm run"
	switch {
	case exists(read, "pnpm-lock.yaml") || strings.HasPrefix(p.PackageManager, "pnpm"):
		rs.Setup = []string{"npm install -g pnpm", "pnpm install --frozen-lockfile"}
		rs.Tools = append(rs.Tools, "pnpm")
		run = "pnpm run"
	case exists(read, "yarn.lock") || strings.HasPrefix(p.PackageManager, "yarn"):
		rs.Setup = []string{"yarn install --frozen-lockfile"}
		rs.Tools = append(rs.Tools, "yarn")
		run = "yarn run"
	case exists(read, "package-lock.json"):
		rs.Setup = []string{"npm ci"}
	default:
		// No lockfile: do not create one (it would show up in the task's change).
		rs.Setup = []string{"npm install --no-package-lock"}
	}
	// pnpm is installed with npm, so the pnpm preflight check would run
	// before it exists: check it after setup instead.
	if run == "pnpm run" {
		rs.Tools = rs.Tools[:2]
	}
	first := func(names ...string) string {
		for _, n := range names {
			if _, ok := p.Scripts[n]; ok {
				return run + " " + n
			}
		}
		return ""
	}
	if t, ok := p.Scripts["test"]; ok && !strings.Contains(t, "no test specified") {
		rs.Commands.Test = run + " test"
	}
	rs.Commands.Lint = first("lint")
	rs.Commands.Format = first("format:check", "fmt:check", "prettier:check", "check-format")
	rs.Commands.Typecheck = first("typecheck", "type-check", "tsc", "check-types")
	return rs
}

func exists(read func(string) (string, bool), p string) bool {
	_, ok := read(p)
	return ok
}
