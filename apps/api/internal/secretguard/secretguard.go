// Package secretguard recognises credential files and tokens. It mirrors the
// indexer's built-in guard (apps/agent-runtime/wise_runtime/indexer/content.py):
// names and token patterns here are never shown to agents, indexed or embedded.
package secretguard

import "regexp"

// name matches file names that commonly hold credentials.
var name = regexp.MustCompile(`(?i)(^|/)(\.env(\..*)?|\.envrc|id_(rsa|dsa|ecdsa|ed25519)|.*\.(pem|key|p12|pfx|jks|keystore)|credentials(\.json)?|\.netrc|\.npmrc|\.pypirc|secrets?\.(ya?ml|json|toml))$`)

// LooksSecret reports file names that may hold credentials.
func LooksSecret(p string) bool { return name.MatchString(p) }

var tokens = []*regexp.Regexp{
	// A whole key block, then a lone header (e.g. a block cut by truncation).
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY-----[A-Za-z0-9+/=\s\\]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{24,}\b`),
	regexp.MustCompile(`\bpk_\d+_[A-Z0-9]{20,}\b`),
	regexp.MustCompile(`\bATATT[A-Za-z0-9_=-]{20,}\b`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
}

// HasToken reports whether text contains a recognised credential.
func HasToken(text string) bool {
	for _, re := range tokens {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// Redact replaces recognised credentials in text with a marker.
func Redact(text string) string {
	for _, re := range tokens {
		text = re.ReplaceAllString(text, "[redacted secret]")
	}
	return text
}
