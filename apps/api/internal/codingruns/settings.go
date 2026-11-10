package codingruns

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/coding"
)

// CLIVersions are the pinned CLI versions in the runner bundle
// (infrastructure/docker/versions.env); not a panel setting.
var CLIVersions = map[string]string{"claude-code": "2.1.319", "codex": "0.163.0"}

// Runtimes lists the external runtimes.
var Runtimes = []string{"claude-code", "codex"}

// Settings is the panel-editable "coding_runtimes" setting.
type Settings struct {
	Runtimes map[string]RuntimeSettings `json:"runtimes"`
}

// RuntimeSettings configures one runtime.
type RuntimeSettings struct {
	Enabled    bool       `json:"enabled"`
	Model      string     `json:"model"`
	Tools      []string   `json:"tools,omitempty"`
	Sandbox    string     `json:"sandbox,omitempty"`
	Credential Credential `json:"credential"`
	Prices     Prices     `json:"prices"`
	Limits     Limits     `json:"limits"`
}

// Credential says where a runtime's model access comes from.
type Credential struct {
	Mode    string `json:"mode"`    // proxy | subscription
	Account string `json:"account"` // account name ("" + subscription + claude-code = the .env token)
}

// Prices are USD per million tokens (proxy-mode accounting).
type Prices struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

// Limits bound a run.
type Limits struct {
	WallClockS int     `json:"wall_clock_s"`
	IdleS      int     `json:"idle_s"`
	MaxTurns   int     `json:"max_turns"`
	MaxCostUSD float64 `json:"max_cost_usd"`
}

var claudeTools = []string{"Bash", "Read", "Edit", "Write", "NotebookEdit", "Task", "TodoWrite"}

// ParseSettings validates the coding_runtimes setting.
func ParseSettings(raw json.RawMessage) (Settings, error) {
	var s Settings
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, apperr.Validation(apperr.FieldError{Field: "value", Message: err.Error()})
	}
	var fe []apperr.FieldError
	add := func(f, m string) { fe = append(fe, apperr.FieldError{Field: f, Message: m}) }
	for name, r := range s.Runtimes {
		p := "runtimes." + name
		if !slices.Contains(Runtimes, name) {
			add(p, "unknown runtime (claude-code, codex)")
			continue
		}
		if r.Credential.Mode != coding.ModeProxy && r.Credential.Mode != coding.ModeSubscription {
			add(p+".credential.mode", "proxy or subscription")
		}
		if r.Credential.Mode == coding.ModeProxy && r.Credential.Account == "" {
			add(p+".credential.account", "proxy mode needs an API-key account")
		}
		for _, t := range r.Tools {
			if name != "claude-code" || !slices.Contains(claudeTools, t) {
				add(p+".tools", fmt.Sprintf("%q is not an allowed tool (%s)", t, strings.Join(claudeTools, ", ")))
			}
		}
		if r.Sandbox != "" && (name != "codex" || (r.Sandbox != "danger-full-access" && r.Sandbox != "workspace-write")) {
			add(p+".sandbox", "codex only: danger-full-access or workspace-write")
		}
		if r.Prices.InputPerMTok < 0 || r.Prices.OutputPerMTok < 0 {
			add(p+".prices", "must not be negative")
		}
		l := r.Limits
		if l.WallClockS < 60 || l.WallClockS > 6*3600 {
			add(p+".limits.wall_clock_s", "between 60 and 21600")
		}
		if l.IdleS < 30 || l.IdleS > l.WallClockS {
			add(p+".limits.idle_s", "between 30 and the wall-clock limit")
		}
		if l.MaxTurns < 1 || l.MaxTurns > 500 {
			add(p+".limits.max_turns", "between 1 and 500")
		}
		if l.MaxCostUSD < 0 || l.MaxCostUSD > 500 {
			add(p+".limits.max_cost_usd", "between 0 and 500")
		}
	}
	if len(fe) > 0 {
		return s, apperr.Validation(fe...)
	}
	return s, nil
}

// ProjectPolicy is a project's choice of runtimes (projects.settings.coding_runtimes).
// It can only narrow the global setting.
type ProjectPolicy struct {
	Enabled           []string `json:"enabled"`
	AllowSubscription bool     `json:"allowSubscription"`
}

// Validate checks a project policy.
func (p *ProjectPolicy) Validate() error {
	if p.Enabled == nil {
		p.Enabled = []string{}
	}
	for _, r := range p.Enabled {
		if !slices.Contains(Runtimes, r) {
			return apperr.Validation(apperr.FieldError{Field: "enabled", Message: fmt.Sprintf("unknown runtime %q", r)})
		}
	}
	return nil
}

// PolicyFrom reads a project's policy (projects.coding_runtimes; none enabled by default).
func PolicyFrom(raw []byte) ProjectPolicy {
	var p ProjectPolicy
	_ = json.Unmarshal(raw, &p)
	if p.Enabled == nil {
		p.Enabled = []string{}
	}
	return p
}
