package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
)

// Report is the final_report artifact: structured facts plus a markdown rendering.
type Report struct {
	Title         string           `json:"title"`
	Request       string           `json:"request"`
	WorkflowRunID string           `json:"workflow_run_id"`
	Artifacts     []ReportArtifact `json:"artifacts"`
	Approvals     []ReportApproval `json:"approvals"`
	Markdown      string           `json:"markdown"`
}

type ReportArtifact struct {
	Kind    string `json:"kind"`
	Version int32  `json:"version"`
	Hash    string `json:"hash"`
}

type ReportApproval struct {
	Gate    string `json:"gate"`
	Round   int32  `json:"round"`
	Status  string `json:"status"`
	Comment string `json:"comment,omitempty"`
}

// BuildReport assembles the report deterministically from stored artifacts.
func BuildReport(task dbgen.Task, arts map[string]dbgen.Artifact, approvals []dbgen.ListApprovalsRow, runID uuid.UUID) Report {
	r := Report{Title: task.Title, Request: task.Description, WorkflowRunID: runID.String(),
		Artifacts: []ReportArtifact{}, Approvals: []ReportApproval{}}
	order := []string{"product_analysis", "requirements", "plan", "architecture_review"}
	obj := map[string]map[string]any{}
	for _, k := range order {
		a, ok := arts[k]
		if !ok {
			continue
		}
		r.Artifacts = append(r.Artifacts, ReportArtifact{Kind: k, Version: a.Version, Hash: a.ContentHash})
		var m map[string]any
		if json.Unmarshal(a.Content, &m) == nil {
			obj[k] = m
		}
	}
	for i := len(approvals) - 1; i >= 0; i-- { // oldest first
		ap := approvals[i].Approval
		if ap.WorkflowRunID != runID {
			continue
		}
		ra := ReportApproval{Gate: ap.Gate, Round: ap.Round, Status: ap.Status}
		if ap.Comment != nil {
			ra.Comment = *ap.Comment
		}
		r.Approvals = append(r.Approvals, ra)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", task.Title)
	if task.Description != "" {
		fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(task.Description, "\n", "\n> "))
	}
	if pa := obj["product_analysis"]; pa != nil {
		b.WriteString("## Product analysis\n\n")
		para(&b, pa["summary"])
		list(&b, "Goals", pa["goals"], nil)
		list(&b, "Key features", pa["key_features"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** (%s) — %s", str(m["name"]), str(m["priority"]), str(m["description"]))
		})
	}
	if rq := obj["requirements"]; rq != nil {
		b.WriteString("## Requirements\n\n")
		list(&b, "User stories", rq["user_stories"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** As %s, I want %s, so that %s.", str(m["id"]), str(m["as_a"]), str(m["i_want"]), str(m["so_that"]))
		})
		list(&b, "Functional requirements", rq["functional_requirements"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** [%s] %s", str(m["id"]), str(m["priority"]), str(m["description"]))
		})
		list(&b, "Non-functional requirements", rq["non_functional_requirements"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** (%s) %s", str(m["id"]), str(m["category"]), str(m["description"]))
		})
		list(&b, "Out of scope", rq["out_of_scope"], nil)
	}
	if pl := obj["plan"]; pl != nil {
		b.WriteString("## Implementation plan\n\n")
		list(&b, "Steps", pl["steps"], func(m map[string]any) string {
			return fmt.Sprintf("**%s %s** (risk: %s) — %s", str(m["id"]), str(m["title"]), str(m["risk"]), str(m["description"]))
		})
		list(&b, "Risks", pl["risks"], nil)
		if ts := str(pl["test_strategy"]); ts != "" {
			fmt.Fprintf(&b, "**Test strategy.** %s\n\n", ts)
		}
	}
	if ar := obj["architecture_review"]; ar != nil {
		b.WriteString("## Architecture\n\n")
		para(&b, ar["summary"])
		fmt.Fprintf(&b, "Scope change: **%s**\n\n", str(ar["scope_change"]))
		list(&b, "Decisions", ar["decisions"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** — %s", str(m["title"]), str(m["rationale"]))
		})
		list(&b, "Concerns", ar["concerns"], nil)
	}
	if len(r.Approvals) > 0 {
		b.WriteString("## Approvals\n\n")
		for _, a := range r.Approvals {
			line := fmt.Sprintf("- %s gate, round %d: **%s**", a.Gate, a.Round, a.Status)
			if a.Comment != "" {
				line += " — " + a.Comment
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Next\n\nImplementation, quality checks and the pull request run in the engineering pipeline (Phase 5).\n")
	r.Markdown = b.String()
	return r
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func para(b *strings.Builder, v any) {
	if s := str(v); s != "" {
		b.WriteString(s + "\n\n")
	}
}

func list(b *strings.Builder, title string, v any, item func(map[string]any) string) {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "**%s**\n\n", title)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && item != nil {
			b.WriteString("- " + item(m) + "\n")
		} else {
			b.WriteString("- " + str(it) + "\n")
		}
	}
	b.WriteString("\n")
}
