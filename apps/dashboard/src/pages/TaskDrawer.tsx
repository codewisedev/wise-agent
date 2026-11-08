import { useMutation, useQuery, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { ExternalLink, Play, StepForward, X } from "lucide-react";
import { useEffect, useRef } from "react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import { TaskDetailsForm } from "../components/TaskFields";
import { Badge, ErrorNote, relativeTime } from "../components/ui";
import { TaskStatus, stepName, workflowTone } from "./workflowUi";

const ACTIVE = ["pending", "running", "awaiting_approval", "awaiting_retry"];

/** Quick view of a card: edit the basics and run its workflow without leaving the board. */
export function TaskDrawer({ taskId, onClose, invalidate }: { taskId: string; onClose: () => void; invalidate: QueryKey[] }) {
  const qc = useQueryClient();
  const detail = useQuery({
    queryKey: ["task", taskId],
    queryFn: () => unwrap(api.GET("/tasks/{taskId}", { params: { path: { taskId } } })),
  });
  const t = detail.data?.task;
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
  const run = useMutation({
    mutationFn: (stepByStep: boolean) => unwrap(api.POST("/tasks/{taskId}/run", { params: { path: { taskId } }, body: { stepByStep } })),
    onSuccess: refresh,
  });
  const wf = detail.data?.workflow;
  const active = wf && ACTIVE.includes(wf.status);

  return (
    <>
      <div className="drawer-scrim" onClick={onClose} aria-hidden />
      <aside className="drawer" role="dialog" aria-modal="true" aria-label={t ? `Task: ${t.title}` : "Task"}>
        <header className="drawer-head">
          {t ? <TaskStatus status={t.status} /> : <span className="faint small">loading…</span>}
          {t?.archivedAt && <Badge tone="warn" dot={false}>archived</Badge>}
          <span className="spacer" />
          <Link to={`/tasks/${taskId}`} className="btn btn-ghost btn-sm"><ExternalLink size={13} aria-hidden /> Open</Link>
          <button ref={closeRef} type="button" className="btn btn-ghost btn-icon" aria-label="Close" onClick={onClose}><X size={15} aria-hidden /></button>
        </header>
        <ErrorNote error={detail.error} />
        {t && (
          <div className="drawer-body">
            <TaskDetailsForm task={t} compact onSaved={() => { for (const k of invalidate) void qc.invalidateQueries({ queryKey: k }); }} />

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
