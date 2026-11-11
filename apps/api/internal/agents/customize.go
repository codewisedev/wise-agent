package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// WithTools sets the tool catalog used to validate panel-edited agents.
func (s *Service) WithTools(tools map[string]Tool) *Service {
	s.tools = tools
	return s
}

// Tools returns the tool catalog (tools are implemented by the runtime).
func (s *Service) Tools() map[string]Tool { return s.tools }

// Origin describes where an agent's current definition comes from.
type Origin struct {
	Source          string // shipped | edited | custom
	UpdateAvailable bool   // edited, and a newer shipped version exists
	Shipped         *dbgen.AgentDefinition
}

// Origin reports whether an agent is shipped as-is, edited or panel-only.
func (s *Service) Origin(ctx context.Context, cur dbgen.AgentDefinition) (Origin, error) {
	shipped, err := s.db.GetLatestShippedAgentDefinition(ctx, cur.Name)
	if db.IsNotFound(err) {
		return Origin{Source: "custom"}, nil
	}
	if err != nil {
		return Origin{}, err
	}
	o := Origin{Source: "shipped", Shipped: &shipped}
	if cur.Source == "panel" {
		o.Source = "edited"
		o.UpdateAvailable = cur.BaseHash != nil && *cur.BaseHash != shipped.ContentHash
	}
	return o, nil
}

func (s *Service) validate(raw json.RawMessage, prompt string) (Loaded, error) {
	l, err := Validate(raw, prompt, s.tools)
	if err != nil {
		if _, ok := apperr.As(err); ok {
			return l, err
		}
		return l, apperr.Validation(apperr.FieldError{Field: "definition", Message: err.Error()})
	}
	return l, nil
}

// store inserts (or reuses, for identical content) a panel row and makes it current.
func (s *Service) store(ctx context.Context, q *dbgen.Queries, l Loaded, baseHash *string, userID *uuid.UUID, action string) (dbgen.AgentDefinition, error) {
	row, err := q.GetAgentDefinitionByHash(ctx, l.Hash)
	if db.IsNotFound(err) {
		id, err := uuid.NewV7()
		if err != nil {
			return row, err
		}
		if row, err = q.InsertAgentDefinition(ctx, dbgen.InsertAgentDefinitionParams{
			ID: id, Name: l.Def.Name, Version: l.Def.Version, ContentHash: l.Hash, Definition: l.Raw, Prompt: l.Prompt,
			Enabled: l.Def.Enabled, Source: "panel", BaseHash: baseHash, CreatedBy: userID,
		}); err != nil {
			return row, err
		}
	} else if err != nil {
		return row, err
	} else if row.Name != l.Def.Name {
		return row, apperr.Conflict("another agent has exactly this definition")
	}
	if row.Candidate { // an evaluated candidate becomes a real version when saved
		if err := q.ClearAgentDefinitionCandidate(ctx, row.ID); err != nil {
			return row, err
		}
		row.Candidate = false
	}
	if row.Archived {
		if err := q.UnarchiveAgentDefinition(ctx, row.ID); err != nil {
			return row, err
		}
	}
	if err := q.ClearCurrentAgentDefinition(ctx, dbgen.ClearCurrentAgentDefinitionParams{Name: l.Def.Name, ID: row.ID}); err != nil {
		return row, err
	}
	if err := q.SetAgentDefinitionCurrent(ctx, row.ID); err != nil {
		return row, err
	}
	return row, audit.Record(ctx, q, audit.Entry{Action: action, EntityType: "agent", EntityID: audit.Ptr(row.ID),
		Payload: map[string]any{"name": l.Def.Name, "version": l.Def.Version, "hash": l.Hash[:12]}})
}

// Save replaces an existing agent's definition and prompt (a new version).
func (s *Service) Save(ctx context.Context, name string, raw json.RawMessage, prompt string, userID *uuid.UUID) (dbgen.AgentDefinition, error) {
	l, err := s.validate(raw, prompt)
	if err != nil {
		return dbgen.AgentDefinition{}, err
	}
	if l.Def.Name != name {
		return dbgen.AgentDefinition{}, apperr.Validation(apperr.FieldError{Field: "definition.name", Message: "renaming an agent is not supported; duplicate it instead"})
	}
	var row dbgen.AgentDefinition
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.GetCurrentAgentDefinitionByName(ctx, name); db.IsNotFound(err) {
			return apperr.NotFound(fmt.Sprintf("agent %q not found", name))
		} else if err != nil {
			return err
		}
		var base *string
		if shipped, err := q.GetLatestShippedAgentDefinition(ctx, name); err == nil {
			if shipped.ContentHash == l.Hash {
				// Identical to the shipped default: this is a reset, not an edit.
				row, err = s.store(ctx, q, l, nil, userID, "agent.update")
				if err == nil {
					return nil
				}
				return err
			}
			base = &shipped.ContentHash
		} else if !db.IsNotFound(err) {
			return err
		}
		row, err = s.store(ctx, q, l, base, userID, "agent.update")
		return err
	})
	return row, err
}

// Create adds a new agent defined in the panel.
func (s *Service) Create(ctx context.Context, raw json.RawMessage, prompt string, userID *uuid.UUID) (dbgen.AgentDefinition, error) {
	l, err := s.validate(raw, prompt)
	if err != nil {
		return dbgen.AgentDefinition{}, err
	}
	var row dbgen.AgentDefinition
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.GetCurrentAgentDefinitionByName(ctx, l.Def.Name); err == nil {
			return apperr.Conflict(fmt.Sprintf("an agent named %q already exists", l.Def.Name))
		} else if !db.IsNotFound(err) {
			return err
		}
		row, err = s.store(ctx, q, l, nil, userID, "agent.create")
		return err
	})
	return row, err
}

// Duplicate copies an agent under a new name.
func (s *Service) Duplicate(ctx context.Context, name, newName string, userID *uuid.UUID) (dbgen.AgentDefinition, error) {
	cur, err := s.db.GetCurrentAgentDefinitionByName(ctx, name)
	if db.IsNotFound(err) {
		return cur, apperr.NotFound(fmt.Sprintf("agent %q not found", name))
	}
	if err != nil {
		return cur, err
	}
	var def map[string]any
	if err := json.Unmarshal(cur.Definition, &def); err != nil {
		return cur, err
	}
	def["name"] = newName
	delete(def, "prompt") // the prompt is stored with the row, not referenced by path
	raw, _ := json.Marshal(def)
	return s.Create(ctx, raw, cur.Prompt, userID)
}

// Reset makes the latest shipped definition current again.
func (s *Service) Reset(ctx context.Context, name string) (dbgen.AgentDefinition, error) {
	var row dbgen.AgentDefinition
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		row, err = q.GetLatestShippedAgentDefinition(ctx, name)
		if db.IsNotFound(err) {
			return apperr.Conflict(fmt.Sprintf("%q was created in the panel; it has no shipped default", name))
		}
		if err != nil {
			return err
		}
		return s.makeCurrent(ctx, q, row, "agent.reset")
	})
	return row, err
}

func (s *Service) makeCurrent(ctx context.Context, q *dbgen.Queries, row dbgen.AgentDefinition, action string) error {
	if row.Archived {
		if err := q.UnarchiveAgentDefinition(ctx, row.ID); err != nil {
			return err
		}
	}
	if err := q.ClearCurrentAgentDefinition(ctx, dbgen.ClearCurrentAgentDefinitionParams{Name: row.Name, ID: row.ID}); err != nil {
		return err
	}
	if err := q.SetAgentDefinitionCurrent(ctx, row.ID); err != nil {
		return err
	}
	return audit.Record(ctx, q, audit.Entry{Action: action, EntityType: "agent", EntityID: audit.Ptr(row.ID),
		Payload: map[string]any{"name": row.Name, "version": row.Version}})
}

// Candidate stores the agent's current definition with a different prompt as
// a non-current version to evaluate. Identical content reuses the existing row.
func (s *Service) Candidate(ctx context.Context, name, prompt string, userID *uuid.UUID) (dbgen.AgentDefinition, error) {
	cur, err := s.db.GetCurrentAgentDefinitionByName(ctx, name)
	if db.IsNotFound(err) {
		return cur, apperr.NotFound(fmt.Sprintf("agent %q not found", name))
	}
	if err != nil {
		return cur, err
	}
	l, err := s.validate(cur.Definition, prompt)
	if err != nil {
		return cur, err
	}
	row, err := s.db.GetAgentDefinitionByHash(ctx, l.Hash)
	switch {
	case err == nil && row.Name != name:
		return row, apperr.Conflict("another agent has exactly this definition")
	case err == nil:
		return row, nil
	case !db.IsNotFound(err):
		return row, err
	}
	var base *string // as Save: the shipped version the edit derives from
	if shipped, err := s.db.GetLatestShippedAgentDefinition(ctx, name); err == nil {
		base = &shipped.ContentHash
	} else if !db.IsNotFound(err) {
		return row, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return row, err
	}
	return s.db.InsertCandidateAgentDefinition(ctx, dbgen.InsertCandidateAgentDefinitionParams{
		ID: id, Name: name, Version: l.Def.Version, ContentHash: l.Hash, Definition: l.Raw, Prompt: l.Prompt,
		Enabled: l.Def.Enabled, BaseHash: base, CreatedBy: userID,
	})
}

// Versions lists an agent's definitions, newest first.
func (s *Service) Versions(ctx context.Context, name string) ([]dbgen.AgentDefinition, error) {
	rows, err := s.db.ListAgentDefinitionVersions(ctx, name)
	if err == nil && len(rows) == 0 {
		return nil, apperr.NotFound(fmt.Sprintf("agent %q not found", name))
	}
	return rows, err
}

// Restore makes an earlier version current.
func (s *Service) Restore(ctx context.Context, name string, id uuid.UUID) (dbgen.AgentDefinition, error) {
	var row dbgen.AgentDefinition
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		row, err = q.GetAgentDefinition(ctx, id)
		if db.IsNotFound(err) || (err == nil && (row.Name != name || row.Candidate)) {
			return apperr.NotFound("version not found")
		}
		if err != nil {
			return err
		}
		return s.makeCurrent(ctx, q, row, "agent.restore")
	})
	return row, err
}

// ErrShipped means a shipped agent cannot be removed (disable it instead).
var ErrShipped = errors.New("shipped agents cannot be deleted")

// Archive removes a panel-only agent (its runs keep their definition rows).
func (s *Service) Archive(ctx context.Context, name string) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.GetCurrentAgentDefinitionByName(ctx, name)
		if db.IsNotFound(err) {
			return apperr.NotFound(fmt.Sprintf("agent %q not found", name))
		}
		if err != nil {
			return err
		}
		if _, err := q.GetLatestShippedAgentDefinition(ctx, name); err == nil {
			return apperr.Conflict("shipped agents cannot be deleted; switch them off (enabled: false) instead").WithCause(ErrShipped)
		}
		if err := q.ArchiveAgent(ctx, name); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "agent.delete", EntityType: "agent", EntityID: audit.Ptr(cur.ID),
			Payload: map[string]any{"name": name}})
	})
}

// Lookup resolves an agent's workflow info from its current (possibly edited) definition.
func (s *Service) Lookup(name string) (Info, bool) {
	row, err := s.db.GetCurrentAgentDefinitionByName(context.Background(), name)
	if err != nil {
		return Info{}, false
	}
	d, err := ParseDefinition(row.Definition)
	if err != nil {
		return Info{}, false
	}
	return infoOf(d), true
}
