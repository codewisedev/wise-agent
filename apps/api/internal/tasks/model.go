// Package tasks owns engineering tasks and the Postgres side of their workflows:
// the status mirror, versioned artifacts, approvals and the command outbox that
// the orchestrator delivers to Temporal.
package tasks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Artifact kinds.
const (
	KindProductAnalysis    = "product_analysis"
	KindRequirements       = "requirements"
	KindPlan               = "plan"
	KindArchitectureReview = "architecture_review"
	KindFinalReport        = "final_report"
)

// Workflow run statuses.
const (
	RunPending          = "pending"
	RunRunning          = "running"
	RunAwaitingApproval = "awaiting_approval"
	RunAwaitingRetry    = "awaiting_retry"
	RunCompleted        = "completed"
	RunFailed           = "failed"
	RunCancelled        = "cancelled"
)

// IsActiveRun reports whether a workflow run can still make progress.
func IsActiveRun(status string) bool {
	switch status {
	case RunPending, RunRunning, RunAwaitingApproval, RunAwaitingRetry:
		return true
	}
	return false
}

// taskStatusFor maps a workflow run status onto its task.
func taskStatusFor(runStatus string) string {
	switch runStatus {
	case RunAwaitingApproval:
		return "awaiting_approval"
	case RunCompleted:
		return "completed"
	case RunFailed:
		return "failed"
	case RunCancelled:
		return "cancelled"
	default:
		return "in_progress"
	}
}

// Command kinds written to the outbox.
const (
	CmdStart    = "start"
	CmdDecision = "decision"
	CmdRetry    = "retry"
	CmdCancel   = "cancel"
)

// Signal names understood by the engineering workflow.
const (
	SignalApprovalDecided = "approval-decided"
	SignalRetry           = "retry"
)

// WorkflowConfig is configs/workflows/engineering.yaml: defaults for every
// workflow run (templates may override them).
type WorkflowConfig struct {
	MaxRevisions int `json:"max_revisions"`
}

// DefaultWorkflowConfig allows 5 rejections per review gate.
func DefaultWorkflowConfig() WorkflowConfig { return WorkflowConfig{MaxRevisions: 5} }

// LoadWorkflowConfig reads the workflow config; a missing file means defaults.
func LoadWorkflowConfig(path string) (WorkflowConfig, error) {
	cfg := DefaultWorkflowConfig()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var file struct {
		MaxRevisions *int `json:"max_revisions"`
	}
	if err := yaml.UnmarshalStrict(b, &file); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if file.MaxRevisions != nil {
		if *file.MaxRevisions < 0 || *file.MaxRevisions > 50 {
			return cfg, fmt.Errorf("%s: max_revisions must be between 0 and 50", path)
		}
		cfg.MaxRevisions = *file.MaxRevisions // 0 = the first rejection fails the workflow
	}
	return cfg, nil
}

// HashContent returns the sha256 of the canonical JSON form of raw (object keys
// sorted, no insignificant whitespace), so equal content always hashes equally
// regardless of how Postgres or a client re-serialized it.
func HashContent(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("artifact content is not JSON: %w", err)
	}
	canon, err := json.Marshal(v) // encoding/json sorts map keys
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// ArtifactRef pins one artifact version inside an approval.
type ArtifactRef struct {
	ArtifactID string `json:"artifactId"`
	Kind       string `json:"kind"`
	Version    int32  `json:"version"`
	Hash       string `json:"hash"`
}

// CombinedHash identifies the exact set of artifact versions under review.
func CombinedHash(refs []ArtifactRef) string {
	lines := make([]string, len(refs))
	for i, r := range refs {
		lines[i] = fmt.Sprintf("%s:%s:%d:%s", r.Kind, r.ArtifactID, r.Version, r.Hash)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}
