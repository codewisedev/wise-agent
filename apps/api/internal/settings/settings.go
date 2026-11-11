// Package settings stores panel-managed configuration. Files ship defaults; once
// a key is seeded, the database is the source of truth. Every change is
// validated, versioned (history), audited and applied live.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"sigs.k8s.io/yaml"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// Def describes one setting.
type Def struct {
	Key         string
	Title       string
	Description string
	// Default returns the shipped default (usually a file under configs/).
	Default func() ([]byte, error)
	// Validate checks a value and returns its normalized JSON.
	Validate func(ctx context.Context, value json.RawMessage) (json.RawMessage, error)
	// Apply makes a (valid) value take effect immediately.
	Apply func(ctx context.Context, value json.RawMessage) error
}

// FileDefault reads a YAML or JSON file as the default value.
func FileDefault(path string) func() ([]byte, error) {
	return func() ([]byte, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read default %s: %w", path, err)
		}
		return yaml.YAMLToJSON(b)
	}
}

// Item is a setting with its current value and default.
type Item struct {
	Def       Def
	Row       dbgen.AppSetting
	Default   json.RawMessage
	DefaultOK bool
}

type Service struct {
	db   *db.DB
	log  *slog.Logger
	mu   sync.RWMutex
	defs map[string]Def
	vals map[string]json.RawMessage
}

func NewService(d *db.DB, log *slog.Logger) *Service {
	return &Service{db: d, log: log, defs: map[string]Def{}, vals: map[string]json.RawMessage{}}
}

// Register adds a setting definition (before Start).
func (s *Service) Register(d Def) { s.defs[d.Key] = d }

// Start seeds missing keys from their defaults and applies every value. A
// stored value that no longer validates (e.g. after an upgrade) is kept in the
// database but replaced by the default in memory, with a warning.
func (s *Service) Start(ctx context.Context) error {
	for _, key := range s.keys() {
		d := s.defs[key]
		row, err := s.db.GetSetting(ctx, key)
		if db.IsNotFound(err) {
			def, err := d.Default()
			if err != nil {
				return err
			}
			norm, err := d.Validate(ctx, def)
			if err != nil {
				return fmt.Errorf("default for %s is invalid: %w", key, err)
			}
			if err := s.db.InsertSetting(ctx, dbgen.InsertSettingParams{Key: key, Value: norm}); err != nil {
				return err
			}
			s.log.Info("setting seeded from default", "key", key)
			if row, err = s.db.GetSetting(ctx, key); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		val := row.Value
		if _, err := d.Validate(ctx, val); err != nil {
			s.log.Warn("stored setting is invalid; using the default until it is fixed in Settings", "key", key, "err", err)
			def, derr := d.Default()
			if derr != nil {
				return derr
			}
			if val, err = d.Validate(ctx, def); err != nil {
				return err
			}
		}
		if err := d.Apply(ctx, val); err != nil {
			return fmt.Errorf("apply %s: %w", key, err)
		}
		s.mu.Lock()
		s.vals[key] = val
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) keys() []string {
	keys := make([]string, 0, len(s.defs))
	for k := range s.defs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Value returns a setting's current (applied) value.
func (s *Service) Value(key string) json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vals[key]
}

// List returns every setting with its default.
func (s *Service) List(ctx context.Context) ([]Item, error) {
	rows, err := s.db.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	byKey := map[string]dbgen.AppSetting{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	var out []Item
	for _, key := range s.keys() {
		d := s.defs[key]
		it := Item{Def: d, Row: byKey[key]}
		if raw, err := d.Default(); err == nil {
			if norm, err := d.Validate(ctx, raw); err == nil {
				it.Default, it.DefaultOK = norm, true
			}
		}
		out = append(out, it)
	}
	return out, nil
}

func (s *Service) def(key string) (Def, error) {
	d, ok := s.defs[key]
	if !ok {
		return d, apperr.NotFound("unknown setting")
	}
	return d, nil
}

// Check validates a value for a setting without storing it (normalized JSON).
func (s *Service) Check(ctx context.Context, key string, value json.RawMessage) (json.RawMessage, error) {
	d, err := s.def(key)
	if err != nil {
		return nil, err
	}
	if d.Validate == nil {
		return value, nil
	}
	return d.Validate(ctx, value)
}

// Update validates and stores a new value; version must be the one the editor
// loaded (optimistic locking), so concurrent edits are not silently lost.
func (s *Service) Update(ctx context.Context, key string, value json.RawMessage, version int32, userID *uuid.UUID) (dbgen.AppSetting, error) {
	return s.write(ctx, key, value, &version, userID, "setting.update")
}

// Reset restores the shipped default.
func (s *Service) Reset(ctx context.Context, key string, userID *uuid.UUID) (dbgen.AppSetting, error) {
	d, err := s.def(key)
	if err != nil {
		return dbgen.AppSetting{}, err
	}
	raw, err := d.Default()
	if err != nil {
		return dbgen.AppSetting{}, err
	}
	return s.write(ctx, key, raw, nil, userID, "setting.reset")
}

func (s *Service) write(ctx context.Context, key string, value json.RawMessage, version *int32, userID *uuid.UUID, action string) (dbgen.AppSetting, error) {
	d, err := s.def(key)
	if err != nil {
		return dbgen.AppSetting{}, err
	}
	norm, err := d.Validate(ctx, value)
	if err != nil {
		if _, ok := apperr.As(err); ok {
			return dbgen.AppSetting{}, err
		}
		return dbgen.AppSetting{}, apperr.Validation(apperr.FieldError{Field: "value", Message: err.Error()})
	}
	var row dbgen.AppSetting
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockSetting(ctx, key)
		if err != nil {
			return err
		}
		if version != nil && *version != cur.Version {
			return apperr.Conflict(fmt.Sprintf("this setting changed since you loaded it (now version %d); reload and apply your change again", cur.Version))
		}
		if err := q.InsertSettingHistory(ctx, dbgen.InsertSettingHistoryParams{Key: key, Version: cur.Version, Value: cur.Value, UpdatedBy: cur.UpdatedBy}); err != nil {
			return err
		}
		if row, err = q.UpdateSetting(ctx, dbgen.UpdateSettingParams{Key: key, Value: norm, UpdatedBy: userID}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: action, EntityType: "setting", Payload: map[string]any{"key": key, "version": row.Version}})
	})
	if err != nil {
		return row, err
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := d.Apply(actx, norm); err != nil {
		// Validation passed, so this is unexpected; the value stays stored and is
		// applied on the next start.
		s.log.Error("apply setting", "key", key, "err", err)
		return row, fmt.Errorf("saved, but applying it failed: %w", err)
	}
	s.mu.Lock()
	s.vals[key] = norm
	s.mu.Unlock()
	return row, nil
}

// History lists earlier versions (newest first).
func (s *Service) History(ctx context.Context, key string) ([]dbgen.AppSettingsHistory, error) {
	if _, err := s.def(key); err != nil {
		return nil, err
	}
	return s.db.ListSettingHistory(ctx, key)
}
