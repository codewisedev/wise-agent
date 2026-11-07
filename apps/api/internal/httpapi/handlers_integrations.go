package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/integrations"
)

func toIntegration(it dbgen.Integration) apigen.Integration {
	out := apigen.Integration{
		Id: it.ID, ProjectId: it.ProjectID, Kind: apigen.IntegrationKind(it.Kind), Name: it.Name, AccountId: it.AccountID,
		Config: jsonObject(it.Config), StatusMap: map[string]apigen.BoardColumn{}, DefaultTemplateId: it.DefaultTemplateID,
		AutoStart: it.AutoStart, IntervalMinutes: int(it.IntervalMinutes), Enabled: it.Enabled, LastSyncedAt: it.LastSyncedAt,
		LastError: it.LastError, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
	}
	_ = json.Unmarshal(it.StatusMap, &out.StatusMap)
	if len(it.LastResult) > 0 {
		r := reshape[apigen.SyncResult](json.RawMessage(it.LastResult))
		out.LastResult = &r
	}
	return out
}

func integrationInput(b *apigen.IntegrationInput) integrations.Input {
	in := integrations.Input{
		Name: b.Name, Kind: string(b.Kind), AccountID: b.AccountId, DefaultTemplateID: b.DefaultTemplateId,
		AutoStart: deref(b.AutoStart), Enabled: b.Enabled == nil || *b.Enabled,
	}
	if b.IntervalMinutes != nil {
		in.IntervalMinutes = *b.IntervalMinutes
	}
	if b.Config != nil {
		in.Config = *b.Config
	}
	if b.StatusMap != nil {
		in.StatusMap = map[string]string{}
		for k, v := range *b.StatusMap {
			in.StatusMap[k] = string(v)
		}
	}
	return in
}

func (s *Server) ListIntegrations(ctx context.Context, req apigen.ListIntegrationsRequestObject) (apigen.ListIntegrationsResponseObject, error) {
	rows, err := s.Integrations.List(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.ListIntegrations200JSONResponse{Items: mapSlice(rows, toIntegration)}, nil
}

func (s *Server) CreateIntegration(ctx context.Context, req apigen.CreateIntegrationRequestObject) (apigen.CreateIntegrationResponseObject, error) {
	p, err := requireSession(ctx, "adding a task source")
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	it, err := s.Integrations.Create(ctx, req.ProjectId, integrationInput(req.Body), &userID)
	if err != nil {
		return nil, err
	}
	return apigen.CreateIntegration201JSONResponse(toIntegration(it)), nil
}

func (s *Server) UpdateIntegration(ctx context.Context, req apigen.UpdateIntegrationRequestObject) (apigen.UpdateIntegrationResponseObject, error) {
	if _, err := requireSession(ctx, "changing a task source"); err != nil {
		return nil, err
	}
	it, err := s.Integrations.Update(ctx, req.IntegrationId, integrationInput(req.Body))
	if err != nil {
		return nil, err
	}
	return apigen.UpdateIntegration200JSONResponse(toIntegration(it)), nil
}

func (s *Server) DeleteIntegration(ctx context.Context, req apigen.DeleteIntegrationRequestObject) (apigen.DeleteIntegrationResponseObject, error) {
	if _, err := requireSession(ctx, "deleting a task source"); err != nil {
		return nil, err
	}
	if err := s.Integrations.Delete(ctx, req.IntegrationId); err != nil {
		return nil, err
	}
	return apigen.DeleteIntegration204Response{}, nil
}

func (s *Server) SyncIntegration(ctx context.Context, req apigen.SyncIntegrationRequestObject) (apigen.SyncIntegrationResponseObject, error) {
	res, err := s.Integrations.Sync(ctx, req.IntegrationId)
	if err != nil {
		if _, ok := apperr.As(err); ok {
			return nil, err
		}
		detail := err.Error()
		if errors.Is(err, integrations.ErrAuth) {
			detail += " — check the account's token in Settings → Accounts"
		}
		return nil, &apperr.Error{Status: http.StatusBadGateway, Title: http.StatusText(http.StatusBadGateway), Detail: detail}
	}
	return apigen.SyncIntegration200JSONResponse(reshape[apigen.SyncResult](res)), nil
}
