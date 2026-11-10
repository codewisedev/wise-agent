package improvement

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/settings"
)

// Schedule is the weekly analysis.
type Schedule struct {
	Enabled bool   `json:"enabled"`
	Weekday string `json:"weekday"`
	Hour    int    `json:"hour"`
}

// ModelChoice pins an account (and model) for a run; empty = the agent's routing.
type ModelChoice struct {
	Account string `json:"account"`
	Model   string `json:"model"`
}

// Settings is the "improvement" setting.
type Settings struct {
	Schedule     Schedule    `json:"schedule"`
	WindowDays   int         `json:"window_days"`
	MinRuns      int         `json:"min_runs"`
	MaxProposals int         `json:"max_proposals"`
	Analysis     ModelChoice `json:"analysis"`
}

var weekdays = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

// DefaultSettings applies until the setting loads.
func DefaultSettings() Settings {
	return Settings{Schedule: Schedule{Enabled: false, Weekday: "monday", Hour: 7}, WindowDays: 30, MinRuns: 3, MaxProposals: 5}
}

// ParseSettings validates and normalizes the setting.
func ParseSettings(raw json.RawMessage) (Settings, error) {
	st := DefaultSettings()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return st, apperr.Validation(apperr.FieldError{Field: "value", Message: err.Error()})
	}
	st.Schedule.Weekday = strings.ToLower(strings.TrimSpace(st.Schedule.Weekday))
	var errs []apperr.FieldError
	if !slices.Contains(weekdays, st.Schedule.Weekday) {
		errs = append(errs, apperr.FieldError{Field: "schedule.weekday", Message: "a day of the week (monday…sunday)"})
	}
	if st.Schedule.Hour < 0 || st.Schedule.Hour > 23 {
		errs = append(errs, apperr.FieldError{Field: "schedule.hour", Message: "0-23 (UTC)"})
	}
	if st.WindowDays < 1 || st.WindowDays > 365 {
		errs = append(errs, apperr.FieldError{Field: "window_days", Message: "1-365"})
	}
	if st.MinRuns < 1 || st.MinRuns > 1000 {
		errs = append(errs, apperr.FieldError{Field: "min_runs", Message: "1-1000"})
	}
	if st.MaxProposals < 1 || st.MaxProposals > 8 {
		errs = append(errs, apperr.FieldError{Field: "max_proposals", Message: "1-8"})
	}
	if st.Analysis.Model != "" && st.Analysis.Account == "" {
		errs = append(errs, apperr.FieldError{Field: "analysis.model", Message: "choose an account for the model"})
	}
	if len(errs) > 0 {
		return st, apperr.Validation(errs...)
	}
	return st, nil
}

// Due reports the period key when the weekly analysis is due at now (UTC), or "".
func (st Settings) Due(now time.Time) string {
	if !st.Schedule.Enabled {
		return ""
	}
	now = now.UTC()
	day := slices.Index(weekdays, st.Schedule.Weekday)
	// The most recent scheduled moment at or before now.
	back := (int(now.Weekday()) - day + 7) % 7
	at := time.Date(now.Year(), now.Month(), now.Day(), st.Schedule.Hour, 0, 0, 0, time.UTC).AddDate(0, 0, -back)
	if at.After(now) {
		at = at.AddDate(0, 0, -7)
	}
	// Catch up only within a day of the scheduled moment (a restart after a long outage does not stack analyses).
	if now.Sub(at) > 24*time.Hour {
		return ""
	}
	y, w := at.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// RegisterSetting declares the "improvement" setting.
func RegisterSetting(s *settings.Service, file string, svc *Service) {
	s.Register(settings.Def{
		Key: "improvement", Title: "Improvement",
		Description: "When Wise analyses its own performance (weekly schedule in UTC, or off), how far back it looks, how many runs an agent needs before its numbers count, how many proposals one analysis may make, and which account runs the analysis.",
		Default:     settings.FileDefault(file),
		Validate: func(_ context.Context, v json.RawMessage) (json.RawMessage, error) {
			st, err := ParseSettings(v)
			if err != nil {
				return nil, err
			}
			return json.Marshal(st)
		},
		Apply: func(_ context.Context, v json.RawMessage) error {
			st, err := ParseSettings(v)
			if err == nil {
				svc.SetSettings(st)
			}
			return err
		},
	})
}
