import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Play, Save, StepForward } from "lucide-react";
import { useEffect, useState } from "react";
import { api, unwrap, type Schemas } from "../api/client";
import { StepInspector, WorkflowFlow, viewOf, type StepView } from "../components/WorkflowFlow";
import { ErrorNote, Panel } from "../components/ui";

type Override = Schemas["StepOverride"];

/**
 * A task's workflow setup: which template, and per step whether it runs, which
 * account runs it and whether it pauses for review. Run starts it as configured;
 * "Run step by step" pauses after every step.
 */
export function TaskWorkflowPanel({ taskId, canRun, task }: { taskId: string; canRun: boolean; task: Schemas["Task"] }) {
  const qc = useQueryClient();
  const agents = useQuery({ queryKey: ["agents"], queryFn: () => unwrap(api.GET("/agents")) });
  const [selected, setSelected] = useState<string | undefined>();
  const setup = useQuery({
    queryKey: ["task", taskId, "workflow"],
    queryFn: () => unwrap(api.GET("/tasks/{taskId}/workflow", { params: { path: { taskId } } })),
  });
  const templates = useQuery({ queryKey: ["workflow-templates"], queryFn: () => unwrap(api.GET("/workflow-templates")) });
  const [templateId, setTemplateId] = useState<string>("");
  const [steps, setSteps] = useState<Record<string, Override>>({});
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (setup.data && !dirty) {
      setTemplateId(setup.data.template.id);
      setSteps(setup.data.steps);
    }
  }, [setup.data, dirty]);

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ["task", taskId] });
  };
  const save = useMutation({
    mutationFn: () =>
      unwrap(api.PUT("/tasks/{taskId}/workflow", { params: { path: { taskId } }, body: { templateId: templateId || null, steps } })),
    onSuccess: () => {
      setDirty(false);
      invalidate();
    },
  });
  const run = useMutation({
    mutationFn: async (stepByStep: boolean) => {
      if (dirty) await save.mutateAsync();
      return unwrap(api.POST("/tasks/{taskId}/run", { params: { path: { taskId } }, body: { stepByStep } }));
    },
    onSuccess: invalidate,
  });

  if (setup.error) return <ErrorNote error={setup.error} />;
  if (!setup.data) return null;
  // Show the selected template's steps; switching template drops overrides.
  const tmpl = templates.data?.items.find((t) => t.id === templateId) ?? setup.data.template;
  const issuesFor = (id?: string) => setup.data.issues.filter((i) => (i.stepId ?? undefined) === id);
  const views: Record<string, StepView> = {};
  for (const st of tmpl.definition.steps) {
    const o = steps[st.id] ?? {};
    const base = viewOf(st);
    views[st.id] = {
      enabled: o.enabled ?? base.enabled, review: o.review ?? base.review,
      account: (o.account ?? base.account) || undefined, model: (o.model ?? base.model) || undefined,
    };
  }
  const selIdx = tmpl.definition.steps.findIndex((s) => s.id === selected);

  return (
    <Panel
      title="Workflow"
      actions={
        <select
          aria-label="Workflow template"
          value={templateId}
          onChange={(e) => {
            setTemplateId(e.target.value);
            setSteps({});
            setDirty(true);
          }}
        >
          {templates.data?.items.map((t) => (
            <option key={t.id} value={t.id}>{t.name}{t.isDefault ? " (default)" : ""}</option>
          ))}
        </select>
      }
    >
      <div className="flow-editor">
        <WorkflowFlow
          def={tmpl.definition} views={views} issues={setup.data.issues} agents={agents.data?.items ?? []}
          selected={selected} onSelect={setSelected}
          sourceLabel={task.source && task.source !== "manual" ? `imported from ${task.source}` : "this task"}
        />
        {selIdx >= 0 ? (
          <StepInspector
            def={tmpl.definition} index={selIdx} agents={agents.data?.items ?? []} mode="task"
            view={views[selected!]}
            onViewChange={(v) => {
              const st = tmpl.definition.steps[selIdx]!;
              const base = viewOf(st);
              const o: Override = {};
              if (v.enabled !== base.enabled) o.enabled = v.enabled;
              if ((v.account ?? "") !== (base.account ?? "")) o.account = v.account ?? "";
              if ((v.model ?? "") !== (base.model ?? "")) o.model = v.model ?? "";
              if (v.review !== base.review) o.review = v.review;
              setSteps((cur) => ({ ...cur, [st.id]: o }));
              setDirty(true);
            }}
          />
        ) : (
          <aside className="inspector inspector-hint">
            <p className="small faint">Select a step to switch it off, pick its account or add a review — for this task only.</p>
          </aside>
        )}
      </div>
      {issuesFor(undefined).map((p) => <p key={p.message} className="wf-issue small panel-body">{p.message}</p>)}
      {canRun && (
        <div className="actions panel-body">
          <button className="btn btn-primary" onClick={() => run.mutate(false)} disabled={run.isPending}>
            <Play size={15} aria-hidden /> Run
          </button>
          <button className="btn" onClick={() => run.mutate(true)} disabled={run.isPending} title="Pause after every step so you pass each result on yourself">
            <StepForward size={15} aria-hidden /> Run step by step
          </button>
          {dirty && (
            <button className="btn btn-ghost" onClick={() => save.mutate()} disabled={save.isPending}>
              <Save size={15} aria-hidden /> Save setup
            </button>
          )}
        </div>
      )}
      <ErrorNote error={save.error ?? run.error} />
    </Panel>
  );
}
