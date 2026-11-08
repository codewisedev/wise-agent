package httpapi

import (
	"context"
	"encoding/json"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/settings"
)

func rawJSON(b []byte) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func toSetting(def settings.Def, row dbgen.AppSetting, dflt json.RawMessage) apigen.Setting {
	out := apigen.Setting{
		Key: def.Key, Title: def.Title, Description: def.Description, Value: rawJSON(row.Value),
		Version: int(row.Version), UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy,
	}
	if dflt != nil {
		out.Default = rawJSON(dflt)
	}
	return out
}

func (s *Server) ListSettings(ctx context.Context, _ apigen.ListSettingsRequestObject) (apigen.ListSettingsResponseObject, error) {
	items, err := s.Settings.List(ctx)
	if err != nil {
		return nil, err
	}
	out := apigen.ListSettings200JSONResponse{Items: []apigen.Setting{}}
	for _, it := range items {
		out.Items = append(out.Items, toSetting(it.Def, it.Row, it.Default))
	}
	return out, nil
}

func (s *Server) settingItem(ctx context.Context, key string) (apigen.Setting, error) {
	items, err := s.Settings.List(ctx)
	if err != nil {
		return apigen.Setting{}, err
	}
	for _, it := range items {
		if it.Def.Key == key {
			return toSetting(it.Def, it.Row, it.Default), nil
		}
	}
	return apigen.Setting{}, nil
}

func (s *Server) UpdateSetting(ctx context.Context, req apigen.UpdateSettingRequestObject) (apigen.UpdateSettingResponseObject, error) {
	p, err := requireSession(ctx, "changing settings")
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req.Body.Value)
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	if _, err := s.Settings.Update(ctx, req.SettingKey, raw, int32(req.Body.Version), &userID); err != nil {
		return nil, err
	}
	it, err := s.settingItem(ctx, req.SettingKey)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateSetting200JSONResponse(it), nil
}

func (s *Server) ResetSetting(ctx context.Context, req apigen.ResetSettingRequestObject) (apigen.ResetSettingResponseObject, error) {
	p, err := requireSession(ctx, "changing settings")
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	if _, err := s.Settings.Reset(ctx, req.SettingKey, &userID); err != nil {
		return nil, err
	}
	it, err := s.settingItem(ctx, req.SettingKey)
	if err != nil {
		return nil, err
	}
	return apigen.ResetSetting200JSONResponse(it), nil
}

func (s *Server) ListSettingHistory(ctx context.Context, req apigen.ListSettingHistoryRequestObject) (apigen.ListSettingHistoryResponseObject, error) {
	rows, err := s.Settings.History(ctx, req.SettingKey)
	if err != nil {
		return nil, err
	}
	out := apigen.ListSettingHistory200JSONResponse{}
	out.Items = make([]struct {
		CreatedAt time.Time           `json:"createdAt"`
		UpdatedBy *openapi_types.UUID `json:"updatedBy,omitempty"`
		Value     interface{}         `json:"value"`
		Version   int                 `json:"version"`
	}, 0, len(rows))
	for _, r := range rows {
		out.Items = append(out.Items, struct {
			CreatedAt time.Time           `json:"createdAt"`
			UpdatedBy *openapi_types.UUID `json:"updatedBy,omitempty"`
			Value     interface{}         `json:"value"`
			Version   int                 `json:"version"`
		}{CreatedAt: r.CreatedAt, UpdatedBy: r.UpdatedBy, Value: rawJSON(r.Value), Version: int(r.Version)})
	}
	return out, nil
}
