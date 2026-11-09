package memory

import "testing"

func TestInferredProposalsNeverBecomeRules(t *testing.T) {
	out := downgradeInferred([]Candidate{
		{Scope: "project", Kind: "rule", Title: "a"},
		{Scope: "developer", Kind: "convention", Title: "b"},
		{Scope: "developer", Kind: "decision", Title: "c"},
	})
	want := []string{"preference", "convention", "preference"}
	for i, c := range out {
		if c.Kind != want[i] || c.Scope != "developer" {
			t.Errorf("%d: %+v", i, c)
		}
	}
}
