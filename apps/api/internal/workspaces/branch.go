package workspaces

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Slug turns a title into a short branch-friendly slug.
func Slug(title string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFKD.String(strings.ToLower(title)) {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case unicode.Is(unicode.Mn, r):
			// drop accents
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= max {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > max {
		s = strings.Trim(s[:max], "-")
	}
	if s == "" {
		s = "task"
	}
	return s
}

// BranchName fills a branch template.
func BranchName(tmpl, title, shortID, external string) string {
	ext := shortID
	if e := Slug(external, 40); external != "" && e != "task" {
		ext = e
	}
	r := strings.NewReplacer("{slug}", Slug(title, 40), "{id}", shortID, "{external}", ext)
	return r.Replace(tmpl)
}

// checkBranch validates a branch name like `git check-ref-format --branch`.
func checkBranch(b string) (string, bool) {
	if b == "" || b == "@" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") || strings.Contains(b, "..") || strings.Contains(b, "//") ||
		strings.Contains(b, "@{") || len(b) > 200 {
		return b, false
	}
	for _, r := range b {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return b, false
		}
	}
	for _, part := range strings.Split(b, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return b, false
		}
	}
	return b, true
}
