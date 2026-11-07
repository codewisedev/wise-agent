package httpapi

import (
	"context"

	"wise/apps/api/internal/accounts"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
)

func toAccount(a dbgen.Account) apigen.Account {
	return apigen.Account{
		Id: a.ID, Name: a.Name, Kind: apigen.AccountKind(a.Kind), BaseUrl: a.BaseUrl, DefaultModel: a.DefaultModel,
		Cloud: a.Cloud, Enabled: a.Enabled, HasSecret: a.SecretCiphertext != nil, SecretHint: a.SecretHint,
		Metadata: jsonObject(a.Metadata), CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func (s *Server) ListAccounts(ctx context.Context, _ apigen.ListAccountsRequestObject) (apigen.ListAccountsResponseObject, error) {
	rows, err := s.Accounts.List(ctx)
	if err != nil {
		return nil, err
	}
	builtin := []string{}
	if s.Gateway != nil {
		builtin = s.Gateway.ProviderNames()
	}
	return apigen.ListAccounts200JSONResponse{
		Items: mapSlice(rows, toAccount), SecretsEnabled: s.Accounts.SecretsEnabled(), BuiltinProviders: builtin,
	}, nil
}

func (s *Server) CreateAccount(ctx context.Context, req apigen.CreateAccountRequestObject) (apigen.CreateAccountResponseObject, error) {
	p, err := requireSession(ctx, "adding an account")
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := accounts.Input{
		Name: &b.Name, Kind: string(b.Kind), BaseURL: b.BaseUrl, DefaultModel: b.DefaultModel,
		Cloud: b.Cloud, Enabled: b.Enabled, Secret: b.Secret,
	}
	if b.Metadata != nil {
		in.Metadata = *b.Metadata
	}
	userID := p.User.ID
	a, err := s.Accounts.Create(ctx, in, &userID)
	if err != nil {
		return nil, err
	}
	return apigen.CreateAccount201JSONResponse(toAccount(a)), nil
}

func (s *Server) UpdateAccount(ctx context.Context, req apigen.UpdateAccountRequestObject) (apigen.UpdateAccountResponseObject, error) {
	if _, err := requireSession(ctx, "changing an account"); err != nil {
		return nil, err
	}
	b := req.Body
	in := accounts.Input{
		Name: b.Name, BaseURL: b.BaseUrl, DefaultModel: b.DefaultModel, Cloud: b.Cloud, Enabled: b.Enabled, Secret: b.Secret,
	}
	if b.Metadata != nil {
		in.Metadata = *b.Metadata
	}
	a, err := s.Accounts.Update(ctx, req.AccountId, in)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateAccount200JSONResponse(toAccount(a)), nil
}

func (s *Server) DeleteAccount(ctx context.Context, req apigen.DeleteAccountRequestObject) (apigen.DeleteAccountResponseObject, error) {
	if _, err := requireSession(ctx, "deleting an account"); err != nil {
		return nil, err
	}
	if err := s.Accounts.Delete(ctx, req.AccountId); err != nil {
		return nil, err
	}
	return apigen.DeleteAccount204Response{}, nil
}

func (s *Server) TestAccount(ctx context.Context, req apigen.TestAccountRequestObject) (apigen.TestAccountResponseObject, error) {
	if err := s.Accounts.Test(ctx, req.AccountId); err != nil {
		if _, nf := asNotFound(err); nf {
			return nil, err
		}
		msg := err.Error()
		return apigen.TestAccount200JSONResponse{Ok: false, Error: &msg}, nil
	}
	return apigen.TestAccount200JSONResponse{Ok: true}, nil
}
