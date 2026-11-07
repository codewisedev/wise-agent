import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle, Bot, CheckCircle2, ChevronRight, CircleSlash, FileText, Flag, Hourglass, Play, RotateCcw, Square, XCircle,
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { useWorkflowEvents, type WorkflowEvent } from "../api/events";
import { ArtifactView } from "../components/ArtifactView";
import { Json } from "../components/Json";
import { Badge, CopyText, Empty, ErrorNote, Panel, formatDate, relativeTime } from "../components/ui";
import {
  ApprovalCard, KIND_LABEL, Pipeline, TaskStatus, WorkflowStatus, isActiveWorkflow, stageName, type Artifact,
} from "./workflowUi";

type P = Record<string, unknown>;
const str = (v: unknown) => (typeof v === "string" ? v : v == null ? "" : JSON.stringify(v));
const KIND_ORDER: Schemas["ArtifactKind"][] = ["product_analysis", "requirements", "plan", "architecture_review", "final_report"];

function describe(ev: WorkflowEvent): { icon: ReactNode; title: ReactNode; tone?: string } {
  const p = ev.payload as P;
  const agentLink = p.agentRunId ? <> · <Link to={`/runs/${str(p.agentRunId)}`} className="mono small">run {str(p.agentRunId).slice(0, 8)}</Link></> : null;
  switch (ev.type) {
    case "workflow.created":
      return { icon: <Flag size={14} />, title: "workflow created" };
    case "workflow.status":
      return {
        icon: <Play size={14} />,
        tone: p.status === "failed" || p.status === "awaiting_retry" ? "bad" : p.status === "completed" ? "ok" : undefined,
        title: <>{str(p.status).replace(/_/g, " ")}{p.stage ? <> · <span className="mono small">{stageName(str(p.stage))}</span></> : null}{p.error ? <> — {str(p.error)}</> : null}</>,
      };
    case "stage.running":
      return { icon: <Bot size={14} />, title: <><span className="mono small">{stageName(str(p.stage))}</span> started{agentLink}</> };
    case "stage.succeeded":
      return { icon: <CheckCircle2 size={14} />, tone: "ok", title: <><span className="mono small">{stageName(str(p.stage))}</span> done{agentLink}</> };
    case "stage.failed":
      return { icon: <XCircle size={14} />, tone: "bad", title: <><span className="mono small">{stageName(str(p.stage))}</span> — {str(p.error)}{agentLink}</> };
    case "stage.awaiting_approval":
      return { icon: <Hourglass size={14} />, tone: "warn", title: <><span className="mono small">{stageName(str(p.stage))}</span> waiting for you</> };
    case "stage.skipped":
      return { icon: <ChevronRight size={14} />, title: <><span className="mono small">{stageName(str(p.stage))}</span> skipped (not required)</> };
    case "artifact.created":
      return { icon: <FileText size={14} />, title: <>{KIND_LABEL[str(p.kind) as Schemas["ArtifactKind"]] ?? str(p.kind)} <span className="mono">v{str(p.version)}</span> <span className="mono small faint">#{str(p.hash).slice(0, 10)}</span></> };
    case "approval.requested":
      return { icon: <Hourglass size={14} />, tone: "warn", title: <>{str(p.gate)} approval requested · round {str(p.round)}</> };
    case "approval.approved":
      return { icon: <CheckCircle2 size={14} />, tone: "ok", title: <>{str(p.gate)} approved{p.comment ? <> — {str(p.comment)}</> : null}</> };
    case "approval.rejected":
      return { icon: <RotateCcw size={14} />, tone: "warn", title: <>{str(p.gate)} changes requested — {str(p.comment)}</> };
    case "approval.superseded":
      return { icon: <RotateCcw size={14} />, title: <>{str(p.gate)} approval superseded ({str(p.reason)})</> };
    case "approval.cancelled":
      return { icon: <CircleSlash size={14} />, title: <>{str(p.gate)} approval cancelled</> };
    case "workflow.retry_requested":
      return { icon: <RotateCcw size={14} />, title: <>retry requested · <span className="mono small">{stageName(str(p.stage))}</span></> };
    case "workflow.cancel_requested":
      return { icon: <Square size={14} />, tone: "warn", title: "cancellation requested" };
    default:
      return { icon: <AlertTriangle size={14} />, title: ev.type };
  }
}

function ArtifactsPanel({ taskId, latest, kind, onKind }: {
  taskId: string;
  latest: Artifact[];
  kind?: Schemas["ArtifactKind"];
  onKind: (k: Schemas["ArtifactKind"]) => void;
}) {
  const kinds = KIND_ORDER.filter((k) => latest.some((a) => a.kind === k));
  const active = kind && kinds.includes(kind) ? kind : kinds.at(-1);
  const [version, setVersion] = useState<number | undefined>();
  const [raw, setRaw] = useState(false);
  const versions = useQuery({
    queryKey: ["task", taskId, "artifacts", active],
    queryFn: () => unwrap(api.GET("/tasks/{taskId}/artifacts", { params: { path: { taskId }, query: { kind: active } } })),
    enabled: !!active,
  });
  useEffect(() => setVersion(undefined), [active]);
  if (!active) return <Panel title="Artifacts"><Empty icon={<FileText size={24} aria-hidden />}>Agents have not produced anything yet.</Empty></Panel>;
  const all = versions.data?.items ?? latest.filter((a) => a.kind === active);
  const shown = all.find((a) => a.version === version) ?? all[0];

  return (
    <Panel
      title="Artifacts"
      actions={
        <div className="panel-actions">
          {all.length > 1 && (
            <select aria-label="Version" value={shown?.version} onChange={(e) => setVersion(Number(e.target.value))}>
              {all.map((a) => <option key={a.id} value={a.version}>v{a.version}{a.version === all[0]?.version ? " (latest)" : ""}</option>)}
            </select>
          )}
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => setRaw((v) => !v)} aria-pressed={raw}>
            {raw ? "Readable" : "JSON"}
          </button>
        </div>
      }
    >
      <div className="tabs" role="tablist" aria-label="Artifact kind">
        {kinds.map((k) => (
          <button key={k} type="button" role="tab" aria-selected={k === active} className="tab" onClick={() => onKind(k)}>
            {KIND_LABEL[k]}
            <span className="mono faint small">v{latest.find((a) => a.kind === k)?.version}</span>
          </button>
        ))}
      </div>
      {shown && (
        <div className="panel-body">
          <div className="artifact-meta small faint">
            <span className="mono" title={shown.contentHash}>#{shown.contentHash.slice(0, 12)}</span>
            <span title={formatDate(shown.createdAt)}>{relativeTime(shown.createdAt)}</span>
            {shown.agentRunId && <Link to={`/runs/${shown.agentRunId}`}>agent run</Link>}
          </div>
          {raw ? <Json value={shown.content} label={KIND_LABEL[shown.kind]} maxHeight={700} /> : <ArtifactView content={shown.content} />}
        </div>
      )}
    </Panel>
  );
}

export function TaskPage() {
  const { taskId = "" } = useParams();
  const qc = useQueryClient();
  const [kind, setKind] = useState<Schemas["ArtifactKind"] | undefined>();
  const detail = useQuery({
    queryKey: ["task", taskId],
    queryFn: () => unwrap(api.GET("/tasks/{taskId}", { params: { path: { taskId } } })),
  });
  const wf = detail.data?.workflow;
  const live = isActiveWorkflow(wf) || !detail.data;
  const { events, connected, error: eventsError } = useWorkflowEvents(wf?.id ?? "", !!wf && live);

  // Every workflow event can change status, steps, artifacts or approvals.
  const lastSeq = events.at(-1)?.seq ?? 0;
  useEffect(() => {
    if (lastSeq) void qc.invalidateQueries({ queryKey: ["task", taskId] });
    if (lastSeq) void qc.invalidateQueries({ queryKey: ["approvals"] });
  }, [lastSeq, qc, taskId]);

  const cancel = useMutation({
    mutationFn: () => unwrap(api.POST("/tasks/{taskId}/cancel", { params: { path: { taskId } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["task", taskId] }),
  });
  const retry = useMutation({
    mutationFn: (workflowId: string) => unwrap(api.POST("/workflows/{workflowId}/retry", { params: { path: { workflowId } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["task", taskId] }),
  });
  const restart = useMutation({
    mutationFn: () => unwrap(api.POST("/tasks/{taskId}/workflows", { params: { path: { taskId } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["task", taskId] }),
  });

  if (detail.error) return <ErrorNote error={detail.error} />;
  if (!detail.data) return <p className="loading">loading…</p>;
  const { task, stages, steps, artifacts, approvals } = detail.data;
  const pending = approvals.find((a) => a.status === "pending" && a.workflowRunId === wf?.id);

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/tasks">tasks</Link>
        <ChevronRight size={12} aria-hidden />
        <span>{task.title}</span>
      </nav>
      <header className="page-header">
        <div>
          <h1>{task.title}</h1>
          <div className="meta">
            {wf ? <WorkflowStatus wf={wf} /> : <TaskStatus status={task.status} />}
            <Link to={`/projects/${task.projectId}`} className="small">project</Link>
            {wf?.temporalWorkflowId && <CopyText value={wf.temporalWorkflowId} display={wf.temporalWorkflowId.slice(0, 11)} label="Temporal workflow id" />}
            <span className="small faint" title={formatDate(task.createdAt)}>created {relativeTime(task.createdAt)}</span>
            {wf && live && <Badge tone={connected ? "ok" : "neutral"} pulse={connected}>{connected ? "live" : "connecting"}</Badge>}
          </div>
          {task.description && <p className="desc">{task.description}</p>}
        </div>
        <div className="actions" style={{ marginTop: 0 }}>
          {wf && isActiveWorkflow(wf) && (
            <button className="btn btn-danger" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
              <Square size={14} aria-hidden /> Cancel
            </button>
          )}
          {(!wf || !isActiveWorkflow(wf)) && task.status !== "completed" && (
            <button className="btn" onClick={() => restart.mutate()} disabled={restart.isPending}>
              <Play size={14} aria-hidden /> Start new workflow
            </button>
          )}
        </div>
      </header>
      <ErrorNote error={cancel.error ?? restart.error} />

      <Pipeline stages={stages} steps={steps} wf={wf} />

      {wf?.status === "awaiting_retry" && (
        <section className="approval approval-bad" aria-label="Stage failed">
          <header>
            <XCircle size={16} aria-hidden />
            <h2>{stageName(wf.currentStage ?? "")} failed</h2>
          </header>
          <p className="small">{wf.error}</p>
          <div className="actions">
            <button className="btn btn-primary" onClick={() => retry.mutate(wf.id)} disabled={retry.isPending}>
              <RotateCcw size={15} aria-hidden /> Retry stage
            </button>
          </div>
          <ErrorNote error={retry.error} />
        </section>
      )}
      {wf?.status === "failed" && wf.error && (
        <p className="error" role="alert"><AlertTriangle size={14} aria-hidden /> <span>{wf.error}</span></p>
      )}
      {pending && <ApprovalCard key={pending.id} approval={pending} onShowArtifact={setKind} />}

      <ArtifactsPanel taskId={taskId} latest={artifacts} kind={kind} onKind={setKind} />

      <Panel title="Timeline" count={events.length}>
        <ErrorNote error={eventsError} />
        {!wf && <Empty icon={<Flag size={24} aria-hidden />}>No workflow has run for this task.</Empty>}
        <ol className="timeline">
          {events.map((ev) => {
            const d = describe(ev);
            return (
              <li key={ev.seq} className={d.tone ? `tone-${d.tone}` : ""}>
                <span className="tl-icon" aria-hidden>{d.icon}</span>
                <div className="tl-main"><div className="tl-title">{d.title}</div></div>
                <time className="tl-time mono" dateTime={ev.createdAt} title={formatDate(ev.createdAt)}>
                  #{ev.seq} {new Date(ev.createdAt).toLocaleTimeString()}
                </time>
              </li>
            );
          })}
        </ol>
      </Panel>
    </>
  );
}
