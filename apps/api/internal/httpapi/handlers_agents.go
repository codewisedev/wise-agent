package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/modelgateway"
	"wise/apps/api/internal/runqueue"
)

// ---- converters ----

func toAgentSummary(a agents.Summary) apigen.AgentSummary {
	tools := a.Def.Tools
	if tools == nil {
		tools = []string{}
	}
	return apigen.AgentSummary{
		Id: a.Row.ID, Name: a.Def.Name, Version: a.Def.Version, Description: a.Def.Description,
		Enabled: a.Row.Enabled, Phase: a.Def.Phase, ModelAlias: a.Def.ModelConfig.Alias, Tools: tools,
		RunCount: a.RunCount, LastRunAt: a.LastRunAt,
	}
}

func toAgentRun(r dbgen.AgentRun) apigen.AgentRun {
	out := apigen.AgentRun{
		Id: r.ID, AgentName: r.AgentName, AgentDefinitionId: r.AgentDefinitionID, ProjectId: r.ProjectID,
		Status: apigen.AgentRunStatus(r.Status), Input: jsonObject(r.Input), Error: r.Error,
		Attempt: int(r.Attempt), MaxAttempts: int(r.MaxAttempts), CancelRequested: r.CancelRequested,
		TotalInputTokens: r.TotalInputTokens, TotalOutputTokens: r.TotalOutputTokens, TotalCostUsd: r.TotalCostUsd,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	if len(r.Output) > 0 {
		o := jsonObject(r.Output)
		out.Output = &o
	}
	return out
}

func toAgentEvent(e dbgen.AgentEvent) apigen.AgentEvent {
	return apigen.AgentEvent{RunId: e.RunID, Seq: int(e.Seq), Type: e.Type, Payload: jsonObject(e.Payload), CreatedAt: e.CreatedAt}
}

func toModelCall(m dbgen.ModelCall) apigen.ModelCall {
	return apigen.ModelCall{
		Id: m.ID, Alias: m.Alias, Provider: m.Provider, Model: m.Model, Status: apigen.ModelCallStatus(m.Status),
		Error: m.Error, InputTokens: int(m.InputTokens), OutputTokens: int(m.OutputTokens), CostUsd: m.CostUsd,
		LatencyMs: int(m.LatencyMs), CacheHit: m.CacheHit, CreatedAt: m.CreatedAt,
	}
}

func toToolDefinition(t agents.Tool) apigen.ToolDefinition {
	perms := t.PermissionsRequired
	if perms == nil {
		perms = []string{}
	}
	return apigen.ToolDefinition{
		Name: t.Name, Description: t.Description, InputSchema: jsonObject(t.InputSchema),
		SideEffects: apigen.ToolDefinitionSideEffects(t.SideEffects), PermissionsRequired: perms,
	}
}

func toRuntimeProject(p *runqueue.ProjectContext) *apigen.RuntimeProject {
	if p == nil {
		return nil
	}
	repos := make([]apigen.RuntimeRepository, len(p.Repositories))
	for i, r := range p.Repositories {
		repos[i] = apigen.RuntimeRepository{
			Id: r.ID, Name: r.Name, Role: string(r.Role), SourceType: string(r.SourceType),
			LocalPath: r.LocalPath, RemoteUrl: r.RemoteUrl, CurrentBranch: r.CurrentBranch, HeadSha: r.HeadSha,
		}
	}
	return &apigen.RuntimeProject{
		Id: p.Project.ID, Name: p.Project.Name, Slug: p.Project.Slug, Description: p.Project.Description, Repositories: repos,
	}
}

// ---- agents (user-facing) ----

func (s *Server) ListAgents(ctx context.Context, _ apigen.ListAgentsRequestObject) (apigen.ListAgentsResponseObject, error) {
	list, err := s.Agents.List(ctx)
	if err != nil {
		return nil, err
	}
	return apigen.ListAgents200JSONResponse{Items: mapSlice(list, toAgentSummary)}, nil
}

func (s *Server) GetAgent(ctx context.Context, req apigen.GetAgentRequestObject) (apigen.GetAgentResponseObject, error) {
	a, err := s.Agents.Get(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	sum := toAgentSummary(a)
	return apigen.GetAgent200JSONResponse{
		Id: sum.Id, Name: sum.Name, Version: sum.Version, Description: sum.Description, Enabled: sum.Enabled,
		Phase: sum.Phase, ModelAlias: sum.ModelAlias, Tools: sum.Tools, RunCount: sum.RunCount, LastRunAt: sum.LastRunAt,
		Definition: jsonObject(a.Row.Definition), Prompt: a.Row.Prompt, ContentHash: a.Row.ContentHash,
		CreatedAt: a.Row.CreatedAt, CloudAllowed: s.Gateway.CloudAllowed(a.Def.Name),
	}, nil
}

func (s *Server) ListAgentRunsForAgent(ctx context.Context, req apigen.ListAgentRunsForAgentRequestObject) (apigen.ListAgentRunsForAgentResponseObject, error) {
	name := req.AgentName
	runs, err := s.Agents.ListRuns(ctx, &name, nil)
	if err != nil {
		return nil, err
	}
	return apigen.ListAgentRunsForAgent200JSONResponse{Items: mapSlice(runs, toAgentRun)}, nil
}

func (s *Server) CreateAgentRun(ctx context.Context, req apigen.CreateAgentRunRequestObject) (apigen.CreateAgentRunResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	run, err := s.Agents.CreateRun(ctx, req.AgentName, req.Body.Input, req.Body.ProjectId, &userID)
	if err != nil {
		return nil, err
	}
	return apigen.CreateAgentRun202JSONResponse(toAgentRun(run)), nil
}

func (s *Server) ListAgentRuns(ctx context.Context, req apigen.ListAgentRunsRequestObject) (apigen.ListAgentRunsResponseObject, error) {
	runs, err := s.Agents.ListRuns(ctx, nil, req.Params.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.ListAgentRuns200JSONResponse{Items: mapSlice(runs, toAgentRun)}, nil
}

func (s *Server) GetAgentRun(ctx context.Context, req apigen.GetAgentRunRequestObject) (apigen.GetAgentRunResponseObject, error) {
	run, err := s.Agents.GetRun(ctx, req.RunId)
	if err != nil {
		return nil, err
	}
	return apigen.GetAgentRun200JSONResponse(toAgentRun(run)), nil
}

func (s *Server) ListAgentRunEvents(ctx context.Context, req apigen.ListAgentRunEventsRequestObject) (apigen.ListAgentRunEventsResponseObject, error) {
	evs, err := s.Agents.ListEvents(ctx, req.RunId, int32(deref(req.Params.AfterSeq)))
	if err != nil {
		return nil, err
	}
	return apigen.ListAgentRunEvents200JSONResponse{Items: mapSlice(evs, toAgentEvent)}, nil
}

func (s *Server) ListAgentRunModelCalls(ctx context.Context, req apigen.ListAgentRunModelCallsRequestObject) (apigen.ListAgentRunModelCallsResponseObject, error) {
	calls, err := s.Agents.ListModelCalls(ctx, req.RunId)
	if err != nil {
		return nil, err
	}
	return apigen.ListAgentRunModelCalls200JSONResponse{Items: mapSlice(calls, toModelCall)}, nil
}

func (s *Server) CancelAgentRun(ctx context.Context, req apigen.CancelAgentRunRequestObject) (apigen.CancelAgentRunResponseObject, error) {
	run, err := s.Agents.CancelRun(ctx, req.RunId)
	if err != nil {
		return nil, err
	}
	return apigen.CancelAgentRun200JSONResponse(toAgentRun(run)), nil
}

func (s *Server) GetModelsOverview(ctx context.Context, req apigen.GetModelsOverviewRequestObject) (apigen.GetModelsOverviewResponseObject, error) {
	days := 7
	if req.Params.Days != nil {
		days = *req.Params.Days
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	usage, err := s.DB.ModelUsageSince(ctx, since)
	if err != nil {
		return nil, err
	}
	out := apigen.GetModelsOverview200JSONResponse{Since: since, Aliases: []apigen.ModelAlias{}, Providers: []apigen.ModelProvider{}, Usage: []apigen.ModelUsage{}}
	for _, a := range s.Gateway.Aliases() {
		targets := make([]apigen.ModelTarget, len(a.Targets))
		for i, t := range a.Targets {
			targets[i] = apigen.ModelTarget{Provider: t.Provider, Model: t.Model}
		}
		out.Aliases = append(out.Aliases, apigen.ModelAlias{Name: a.Name, Description: a.Description, Targets: targets})
	}
	for _, p := range s.Gateway.Health(ctx) {
		mp := apigen.ModelProvider{Name: p.Name, Type: p.Type, Cloud: p.Cloud, Healthy: p.Healthy}
		if p.Error != "" {
			e := p.Error
			mp.Error = &e
		}
		out.Providers = append(out.Providers, mp)
	}
	for _, u := range usage {
		out.Usage = append(out.Usage, apigen.ModelUsage{
			AgentName: u.AgentName, Alias: u.Alias, Provider: u.Provider, Model: u.Model, Calls: u.Calls, Errors: u.Errors,
			InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostUsd: u.CostUsd,
		})
	}
	return out, nil
}

// ---- runtime (per-run token) ----

func (s *Server) RuntimeClaimRun(ctx context.Context, req apigen.RuntimeClaimRunRequestObject) (apigen.RuntimeClaimRunResponseObject, error) {
	c, err := s.RunQueue.Claim(ctx, req.Body.RuntimeId)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return apigen.RuntimeClaimRun204Response{}, nil
	}
	return apigen.RuntimeClaimRun200JSONResponse{
		Run: toAgentRun(c.Run), RunToken: c.Token, LeaseSeconds: s.RunQueue.LeaseSeconds(),
		Definition: jsonObject(c.Definition), Prompt: c.Prompt, Tools: mapSlice(c.Tools, toToolDefinition),
		Project: toRuntimeProject(c.Project),
	}, nil
}

func (s *Server) RuntimeHeartbeat(ctx context.Context, _ apigen.RuntimeHeartbeatRequestObject) (apigen.RuntimeHeartbeatResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	cancel, err := s.RunQueue.Heartbeat(ctx, run)
	if err != nil {
		return nil, err
	}
	return apigen.RuntimeHeartbeat200JSONResponse{CancelRequested: cancel, LeaseSeconds: s.RunQueue.LeaseSeconds()}, nil
}

func (s *Server) RuntimeAppendEvents(ctx context.Context, req apigen.RuntimeAppendEventsRequestObject) (apigen.RuntimeAppendEventsResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	in := make([]runqueue.RuntimeEvent, len(req.Body.Events))
	for i, e := range req.Body.Events {
		in[i] = runqueue.RuntimeEvent{Type: string(e.Type), Payload: e.Payload}
	}
	if err := s.RunQueue.AppendEvents(ctx, run, in); err != nil {
		return nil, err
	}
	return apigen.RuntimeAppendEvents204Response{}, nil
}

func (s *Server) RuntimeModelTurn(ctx context.Context, req apigen.RuntimeModelTurnRequestObject) (apigen.RuntimeModelTurnResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	msgs := make([]modelgateway.Message, len(req.Body.Messages))
	for i, m := range req.Body.Messages {
		msgs[i] = modelgateway.Message{Role: string(m.Role), Content: m.Content, Name: deref(m.Name)}
	}
	t, err := s.RunQueue.ModelTurn(ctx, run, msgs)
	if err != nil {
		return nil, err
	}
	out := apigen.RuntimeModelTurn200JSONResponse{
		Action: apigen.ModelTurnAction(t.Envelope.Action), Alias: t.Alias, Provider: t.Provider, Model: t.Model,
		Usage:   apigen.TokenUsage{InputTokens: t.Usage.InputTokens, OutputTokens: t.Usage.OutputTokens},
		CostUsd: t.CostUSD, CacheHit: t.CacheHit, LatencyMs: t.LatencyMS,
	}
	if t.Envelope.Thought != "" {
		out.Thought = &t.Envelope.Thought
	}
	if t.ParseError != "" {
		out.ParseError, out.RawText = &t.ParseError, &t.RawText
	}
	if len(t.Envelope.ToolCalls) > 0 {
		calls := make([]apigen.TurnToolCall, len(t.Envelope.ToolCalls))
		for i, c := range t.Envelope.ToolCalls {
			calls[i] = apigen.TurnToolCall{Tool: c.Tool, Arguments: jsonObject(c.Arguments)}
		}
		out.ToolCalls = &calls
	}
	if len(t.Envelope.Final) > 0 {
		var final map[string]any
		if err := json.Unmarshal(t.Envelope.Final, &final); err == nil {
			out.Final = &final
		} else {
			// A non-object final cannot satisfy an object output schema; let the runtime re-prompt.
			msg := "final output must be a JSON object"
			out.Action, out.ParseError, out.RawText = apigen.ModelTurnAction(modelgateway.ActionInvalid), &msg, &t.RawText
		}
	}
	return out, nil
}

func (s *Server) RuntimeCompleteRun(ctx context.Context, req apigen.RuntimeCompleteRunRequestObject) (apigen.RuntimeCompleteRunResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.RunQueue.Complete(ctx, run, req.Body.Output); err != nil {
		return nil, err
	}
	return apigen.RuntimeCompleteRun204Response{}, nil
}

func (s *Server) RuntimeFailRun(ctx context.Context, req apigen.RuntimeFailRunRequestObject) (apigen.RuntimeFailRunResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.RunQueue.Fail(ctx, run, req.Body.Error, deref(req.Body.Retryable), deref(req.Body.Cancelled)); err != nil {
		return nil, err
	}
	return apigen.RuntimeFailRun204Response{}, nil
}

func runFrom(ctx context.Context) (dbgen.AgentRun, error) {
	run, ok := ctx.Value(runCtxKey{}).(dbgen.AgentRun)
	if !ok {
		return dbgen.AgentRun{}, apperr.Unauthorized("run token required")
	}
	return run, nil
}
