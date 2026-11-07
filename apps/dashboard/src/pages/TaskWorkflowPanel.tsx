import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, Play, Save, StepForward } from "lucide-react";
import { useEffect, useState } from "react";
import { api, unwrap, type Schemas } from "../api/client";
import { AccountSelect } from "../components/AccountSelect";
import { ErrorNote, Panel } from "../components/ui";

type Override = Schemas["StepOverride"];

/**
 * A task's workflow setup: which template, and per step whether it runs, which
 * account runs it and whether it pauses for review. Run starts it as configured;
 * "Run step by step" pauses after every step.
 */
export function TaskWorkflowPanel({ taskId, canRun }: { taskId: string; canRun: boolean }) {
  const qc = useQueryClient();
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
  const change = (id: string, patch: Override) => {
    setSteps((s) => ({ ...s, [id]: { ...s[id], ...patch } }));
    setDirty(true);
  };

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
      <ol className="wf-steps">
        {tmpl.definition.steps.map((st, i) => {
          const o = steps[st.id] ?? {};
          const enabled = o.enabled ?? st.enabled ?? true;
          const review = o.review ?? st.review ?? false;
          const account = o.account ?? st.account ?? "";
          const problems = issuesFor(st.id);
          return (
            <li key={st.id} className={`${enabled ? "" : "is-dim"} ${problems.length ? "has-issue" : ""}`}>
              <span className="wf-num mono">{i + 1}</span>
              <label className="checkbox">
                <input type="checkbox" checked={enabled} onChange={(e) => change(st.id, { enabled: e.target.checked })} />
                <span>{st.name || st.id}</span>
              </label>
              <span className="mono small faint">{st.agent}</span>
              <span className="spacer" />
              <label className="checkbox small" title="Pause after this step for your review">
                <input type="checkbox" checked={review} disabled={!enabled} onChange={(e) => change(st.id, { review: e.target.checked })} />
                <Eye size={13} aria-hidden /> review
              </label>
              <AccountSelect value={account} onChange={(v) => change(st.id, { account: v })} defaultLabel="default routing" />
              {problems.map((p) => <p key={p.message} className="wf-issue small">{p.message}</p>)}
            </li>
          );
        })}
      </ol>
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
