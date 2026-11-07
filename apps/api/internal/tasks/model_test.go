package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHashContentIsCanonical(t *testing.T) {
	a, err := HashContent(json.RawMessage(`{"b": [1, 2], "a": {"y": 1, "x": "z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashContent(json.RawMessage(`{"a":{"x":"z","y":1},"b":[1,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("equal content must hash equally: %s vs %s", a, b)
	}
	c, _ := HashContent(json.RawMessage(`{"a":{"x":"z","y":2},"b":[1,2]}`))
	if a == c {
		t.Fatal("different content must hash differently")
	}
}

func TestCombinedHashIsOrderIndependentAndVersionSensitive(t *testing.T) {
	r1 := ArtifactRef{ArtifactID: "a", Kind: KindRequirements, Version: 1, Hash: "h1"}
	r2 := ArtifactRef{ArtifactID: "b", Kind: KindProductAnalysis, Version: 1, Hash: "h2"}
	if CombinedHash([]ArtifactRef{r1, r2}) != CombinedHash([]ArtifactRef{r2, r1}) {
		t.Fatal("order must not matter")
	}
	r1v2 := r1
	r1v2.Version = 2
	if CombinedHash([]ArtifactRef{r1, r2}) == CombinedHash([]ArtifactRef{r1v2, r2}) {
		t.Fatal("a new version must change the combined hash")
	}
}

func TestLoadWorkflowConfig(t *testing.T) {
	cfg, err := LoadWorkflowConfig(filepath.Join("..", "..", "..", "..", "configs", "workflows", "engineering.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GateRequired(GateScope) || !cfg.GateRequired(GatePlan) || cfg.MaxRevisions != 5 {
		t.Fatalf("unexpected repo config: %+v", cfg)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "wf.yaml")
	os.WriteFile(p, []byte("gates:\n  scope: { required: false }\nmax_revisions: 2\n"), 0o600)
	cfg, err = LoadWorkflowConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GateRequired(GateScope) || !cfg.GateRequired(GatePlan) || cfg.MaxRevisions != 2 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	os.WriteFile(p, []byte("gates:\n  deploy: { required: true }\n"), 0o600)
	if _, err := LoadWorkflowConfig(p); err == nil {
		t.Fatal("unknown gates must be rejected")
	}
	os.WriteFile(p, []byte("max_revisions: 0\n"), 0o600)
	if cfg, err := LoadWorkflowConfig(p); err != nil || cfg.MaxRevisions != 0 {
		t.Fatalf("explicit max_revisions: 0 must be honoured: %+v %v", cfg, err)
	}
	if cfg, err := LoadWorkflowConfig(filepath.Join(dir, "missing.yaml")); err != nil || !cfg.GateRequired(GateScope) {
		t.Fatal("a missing file means defaults")
	}
}
