package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
	out := apigen.AgentSummary{
		Id: a.Row.ID, Name: a.Def.Name, Version: a.Def.Version, Description: a.Def.Description,
		Enabled: a.Row.Enabled, Phase: a.Def.Phase, ModelAlias: a.Def.ModelConfig.Alias, Tools: tools,
		RunCount: a.RunCount, LastRunAt: a.LastRunAt,
	}
	if a.Def.Produces != "" {
		p := a.Def.Produces
		out.Produces = &p
	}
	if len(a.Def.Consumes) > 0 {
		c := a.Def.Consumes
		out.Consumes = &c
	}
	return out
}

func toAgentRun(r dbgen.AgentRun) apigen.AgentRun {
	out := apigen.AgentRun{
		Id: r.ID, AgentName: r.AgentName, AgentDefinitionId: r.AgentDefinitionID, ProjectId: r.ProjectID, WorkflowRunId: r.WorkflowRunID,
		Status: apigen.AgentRunStatus(r.Status), Input: jsonObject(r.Input), Error: r.Error,
		Attempt: int(r.Attempt), MaxAttempts: int(r.MaxAttempts), CancelRequested: r.CancelRequested,
		TotalInputTokens: r.TotalInputTokens, TotalOutputTokens: r.TotalOutputTokens, TotalCostUsd: r.TotalCostUsd,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	if len(r.Output) > 0 {
		o := jsonObject(r.Output)
		out.Output = &o
	}
	out.Context = runContext(r.Context)
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
	items := make([]apigen.AgentSummary, len(list))
	for i, a := range list {
		items[i] = s.withOrigin(ctx, a, toAgentSummary(a))
	}
	return apigen.ListAgents200JSONResponse{Items: items}, nil
}

func (s *Server) agentDetail(ctx context.Context, name string) (apigen.AgentDetail, error) {
	a, err := s.Agents.Get(ctx, name)
	if err != nil {
		return apigen.AgentDetail{}, err
	}
	sum := s.withOrigin(ctx, a, toAgentSummary(a))
	out := apigen.AgentDetail{
		Id: sum.Id, Name: sum.Name, Version: sum.Version, Description: sum.Description, Enabled: sum.Enabled,
		Phase: sum.Phase, ModelAlias: sum.ModelAlias, Tools: sum.Tools, RunCount: sum.RunCount, LastRunAt: sum.LastRunAt,
		Produces: sum.Produces, Consumes: sum.Consumes, UpdateAvailable: sum.UpdateAvailable,
		Definition: jsonObject(a.Row.Definition), Prompt: a.Row.Prompt, ContentHash: a.Row.ContentHash,
		CreatedAt: a.Row.CreatedAt, CloudAllowed: s.Gateway.CloudAllowed(a.Def.Name),
	}
	if sum.Source != nil {
		src := apigen.AgentDetailSource(*sum.Source)
		out.Source = &src
	}
	if o, err := s.Agents.Origin(ctx, a.Row); err == nil && o.Shipped != nil {
		p := o.Shipped.Prompt
		d := jsonObject(o.Shipped.Definition)
		out.ShippedPrompt, out.ShippedDefinition = &p, &d
	}
	return out, nil
}

// withOrigin adds where the agent's current definition comes from.
func (s *Server) withOrigin(ctx context.Context, a agents.Summary, sum apigen.AgentSummary) apigen.AgentSummary {
	o, err := s.Agents.Origin(ctx, a.Row)
	if err != nil {
		return sum
	}
	src := apigen.AgentSummarySource(o.Source)
	upd := o.UpdateAvailable
	sum.Source, sum.UpdateAvailable = &src, &upd
	return sum
}

func (s *Server) GetAgent(ctx context.Context, req apigen.GetAgentRequestObject) (apigen.GetAgentResponseObject, error) {
	d, err := s.agentDetail(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	return apigen.GetAgent200JSONResponse(d), nil
}

func agentInputRaw(b *apigen.AgentInput) ([]byte, string, error) {
	raw, err := json.Marshal(b.Definition)
	return raw, b.Prompt, err
}

func (s *Server) CreateAgent(ctx context.Context, req apigen.CreateAgentRequestObject) (apigen.CreateAgentResponseObject, error) {
	p, err := requireSession(ctx, "creating an agent")
	if err != nil {
		return nil, err
	}
	raw, prompt, err := agentInputRaw(req.Body)
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	row, err := s.Agents.Create(ctx, raw, prompt, &userID)
	if err != nil {
		return nil, err
	}
	d, err := s.agentDetail(ctx, row.Name)
	if err != nil {
		return nil, err
	}
	return apigen.CreateAgent201JSONResponse(d), nil
}

func (s *Server) UpdateAgent(ctx context.Context, req apigen.UpdateAgentRequestObject) (apigen.UpdateAgentResponseObject, error) {
	p, err := requireSession(ctx, "editing an agent")
	if err != nil {
		return nil, err
	}
	raw, prompt, err := agentInputRaw(req.Body)
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	if _, err := s.Agents.Save(ctx, req.AgentName, raw, prompt, &userID); err != nil {
		return nil, err
	}
	d, err := s.agentDetail(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateAgent200JSONResponse(d), nil
}

func (s *Server) DuplicateAgent(ctx context.Context, req apigen.DuplicateAgentRequestObject) (apigen.DuplicateAgentResponseObject, error) {
	p, err := requireSession(ctx, "creating an agent")
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	row, err := s.Agents.Duplicate(ctx, req.AgentName, req.Body.Name, &userID)
	if err != nil {
		return nil, err
	}
	d, err := s.agentDetail(ctx, row.Name)
	if err != nil {
		return nil, err
	}
	return apigen.DuplicateAgent201JSONResponse(d), nil
}

func (s *Server) ResetAgent(ctx context.Context, req apigen.ResetAgentRequestObject) (apigen.ResetAgentResponseObject, error) {
	if _, err := requireSession(ctx, "resetting an agent"); err != nil {
		return nil, err
	}
	if _, err := s.Agents.Reset(ctx, req.AgentName); err != nil {
		return nil, err
	}
	d, err := s.agentDetail(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	return apigen.ResetAgent200JSONResponse(d), nil
}

func (s *Server) DeleteAgent(ctx context.Context, req apigen.DeleteAgentRequestObject) (apigen.DeleteAgentResponseObject, error) {
	if _, err := requireSession(ctx, "deleting an agent"); err != nil {
		return nil, err
	}
	using, err := s.Tasks.TemplatesUsingAgent(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	if len(using) > 0 {
		return nil, apperr.Conflict(fmt.Sprintf("used by workflow templates: %s — remove it from them first", strings.Join(using, ", ")))
	}
	if err := s.Agents.Archive(ctx, req.AgentName); err != nil {
		return nil, err
	}
	return apigen.DeleteAgent204Response{}, nil
}

func (s *Server) ListAgentVersions(ctx context.Context, req apigen.ListAgentVersionsRequestObject) (apigen.ListAgentVersionsResponseObject, error) {
	rows, err := s.Agents.Versions(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.AgentVersion, len(rows))
	for i, r := range rows {
		items[i] = apigen.AgentVersion{Id: r.ID, Version: r.Version, ContentHash: r.ContentHash,
			Source: apigen.AgentVersionSource(r.Source), Current: r.IsCurrent, CreatedAt: r.CreatedAt}
	}
	return apigen.ListAgentVersions200JSONResponse{Items: items}, nil
}

func (s *Server) RestoreAgentVersion(ctx context.Context, req apigen.RestoreAgentVersionRequestObject) (apigen.RestoreAgentVersionResponseObject, error) {
	if _, err := requireSession(ctx, "restoring an agent version"); err != nil {
		return nil, err
	}
	if _, err := s.Agents.Restore(ctx, req.AgentName, req.VersionId); err != nil {
		return nil, err
	}
	d, err := s.agentDetail(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	return apigen.RestoreAgentVersion200JSONResponse(d), nil
}

func (s *Server) ListTools(ctx context.Context, _ apigen.ListToolsRequestObject) (apigen.ListToolsResponseObject, error) {
	var items []apigen.ToolDefinition
	for _, t := range s.Agents.Tools() {
		items = append(items, toToolDefinition(t))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if items == nil {
		items = []apigen.ToolDefinition{}
	}
	return apigen.ListTools200JSONResponse{Items: items}, nil
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
	opts := agents.RunOptions{ProjectID: req.Body.ProjectId, UserID: &userID}
	if req.Body.Account != nil && *req.Body.Account != "" {
		if err := s.Accounts.ValidateModelChoice(ctx, *req.Body.Account); err != nil {
			return nil, err
		}
		opts.Model = &agents.ModelOverride{Account: *req.Body.Account, Model: deref(req.Body.Model)}
	}
	run, err := s.Agents.Enqueue(ctx, req.AgentName, req.Body.Input, opts)
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
	hasWS := false
	if s.Workspaces != nil {
		if w, err := s.Workspaces.ForRun(ctx, c.Run); err == nil && w != nil && (w.Status == "ready" || w.Status == "stopped") {
			hasWS = true
		}
	}
	return apigen.RuntimeClaimRun200JSONResponse{
		Run: toAgentRun(c.Run), RunToken: c.Token, LeaseSeconds: s.RunQueue.LeaseSeconds(),
		Definition: jsonObject(c.Definition), Prompt: c.Prompt, Tools: mapSlice(c.Tools, toToolDefinition),
		Project: toRuntimeProject(c.Project), Workspace: &hasWS, Runtime: ptrTo(agents.RunRuntime(c.Run)),
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

func ptrTo[T any](v T) *T { return &v }
