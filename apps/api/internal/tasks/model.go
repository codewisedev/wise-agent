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
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

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
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return DefaultWorkflowConfig(), nil
	}
	if err != nil {
		return DefaultWorkflowConfig(), err
	}
	cfg, err := ParseWorkflowConfig(b)
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// ParseWorkflowConfig decodes workflow defaults (YAML or JSON).
func ParseWorkflowConfig(raw []byte) (WorkflowConfig, error) {
	cfg := DefaultWorkflowConfig()
	var file struct {
		MaxRevisions *int `json:"max_revisions"`
	}
	if err := yaml.UnmarshalStrict(raw, &file); err != nil {
		return cfg, fmt.Errorf("parse: %w", err)
	}
	if file.MaxRevisions != nil {
		if *file.MaxRevisions < 0 || *file.MaxRevisions > 50 {
			return cfg, fmt.Errorf("max_revisions must be between 0 and 50")
		}
		cfg.MaxRevisions = *file.MaxRevisions // 0 = the first rejection fails the workflow
	}
	return cfg, nil
}

// Column roles let workflows move cards on any board layout.
const (
	RoleBacklog    = "backlog"
	RoleInProgress = "in_progress"
	RoleReview     = "review"
	RoleDone       = "done"
)

// Roles lists the workflow roles a column may have.
var Roles = []string{RoleBacklog, RoleInProgress, RoleReview, RoleDone}

// ColumnDef is one column of a board layout.
type ColumnDef struct {
	Name  string `json:"name"`
	Role  string `json:"role,omitempty"`
	Color string `json:"color,omitempty"`
}

// BoardDefaults is the column set new projects get.
type BoardDefaults struct {
	Columns []ColumnDef `json:"columns"`
}

var colorRe = regexp.MustCompile(`^(#[0-9a-fA-F]{6})?$`)

// ParseBoardDefaults decodes and validates a column set (YAML or JSON).
func ParseBoardDefaults(raw []byte) (BoardDefaults, error) {
	var b BoardDefaults
	if err := yaml.UnmarshalStrict(raw, &b); err != nil {
		return b, fmt.Errorf("parse: %w", err)
	}
	return b, ValidateColumns(b.Columns)
}

// ValidateColumns checks names (1-40 chars, unique), roles (known, unique) and colors.
func ValidateColumns(cols []ColumnDef) error {
	if len(cols) == 0 || len(cols) > 20 {
		return fmt.Errorf("a board needs 1 to 20 columns")
	}
	names, roles := map[string]bool{}, map[string]bool{}
	for i, c := range cols {
		n := strings.TrimSpace(c.Name)
		if n == "" || utf8.RuneCountInString(n) > 40 {
			return fmt.Errorf("column %d: name must be 1-40 characters", i+1)
		}
		if names[strings.ToLower(n)] {
			return fmt.Errorf("column %q appears twice", n)
		}
		names[strings.ToLower(n)] = true
		if c.Role != "" {
			if !slices.Contains(Roles, c.Role) {
				return fmt.Errorf("column %q: unknown role %q", n, c.Role)
			}
			if roles[c.Role] {
				return fmt.Errorf("only one column can have the %s role", c.Role)
			}
			roles[c.Role] = true
		}
		if !colorRe.MatchString(c.Color) {
			return fmt.Errorf("column %q: color must be #rrggbb", n)
		}
	}
	return nil
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
