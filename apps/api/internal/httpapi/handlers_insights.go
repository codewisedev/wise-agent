package httpapi

import (
	"context"

	"wise/apps/api/internal/httpapi/apigen"
)

func (s *Server) GetInsights(ctx context.Context, req apigen.GetInsightsRequestObject) (apigen.GetInsightsResponseObject, error) {
	w := ""
	if req.Params.Window != nil {
		w = string(*req.Params.Window)
	}
	rep, err := s.Insights.Report(ctx, w)
	if err != nil {
		return nil, err
	}
	return apigen.GetInsights200JSONResponse(reshape[apigen.InsightsReport](rep)), nil
}

func (s *Server) GetAgentInsights(ctx context.Context, req apigen.GetAgentInsightsRequestObject) (apigen.GetAgentInsightsResponseObject, error) {
	w := ""
	if req.Params.Window != nil {
		w = string(*req.Params.Window)
	}
	d, err := s.Insights.Agent(ctx, req.AgentName, w)
	if err != nil {
		return nil, err
	}
	return apigen.GetAgentInsights200JSONResponse(reshape[apigen.AgentInsights](d)), nil
}
