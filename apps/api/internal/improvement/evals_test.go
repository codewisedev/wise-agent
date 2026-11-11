package improvement

import (
	"testing"

	"wise/apps/api/internal/db/dbgen"
)

func i32(v int32) *int32 { return &v }

func TestCheckAndSummarize(t *testing.T) {
	ok := Check(dbgen.AgentRun{Status: dbgen.AgentRunStatusSucceeded, Output: []byte(`{"summary":"","risks":[],"goals":["g"]}`)}, []string{"summary", "risks", "goals"}, 1)
	if !ok.Valid || len(ok.EmptyRequired) != 2 || ok.InvalidAttempts != 1 || ok.Secret {
		t.Fatalf("%+v", ok)
	}
	if s := Check(dbgen.AgentRun{Status: dbgen.AgentRunStatusSucceeded, Output: []byte(`{"summary":"key ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`)}, nil, 0); !s.Secret {
		t.Fatal("secret not flagged")
	}
	if f := Check(dbgen.AgentRun{Status: dbgen.AgentRunStatusFailed}, nil, 3); f.Valid {
		t.Fatal("failed run valid")
	}
	sum := Summarize([]dbgen.EvalResult{
		{Status: "done", BaselineScore: i32(2), CandidateScore: i32(4)},
		{Status: "done", BaselineScore: i32(3), CandidateScore: i32(3)},
		{Status: "done", Checks: []byte(`{"baseline":{"valid":true},"candidate":{"valid":false}}`)},
		{Status: "failed"},
	})
	if sum.Cases != 4 || sum.Judged != 2 || sum.CandidateBetter != 1 || sum.Same != 1 || sum.CandidateInvalid != 1 || sum.Failed != 1 ||
		sum.BaselineMean != 2.5 || sum.CandidateMean != 3.5 {
		t.Fatalf("%+v", sum)
	}
}
