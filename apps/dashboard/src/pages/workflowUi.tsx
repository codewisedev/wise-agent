import { useMutation, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Check, CheckCircle2, Circle, CircleDot, CircleSlash, Hourglass, PauseCircle, Undo2, XCircle } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, ErrorNote, Field, label, relativeTime, type Tone } from "../components/ui";

export type Task = Schemas["Task"];
export type WorkflowRun = Schemas["WorkflowRun"];
export type WorkflowStep = Schemas["WorkflowStep"];
export type Approval = Schemas["Approval"];
export type Artifact = Schemas["Artifact"];

export const taskTone: Record<Schemas["TaskStatus"], Tone> = {
  open: "neutral",
  in_progress: "info",
  awaiting_approval: "warn",
  completed: "ok",
  failed: "bad",
  cancelled: "neutral",
};

export const workflowTone: Record<Schemas["WorkflowRunStatus"], Tone> = {
  pending: "info",
  running: "info",
  awaiting_approval: "warn",
  awaiting_retry: "bad",
  completed: "ok",
  failed: "bad",
  cancelled: "neutral",
};

const ACTIVE = new Set(["pending", "running", "awaiting_approval", "awaiting_retry"]);
export const isActiveWorkflow = (w?: WorkflowRun) => !!w && ACTIVE.has(w.status);

export function TaskStatus({ status }: { status: Schemas["TaskStatus"] }) {
  return <Badge tone={taskTone[status]} pulse={status === "in_progress"}>{label(status)}</Badge>;
}

export function WorkflowStatus({ wf }: { wf: WorkflowRun }) {
  return <Badge tone={workflowTone[wf.status]} pulse={wf.status === "running" || wf.status === "pending"}>{label(wf.status)}</Badge>;
}

export const stageName = (s: string) => label(s.toLowerCase());
export const GATE_STAGE: Record<string, string> = { scope: "SCOPE_APPROVAL", plan: "PLAN_APPROVAL" };
export const KIND_LABEL: Record<Schemas["ArtifactKind"], string> = {
  product_analysis: "Product analysis",
  requirements: "Requirements",
  plan: "Plan",
  architecture_review: "Architecture",
  final_report: "Report",
};

type StageState = "done" | "active" | "waiting" | "failed" | "skipped" | "cancelled" | "todo";

function stageState(stage: string, steps: WorkflowStep[], wf?: WorkflowRun): StageState {
  const mine = steps.filter((s) => s.stage === stage);
  const last = mine.at(-1);
  if (stage === "COMPLETED") return wf?.status === "completed" ? "done" : "todo";
  if (wf?.currentStage === stage) {
    if (wf.status === "awaiting_approval") return "waiting";
    if (wf.status === "awaiting_retry" || wf.status === "failed") return "failed";
    if (wf.status === "cancelled") return "cancelled";
    if (wf.status !== "completed") return "active";
  }
  if (!last) return "todo";
  switch (last.status) {
    case "succeeded":
      return "done";
    case "skipped":
      return "skipped";
    case "cancelled":
      return "cancelled";
    case "failed":
      return "failed";
    default:
      return "active";
  }
}

const STAGE_ICON: Record<StageState, typeof Circle> = {
  done: CheckCircle2,
  active: CircleDot,
  waiting: PauseCircle,
  failed: XCircle,
  skipped: Undo2,
  cancelled: CircleSlash,
  todo: Circle,
};

/** The pipeline as a strip of stages with their live state. */
export function Pipeline({ stages, steps, wf }: { stages: string[]; steps: WorkflowStep[]; wf?: WorkflowRun }) {
  return (
    <ol className="pipeline" aria-label="Workflow stages">
      {stages.map((stage) => {
        const st = stageState(stage, steps, wf);
        const Icon = STAGE_ICON[st];
        const runs = steps.filter((s) => s.stage === stage).length;
        return (
          <li key={stage} className={`stage stage-${st}`} aria-current={st === "active" || st === "waiting" ? "step" : undefined}>
            <Icon size={15} aria-hidden />
            <span className="stage-name">{stageName(stage)}</span>
            {runs > 1 && <span className="stage-runs" title={`${runs} executions`}>×{runs}</span>}
            <span className="sr-only">{st}</span>
          </li>
        );
      })}
    </ol>
  );
}

/**
 * A pending approval: what is pinned, any notes, and the decision form. The
 * reviewed combinedHash is sent back so the server refuses stale decisions.
 */
export function ApprovalCard({
  approval,
  onShowArtifact,
}: {
  approval: Approval;
  onShowArtifact?: (kind: Schemas["ArtifactKind"]) => void;
}) {
  const qc = useQueryClient();
  const [comment, setComment] = useState("");
  const decide = useMutation({
    mutationFn: (approve: boolean) => {
      const params = { params: { path: { approvalId: approval.id } } };
      const body = { combinedHash: approval.combinedHash, comment: comment.trim() || undefined };
      return unwrap(
        approve
          ? api.POST("/approvals/{approvalId}/approve", { ...params, body })
          : api.POST("/approvals/{approvalId}/reject", { ...params, body }),
      );
    },
    onSuccess: () => {
      setComment("");
      void qc.invalidateQueries({ queryKey: ["task", approval.taskId] });
      void qc.invalidateQueries({ queryKey: ["approvals"] });
    },
    onError: () => void qc.invalidateQueries({ queryKey: ["task", approval.taskId] }),
  });
  const canReject = comment.trim().length >= 3;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    decide.mutate(true);
  };

  return (
    <section className="approval" aria-label={`${approval.gate} approval`}>
      <header>
        <Hourglass size={16} aria-hidden />
        <h2>
          {approval.gate === "scope" ? "Approve scope" : "Approve plan"}
          <span className="faint small"> · round {approval.round} · requested {relativeTime(approval.createdAt)}</span>
        </h2>
      </header>
      <p className="small muted">
        You are approving exactly these versions. If any of them changes, this approval is invalidated and a new one is requested.
      </p>
      <ul className="pinned">
        {approval.artifacts.map((a) => (
          <li key={a.artifactId}>
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => onShowArtifact?.(a.kind)}>
              {KIND_LABEL[a.kind]} <span className="mono">v{a.version}</span>
            </button>
            <span className="mono small faint" title={a.hash}>#{a.hash.slice(0, 10)}</span>
          </li>
        ))}
      </ul>
      {approval.notes.length > 0 && (
        <ul className="notes">
          {approval.notes.map((n) => (
            <li key={n}>
              <AlertTriangle size={14} aria-hidden /> {n}
            </li>
          ))}
        </ul>
      )}
      <form onSubmit={submit}>
        <Field label="Comment" hint="Required to request changes: the agents revise using it as feedback.">
          <textarea rows={3} value={comment} onChange={(e) => setComment(e.target.value)} maxLength={10000} />
        </Field>
        <div className="actions">
          <button type="submit" className="btn btn-primary" disabled={decide.isPending}>
            <Check size={15} aria-hidden /> Approve
          </button>
          <button
            type="button"
            className="btn btn-danger"
            disabled={decide.isPending || !canReject}
            title={canReject ? undefined : "Write what should change first"}
            onClick={() => decide.mutate(false)}
          >
            <Undo2 size={15} aria-hidden /> Request changes
          </button>
        </div>
        <ErrorNote error={decide.error} />
      </form>
    </section>
  );
}
