package codeindex

import "testing"

func TestSearchTextSplitsIdentifiers(t *testing.T) {
	got := SearchText("func (g *Gateway) ReconfigureHTTPServer(max_retries)")
	for _, w := range []string{"reconfigurehttpserver", "reconfigure", "http", "server", "max_retries", "retries", "gateway"} {
		if !contains(got, w) {
			t.Errorf("%q missing from %q", w, got)
		}
	}
}

func TestAnyWordsDropsStopwords(t *testing.T) {
	if got := anyWords("how are run tokens hashed?"); got != "run:* | tokens:* | hashed:*" {
		t.Errorf("anyWords = %q", got)
	}
	if got := anyWords("the of and"); got != "" {
		t.Errorf("only stopwords: %q", got)
	}
	if got := queryText("ReconfigureGateway max_retries"); got != "reconfigure gateway max retries" {
		t.Errorf("queryText = %q", got)
	}
}

func contains(s, w string) bool {
	for _, f := range splitFields(s) {
		if f == w {
			return true
		}
	}
	return false
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		if r == ' ' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}
