package repositories

import "testing"

func TestValidateRemoteURL(t *testing.T) {
	ok := []string{"https://github.com/org/repo.git", "ssh://git@github.com/org/repo.git", "git@github.com:org/repo.git"}
	bad := []string{"", "http://github.com/x", "file:///etc", "https://user:pw@github.com/x", "https://token@github.com/x", "ext::sh -c id", "https:///nohost"}
	for _, u := range ok {
		if err := ValidateRemoteURL(u); err != nil {
			t.Errorf("%q: unexpected error %v", u, err)
		}
	}
	for _, u := range bad {
		if err := ValidateRemoteURL(u); err == nil {
			t.Errorf("%q: expected error", u)
		}
	}
}
