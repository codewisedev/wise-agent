package httpapi

import (
	"context"

	"wise/apps/api/internal/httpapi/apigen"
)

func (s *Server) GetCosts(ctx context.Context, req apigen.GetCostsRequestObject) (apigen.GetCostsResponseObject, error) {
	w, g := "", ""
	if req.Params.Window != nil {
		w = string(*req.Params.Window)
	}
	if req.Params.GroupBy != nil {
		g = string(*req.Params.GroupBy)
	}
	rep, err := s.Costs.Report(ctx, w, g)
	if err != nil {
		return nil, err
	}
	return reshape[apigen.GetCosts200JSONResponse](rep), nil
}
