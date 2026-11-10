package httpapi

import (
	"context"
	"encoding/json"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/coding"
	"wise/apps/api/internal/codingruns"
	"wise/apps/api/internal/httpapi/apigen"
)

func (s *Server) codingRuns() (*codingruns.Service, error) {
	if s.CodingRuns == nil {
		return nil, apperr.Unavailable("external coding runtimes are not configured")
	}
	return s.CodingRuns, nil
}

func (s *Server) GetProjectCodingRuntimes(ctx context.Context, req apigen.GetProjectCodingRuntimesRequestObject) (apigen.GetProjectCodingRuntimesResponseObject, error) {
	cr, err := s.codingRuns()
	if err != nil {
		return nil, err
	}
	p, err := cr.ProjectPolicy(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.GetProjectCodingRuntimes200JSONResponse(toCodingPolicy(p)), nil
}

func (s *Server) SetProjectCodingRuntimes(ctx context.Context, req apigen.SetProjectCodingRuntimesRequestObject) (apigen.SetProjectCodingRuntimesResponseObject, error) {
	if _, err := requireSession(ctx, "changing a project's coding runtimes"); err != nil {
		return nil, err
	}
	cr, err := s.codingRuns()
	if err != nil {
		return nil, err
	}
	in := codingruns.ProjectPolicy{AllowSubscription: req.Body.AllowSubscription}
	for _, r := range req.Body.Enabled {
		in.Enabled = append(in.Enabled, string(r))
	}
	p, err := cr.SetProjectPolicy(ctx, req.ProjectId, in)
	if err != nil {
		return nil, err
	}
	return apigen.SetProjectCodingRuntimes200JSONResponse(toCodingPolicy(p)), nil
}

func toCodingPolicy(p codingruns.ProjectPolicy) apigen.ProjectCodingRuntimes {
	out := apigen.ProjectCodingRuntimes{AllowSubscription: p.AllowSubscription, Enabled: []apigen.ProjectCodingRuntimesEnabled{}}
	for _, r := range p.Enabled {
		out.Enabled = append(out.Enabled, apigen.ProjectCodingRuntimesEnabled(r))
	}
	return out
}

func (s *Server) RuntimeExternalStart(ctx context.Context, _ apigen.RuntimeExternalStartRequestObject) (apigen.RuntimeExternalStartResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.codingRuns()
	if err != nil {
		return nil, err
	}
	st, err := cr.Start(ctx, run)
	if err != nil {
		return nil, err
	}
	return apigen.RuntimeExternalStart200JSONResponse{Runtime: st.Runtime, Mode: apigen.ExternalStartMode(st.Mode),
		ReducedIsolation: st.ReducedIsolation, CliVersion: st.CLIVersion}, nil
}

func (s *Server) RuntimeExternalPoll(ctx context.Context, req apigen.RuntimeExternalPollRequestObject) (apigen.RuntimeExternalPollResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.codingRuns()
	if err != nil {
		return nil, err
	}
	wait := 20
	if req.Params.Wait != nil {
		wait = *req.Params.Wait
	}
	p, err := cr.Poll(ctx, run, wait)
	if err != nil {
		return nil, err
	}
	out := apigen.RuntimeExternalPoll200JSONResponse{Done: p.Done, Events: []apigen.ExternalEvent{}}
	for _, e := range p.Events {
		raw, _ := json.Marshal(e)
		var ev apigen.ExternalEvent
		_ = json.Unmarshal(raw, &ev)
		out.Events = append(out.Events, ev)
	}
	if o := p.Outcome; o != nil {
		retry := coding.Retryable(o.Class)
		oc := apigen.ExternalOutcome{Class: apigen.ExternalOutcomeClass(o.Class), Retryable: &retry}
		if o.Message != "" {
			oc.Message = &o.Message
		}
		oc.Usage.InputTokens, oc.Usage.OutputTokens, oc.Usage.CostUsd = o.Usage.InputTokens, o.Usage.OutputTokens, float32(o.Usage.CostUSD)
		turns := o.Usage.Turns
		oc.Usage.Turns = &turns
		if len(o.Output) > 0 {
			var m map[string]any
			if json.Unmarshal(o.Output, &m) == nil {
				oc.Output = &m
			}
		}
		out.Outcome = &oc
	}
	return out, nil
}

func (s *Server) RuntimeExternalCancel(ctx context.Context, _ apigen.RuntimeExternalCancelRequestObject) (apigen.RuntimeExternalCancelResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.codingRuns()
	if err != nil {
		return nil, err
	}
	if err := cr.Cancel(ctx, run); err != nil {
		return nil, err
	}
	return apigen.RuntimeExternalCancel204Response{}, nil
}
