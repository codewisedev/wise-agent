import { useMutation, useQuery, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { ExternalLink, Play, Save, StepForward, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, ErrorNote, Field, relativeTime } from "../components/ui";
import { TaskStatus, stepName, workflowTone } from "./workflowUi";

const PRIORITIES: Schemas["TaskPriority"][] = ["none", "low", "medium", "high", "urgent"];
const ACTIVE = ["pending", "running", "awaiting_approval", "awaiting_retry"];

/** Quick view of a card: edit the basics and run its workflow without leaving the board. */
export function TaskDrawer({ taskId, onClose, invalidate }: { taskId: string; onClose: () => void; invalidate: QueryKey[] }) {
  const qc = useQueryClient();
  const detail = useQuery({
    queryKey: ["task", taskId],
    queryFn: () => unwrap(api.GET("/tasks/{taskId}", { params: { path: { taskId } } })),
  });
  const t = detail.data?.task;
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState<Schemas["TaskPriority"]>("none");
  const [labels, setLabels] = useState("");
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    if (t && !dirty) {
      setTitle(t.title);
      setDescription(t.description);
      setPriority(t.priority ?? "none");
      setLabels((t.labels ?? []).join(", "));
    }
  }, [t, dirty]);
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    closeRef.current?.focus();
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["task", taskId] });
    for (const k of invalidate) void qc.invalidateQueries({ queryKey: k });
  };
  const save = useMutation({
    mutationFn: () => unwrap(api.PATCH("/tasks/{taskId}", {
      params: { path: { taskId } },
      body: { title: title.trim(), description, priority, labels: labels.split(",").map((l) => l.trim()).filter(Boolean) },
    })),
    onSuccess: (updated) => {
      // Show the saved values right away (the effect re-syncs from the cache once dirty is cleared).
      qc.setQueryData<typeof detail.data>(["task", taskId], (old) => (old ? { ...old, task: updated } : old));
      setDirty(false);
      refresh();
    },
  });
  const run = useMutation({
    mutationFn: (stepByStep: boolean) => unwrap(api.POST("/tasks/{taskId}/run", { params: { path: { taskId } }, body: { stepByStep } })),
    onSuccess: refresh,
  });
  const wf = detail.data?.workflow;
  const active = wf && ACTIVE.includes(wf.status);
  const edit = <T,>(set: (v: T) => void) => (v: T) => { set(v); setDirty(true); };

  return (
    <>
      <div className="drawer-scrim" onClick={onClose} aria-hidden />
      <aside className="drawer" role="dialog" aria-modal="true" aria-label={t ? `Task: ${t.title}` : "Task"}>
        <header className="drawer-head">
          {t ? <TaskStatus status={t.status} /> : <span className="faint small">loading…</span>}
          <span className="spacer" />
          <Link to={`/tasks/${taskId}`} className="btn btn-ghost btn-sm"><ExternalLink size={13} aria-hidden /> Open</Link>
          <button ref={closeRef} type="button" className="btn btn-ghost btn-icon" aria-label="Close" onClick={onClose}><X size={15} aria-hidden /></button>
        </header>
        <ErrorNote error={detail.error} />
        {t && (
          <div className="drawer-body">
            <Field label="Title" grow>
              <input value={title} onChange={(e) => edit(setTitle)(e.target.value)} minLength={3} maxLength={200} />
            </Field>
            <Field label="Description" grow>
              <textarea rows={7} value={description} onChange={(e) => edit(setDescription)(e.target.value)} maxLength={20000} />
            </Field>
            <div className="form-grid">
              <Field label="Priority">
                <select value={priority} onChange={(e) => edit(setPriority)(e.target.value as Schemas["TaskPriority"])}>
                  {PRIORITIES.map((p) => <option key={p} value={p}>{p}</option>)}
                </select>
              </Field>
              <Field label="Labels" hint="Comma-separated" grow>
                <input value={labels} onChange={(e) => edit(setLabels)(e.target.value)} />
              </Field>
            </div>
            <div className="actions">
              <button type="button" className="btn btn-primary btn-sm" disabled={!dirty || title.trim().length < 3 || save.isPending} onClick={() => save.mutate()}>
                <Save size={13} aria-hidden /> Save
              </button>
            </div>
            <ErrorNote error={save.error} />

            <section className="drawer-section" aria-label="Workflow">
              <h3>Workflow</h3>
              {wf ? (
                <p className="small">
                  <Badge tone={workflowTone[wf.status]}>{wf.status.replace(/_/g, " ")}</Badge>{" "}
                  {wf.currentStage && <span className="muted">{stepName(detail.data?.plan, wf.currentStage)} · </span>}
                  <span className="faint">{relativeTime(wf.updatedAt ?? wf.createdAt)}</span>
                </p>
              ) : (
                <p className="small faint">Not run yet{detail.data?.plan?.templateName ? ` · ${detail.data.plan.templateName}` : ""}.</p>
              )}
              {detail.data?.approvals.some((a) => a.status === "pending") && (
                <p className="small"><Link to={`/tasks/${taskId}`}>A review is waiting for you →</Link></p>
              )}
              {!active && (
                <div className="actions">
                  <button type="button" className="btn btn-sm" onClick={() => run.mutate(false)} disabled={run.isPending || (detail.data?.issues.length ?? 0) > 0}>
                    <Play size={13} aria-hidden /> Run workflow
                  </button>
                  <button type="button" className="btn btn-ghost btn-sm" onClick={() => run.mutate(true)} disabled={run.isPending || (detail.data?.issues.length ?? 0) > 0}>
                    <StepForward size={13} aria-hidden /> Step by step
                  </button>
                </div>
              )}
              {(detail.data?.issues.length ?? 0) > 0 && <p className="small error">Fix the workflow setup on the task page first.</p>}
              <ErrorNote error={run.error} />
            </section>
          </div>
        )}
      </aside>
    </>
  );
}
