// Package agents owns agent definitions (the registry) and agent runs as seen
// by users: creating, listing, cancelling and reading events.
package agents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"

	"github.com/google/uuid"
	"sigs.k8s.io/yaml"

	agentdefs "wise/packages/agent-definitions"
	agentcontract "wise/packages/contracts/agents"
	"wise/packages/prompts"

	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/schemas"
)

// Definition is the typed view of the fields the control plane needs.
type Definition struct {
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Description  string          `json:"description"`
	Enabled      bool            `json:"enabled"`
	Phase        *int            `json:"phase,omitempty"`
	Prompt       string          `json:"prompt,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Tools        []string        `json:"tools"`
	Permissions  []string        `json:"permissions"`
	ModelConfig  struct {
		Alias           string   `json:"alias"`
		Fallbacks       []string `json:"fallbacks,omitempty"`
		Temperature     *float64 `json:"temperature,omitempty"`
		MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	} `json:"model_config"`
	ExecutionLimits struct {
		MaxTurns     int     `json:"max_turns"`
		MaxToolCalls int     `json:"max_tool_calls"`
		MaxTokens    int64   `json:"max_tokens,omitempty"`
		MaxCostUSD   float64 `json:"max_cost_usd,omitempty"`
		TimeoutS     int     `json:"timeout_s"`
	} `json:"execution_limits"`
	ErrorHandling struct {
		MaxAttempts          int `json:"max_attempts"`
		InvalidOutputRetries int `json:"invalid_output_retries"`
	} `json:"error_handling"`
}

// Tool is a tool catalog entry (packages/agent-definitions/tools.yaml).
type Tool struct {
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	InputSchema         json.RawMessage `json:"input_schema"`
	OutputSchema        json.RawMessage `json:"output_schema,omitempty"`
	SideEffects         string          `json:"side_effects"`
	PermissionsRequired []string        `json:"permissions_required"`
}

// Loaded is one validated definition ready to register.
type Loaded struct {
	Def    Definition
	Raw    json.RawMessage // canonical JSON of the definition
	Prompt string
	Hash   string
}

// Catalog is the result of loading embedded definitions.
type Catalog struct {
	Agents []Loaded
	Tools  map[string]Tool
}

// Load parses and validates the embedded definitions, tools and prompts.
func Load() (Catalog, error) {
	return LoadFS(agentdefs.FS, prompts.FS)
}

// LoadFS is Load with injectable filesystems (tests).
func LoadFS(defs fs.FS, promptFS fs.FS) (Catalog, error) {
	cat := Catalog{Tools: map[string]Tool{}}

	toolsYAML, err := fs.ReadFile(defs, "tools.yaml")
	if err != nil {
		return cat, err
	}
	toolsJSON, err := yaml.YAMLToJSON(toolsYAML)
	if err != nil {
		return cat, fmt.Errorf("tools.yaml: %w", err)
	}
	var rawTools []json.RawMessage
	if err := json.Unmarshal(toolsJSON, &rawTools); err != nil {
		return cat, fmt.Errorf("tools.yaml: %w", err)
	}
	for i, rt := range rawTools {
		if err := schemas.Validate(agentcontract.ToolSchema, rt, fmt.Sprintf("tools[%d]", i)); err != nil {
			return cat, fmt.Errorf("tools.yaml: %w", err)
		}
		var t Tool
		if err := json.Unmarshal(rt, &t); err != nil {
			return cat, err
		}
		if _, err := schemas.Compile(t.InputSchema); err != nil {
			return cat, fmt.Errorf("tool %s input_schema: %w", t.Name, err)
		}
		cat.Tools[t.Name] = t
	}

	files, err := fs.Glob(defs, "agents/*.yaml")
	if err != nil {
		return cat, err
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		l, err := loadOne(defs, promptFS, f, cat.Tools)
		if err != nil {
			return cat, fmt.Errorf("%s: %w", f, err)
		}
		if seen[l.Def.Name] {
			return cat, fmt.Errorf("%s: duplicate agent name %q", f, l.Def.Name)
		}
		seen[l.Def.Name] = true
		cat.Agents = append(cat.Agents, l)
	}
	return cat, nil
}

func loadOne(defs, promptFS fs.FS, file string, tools map[string]Tool) (Loaded, error) {
	y, err := fs.ReadFile(defs, file)
	if err != nil {
		return Loaded{}, err
	}
	j, err := yaml.YAMLToJSON(y)
	if err != nil {
		return Loaded{}, err
	}
	if err := schemas.Validate(agentcontract.DefinitionSchema, j, "definition"); err != nil {
		return Loaded{}, err
	}
	var d Definition
	if err := json.Unmarshal(j, &d); err != nil {
		return Loaded{}, err
	}
	if path.Base(file) != d.Name+".yaml" {
		return Loaded{}, fmt.Errorf("file name must be %s.yaml", d.Name)
	}
	for _, t := range d.Tools {
		if _, ok := tools[t]; !ok {
			return Loaded{}, fmt.Errorf("unknown tool %q", t)
		}
	}
	for _, s := range []json.RawMessage{d.InputSchema, d.OutputSchema} {
		if _, err := schemas.Compile(s); err != nil {
			return Loaded{}, err
		}
	}
	var prompt string
	if d.Prompt != "" {
		b, err := fs.ReadFile(promptFS, d.Prompt)
		if err != nil {
			return Loaded{}, fmt.Errorf("prompt: %w", err)
		}
		prompt = string(b)
	} else if d.Enabled {
		return Loaded{}, fmt.Errorf("enabled agents need a prompt")
	}
	var canon bytes.Buffer
	if err := json.Compact(&canon, j); err != nil {
		return Loaded{}, err
	}
	// The hash covers the definition, its prompt and the referenced tool contracts.
	h := sha256.New()
	h.Write(canon.Bytes())
	h.Write([]byte{0})
	h.Write([]byte(prompt))
	for _, t := range d.Tools {
		tb, _ := json.Marshal(tools[t])
		h.Write([]byte{0})
		h.Write(tb)
	}
	return Loaded{Def: d, Raw: canon.Bytes(), Prompt: prompt, Hash: hex.EncodeToString(h.Sum(nil))}, nil
}

// Sync registers the catalog: unchanged definitions are kept, changed ones get
// a new row that becomes current. Old rows stay for runs that reference them.
func Sync(ctx context.Context, d *db.DB, cat Catalog, log *slog.Logger) error {
	return d.InTx(ctx, func(q *dbgen.Queries) error {
		for _, l := range cat.Agents {
			row, err := q.GetAgentDefinitionByHash(ctx, l.Hash)
			if db.IsNotFound(err) {
				id, err := uuid.NewV7()
				if err != nil {
					return err
				}
				row, err = q.InsertAgentDefinition(ctx, dbgen.InsertAgentDefinitionParams{
					ID: id, Name: l.Def.Name, Version: l.Def.Version, ContentHash: l.Hash,
					Definition: l.Raw, Prompt: l.Prompt, Enabled: l.Def.Enabled,
				})
				if err != nil {
					return fmt.Errorf("register %s: %w", l.Def.Name, err)
				}
				log.Info("agent definition registered", "agent", l.Def.Name, "version", l.Def.Version, "hash", l.Hash[:12])
			} else if err != nil {
				return err
			}
			if err := q.ClearCurrentAgentDefinition(ctx, dbgen.ClearCurrentAgentDefinitionParams{Name: l.Def.Name, ID: row.ID}); err != nil {
				return err
			}
			if err := q.SetAgentDefinitionCurrent(ctx, row.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ParseDefinition decodes a stored definition row.
func ParseDefinition(raw []byte) (Definition, error) {
	var d Definition
	err := json.Unmarshal(raw, &d)
	return d, err
}
