package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/tasks"
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

// BuildReport assembles the report deterministically from stored artifacts,
// one section per enabled step in plan order.
func BuildReport(task dbgen.Task, plan tasks.Plan, arts map[string]dbgen.Artifact, approvals []dbgen.ListApprovalsRow, runID uuid.UUID) Report {
	r := Report{Title: task.Title, Request: task.Description, WorkflowRunID: runID.String(),
		Artifacts: []ReportArtifact{}, Approvals: []ReportApproval{}}
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
	done := map[string]bool{}
	for _, st := range plan.Steps {
		a, ok := arts[st.Kind]
		if !st.Enabled || !ok || done[st.Kind] {
			continue
		}
		done[st.Kind] = true
		r.Artifacts = append(r.Artifacts, ReportArtifact{Kind: st.Kind, Version: a.Version, Hash: a.ContentHash})
		var m map[string]any
		if json.Unmarshal(a.Content, &m) != nil {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", st.Name)
		if render, ok := sectionRenderers[st.Kind]; ok {
			render(&b, m)
		} else {
			genericSection(&b, m)
		}
	}
	if len(r.Approvals) > 0 {
		b.WriteString("## Reviews\n\n")
		for _, a := range r.Approvals {
			name := a.Gate
			if st, _, ok := plan.Step(a.Gate); ok {
				name = st.Name
			}
			line := fmt.Sprintf("- %s, round %d: **%s**", name, a.Round, a.Status)
			if a.Comment != "" {
				line += " — " + a.Comment
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	r.Markdown = b.String()
	return r
}

var sectionRenderers = map[string]func(*strings.Builder, map[string]any){
	tasks.KindProductAnalysis: func(b *strings.Builder, pa map[string]any) {
		para(b, pa["summary"])
		list(b, "Goals", pa["goals"], nil)
		list(b, "Key features", pa["key_features"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** (%s) — %s", str(m["name"]), str(m["priority"]), str(m["description"]))
		})
	},
	tasks.KindRequirements: func(b *strings.Builder, rq map[string]any) {
		list(b, "User stories", rq["user_stories"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** As %s, I want %s, so that %s.", str(m["id"]), str(m["as_a"]), str(m["i_want"]), str(m["so_that"]))
		})
		list(b, "Functional requirements", rq["functional_requirements"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** [%s] %s", str(m["id"]), str(m["priority"]), str(m["description"]))
		})
		list(b, "Non-functional requirements", rq["non_functional_requirements"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** (%s) %s", str(m["id"]), str(m["category"]), str(m["description"]))
		})
		list(b, "Out of scope", rq["out_of_scope"], nil)
	},
	tasks.KindPlan: func(b *strings.Builder, pl map[string]any) {
		list(b, "Steps", pl["steps"], func(m map[string]any) string {
			return fmt.Sprintf("**%s %s** (risk: %s) — %s", str(m["id"]), str(m["title"]), str(m["risk"]), str(m["description"]))
		})
		list(b, "Risks", pl["risks"], nil)
		if ts := str(pl["test_strategy"]); ts != "" {
			fmt.Fprintf(b, "**Test strategy.** %s\n\n", ts)
		}
	},
	tasks.KindArchitectureReview: func(b *strings.Builder, ar map[string]any) {
		para(b, ar["summary"])
		fmt.Fprintf(b, "Scope change: **%s**\n\n", str(ar["scope_change"]))
		list(b, "Decisions", ar["decisions"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** — %s", str(m["title"]), str(m["rationale"]))
		})
		list(b, "Concerns", ar["concerns"], nil)
	},
	tasks.KindImplementation: func(b *strings.Builder, im map[string]any) {
		para(b, im["summary"])
		list(b, "Changed files", im["changed_files"], func(m map[string]any) string {
			return fmt.Sprintf("`%s` (%s) — %s", str(m["path"]), str(m["change"]), str(m["why"]))
		})
		list(b, "Tests", im["tests"], func(m map[string]any) string {
			return fmt.Sprintf("`%s` — %s", str(m["path"]), str(m["description"]))
		})
		list(b, "Notes", im["notes"], nil)
	},
	tasks.KindQualityReport: func(b *strings.Builder, q map[string]any) {
		list(b, "Checks", q["checks"], func(m map[string]any) string {
			return fmt.Sprintf("**%s** `%s`: %s", str(m["name"]), str(m["command"]), str(m["status"]))
		})
		if n := str(q["note"]); n != "" {
			para(b, n)
		}
	},
	tasks.KindChangeset: func(b *strings.Builder, cs map[string]any) {
		head := str(cs["headSha"])
		if head == "" {
			b.WriteString("No changes.\n\n")
			return
		}
		fmt.Fprintf(b, "Commit `%s` on `%s` (base `%s`): +%v −%v\n\n", head[:min(12, len(head))], str(cs["branch"]),
			str(cs["baseSha"])[:min(12, len(str(cs["baseSha"])))], cs["additions"], cs["deletions"])
		list(b, "Files", cs["files"], func(m map[string]any) string {
			return fmt.Sprintf("`%s` %s (+%v −%v)", str(m["path"]), str(m["status"]), m["additions"], m["deletions"])
		})
	},
	tasks.KindDelivery: func(b *strings.Builder, d map[string]any) {
		if br := str(d["branch"]); br != "" {
			fmt.Fprintf(b, "Branch **`%s`** is in your repository at `%s`. Check it out with `git switch %s`.\n\n", br, str(d["sha"])[:min(12, len(str(d["sha"])))], br)
		} else {
			para(b, d["note"])
		}
	},
}

// genericSection renders any agent output: summary first, then lists and text.
func genericSection(b *strings.Builder, m map[string]any) {
	para(b, m["summary"])
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "summary" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		title := strings.ToUpper(k[:1]) + strings.ReplaceAll(k[1:], "_", " ")
		switch v := m[k].(type) {
		case []any:
			list(b, title, v, func(o map[string]any) string { return str(o) })
		case string:
			if v != "" {
				fmt.Fprintf(b, "**%s.** %s\n\n", title, v)
			}
		}
	}
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
