import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle, ArrowDownLeft, ArrowUpRight, Bot, CheckCircle2, ChevronRight, CircleSlash, Cpu, Play, RotateCcw,
  Square, Wrench, XCircle,
} from "lucide-react";
import { useEffect, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import { api, unwrap } from "../api/client";
import { useRunEvents, type AgentEvent } from "../api/events";
import { Json } from "../components/Json";
import { FeedbackButtons } from "../components/FeedbackButtons";
import { Badge, CopyText, ErrorNote, Panel, formatDate, relativeTime } from "../components/ui";
import { RunStatus, formatCost, formatTokens } from "./RunsTable";

type P = Record<string, unknown>;
const str = (v: unknown) => (typeof v === "string" ? v : v == null ? "" : JSON.stringify(v));
const num = (v: unknown) => (typeof v === "number" ? v : 0);

function describe(ev: AgentEvent): { icon: ReactNode; title: ReactNode; tone?: string; body?: ReactNode } {
  const p = ev.payload as P;
  switch (ev.type) {
    case "run.queued":
      return { icon: <Play size={14} />, title: <>queued <span className="mono faint">{str(p.agent)} v{str(p.version)}</span></> };
    case "run.started":
      return { icon: <Play size={14} />, title: <>started on <span className="mono">{str(p.runtimeId)}</span> · attempt {num(p.attempt)}</> };
    case "run.requeued":
      return { icon: <RotateCcw size={14} />, tone: "warn", title: <>requeued — {str(p.reason)}</> };
    case "run.completed":
      return { icon: <CheckCircle2 size={14} />, tone: "ok", title: "completed" };
    case "run.failed":
      return { icon: <XCircle size={14} />, tone: "bad", title: <>failed — {str(p.error)}</> };
    case "run.cancelled":
      return { icon: <CircleSlash size={14} />, tone: "warn", title: "cancelled" };
    case "model.request":
      return {
        icon: <ArrowUpRight size={14} />,
        title: <>model request → <span className="mono">{str(p.provider)}/{str(p.model) || "default"}</span>
          {num(p.attempt) > 1 && <> · attempt {num(p.attempt)}</>}<span className="faint"> ({num(p.messages)} messages)</span></>,
      };
    case "model.response": {
      const u = (p.usage ?? {}) as P;
      return {
        icon: <ArrowDownLeft size={14} />,
        tone: p.parseError ? "warn" : undefined,
        title: <>
          <span className="mono">{str(p.action)}</span>
          {Array.isArray(p.toolCalls) && <> → {(p.toolCalls as string[]).map((t) => <code key={t} className="chip">{t}</code>)}</>}
          <span className="faint"> · {formatTokens(num(u.inputTokens))} in / {formatTokens(num(u.outputTokens))} out · {formatCost(num(p.costUsd))} · {(num(p.latencyMs) / 1000).toFixed(1)}s{p.cacheHit ? " · cached" : ""}</span>
        </>,
        body: p.parseError ? <p className="small">parse error: {str(p.parseError)}</p> : undefined,
      };
    }
    case "model.error":
      return { icon: <AlertTriangle size={14} />, tone: p.rejected ? "warn" : "bad", title: <><span className="mono">{str(p.provider)}</span> {p.rejected ? "skipped" : "error"} — {str(p.error)}</> };
    case "agent.message":
      return { icon: <Bot size={14} />, title: <span className="thought">{str(p.thought)}</span> };
    case "tool.call":
      return { icon: <Wrench size={14} />, title: <>call <code className="chip">{str(p.tool)}</code> <span className="mono small faint">{JSON.stringify(p.input ?? {})}</span></> };
    case "tool.result":
      return {
        icon: <Wrench size={14} />,
        tone: p.status === "error" ? "bad" : undefined,
        title: <><code className="chip">{str(p.tool)}</code> {p.status === "error" ? <>error — {str(p.error)}</> : "ok"} <span className="faint">· {num(p.durationMs)}ms</span></>,
        body: p.status !== "error" && p.output != null ? (
          <details><summary>output</summary><Json value={p.output} label="tool output" maxHeight={300} /></details>
        ) : undefined,
      };
    case "external.started":
      return {
        icon: <Cpu size={14} />, tone: p.reducedIsolation ? "warn" : undefined,
        title: <>running on <code className="chip">{str(p.runtime)}</code> {str(p.cliVersion)} · {p.mode === "proxy" ? "Runtime Proxy" : "subscription"}
          {p.reducedIsolation ? <> <Badge tone="warn" dot={false}>reduced isolation</Badge></> : null}</>,
      };
    case "file.changed":
      return { icon: <Wrench size={14} />, title: <>changed <span className="mono small">{str(p.path)}</span></> };
    case "agent.warning":
      return { icon: <AlertTriangle size={14} />, tone: "warn", title: <>{str(p.text)}</> };
    case "external.finished":
      return { icon: <Cpu size={14} />, tone: p.ok ? "ok" : "bad", title: <>{p.ok ? "runtime finished" : "runtime failed"}{p.text ? <> — {str(p.text).slice(0, 300)}</> : null}</> };
    case "output.invalid":
      return {
        icon: <AlertTriangle size={14} />,
        tone: "warn",
        title: <>invalid output (attempt {num(p.attempt)}) — {str(p.reason)}</>,
        body: Array.isArray(p.errors) ? <ul className="bullets small">{(p.errors as string[]).map((e) => <li key={e}>{e}</li>)}</ul> : undefined,
      };
    default:
      return { icon: <Cpu size={14} />, title: ev.type, body: <Json value={p} /> };
  }
}

export function RunPage() {
  const { runId = "" } = useParams();
  const qc = useQueryClient();
  const run = useQuery({
    queryKey: ["run", runId],
    queryFn: () => unwrap(api.GET("/agent-runs/{runId}", { params: { path: { runId } } })),
  });
  const active = run.data ? run.data.status === "queued" || run.data.status === "running" : true;
  const { events, connected, error: eventsError } = useRunEvents(runId, active);

  // Refresh the run summary whenever a lifecycle or model event arrives.
  const lastSeq = events.at(-1)?.seq ?? 0;
  useEffect(() => {
    if (lastSeq) void qc.invalidateQueries({ queryKey: ["run", runId], exact: true });
  }, [lastSeq, qc, runId]);

  const cancel = useMutation({
    mutationFn: () => unwrap(api.POST("/agent-runs/{runId}/cancel", { params: { path: { runId } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["run", runId], exact: true }),
  });

  if (run.error) return <ErrorNote error={run.error} />;
  if (!run.data) return <p className="loading">loading…</p>;
  const r = run.data;

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/agents">agents</Link>
        <ChevronRight size={12} aria-hidden />
        <Link to={`/agents/${r.agentName}`} className="mono">{r.agentName}</Link>
        <ChevronRight size={12} aria-hidden />
        <span className="mono">{r.id.slice(0, 8)}</span>
      </nav>
      <header className="page-header">
        <div>
          <h1>Run <span className="mono">{r.id.slice(0, 8)}</span></h1>
          <div className="meta">
            <RunStatus run={r} />
            <CopyText value={r.id} display={`id:${r.id.slice(0, 8)}`} label="run id" />
            {r.projectId && <Link to={`/projects/${r.projectId}`} className="small">project</Link>}
            <span className="small faint" title={formatDate(r.createdAt)}>created {relativeTime(r.createdAt)}</span>
            {active && <Badge tone={connected ? "ok" : "neutral"} pulse={connected}>{connected ? "live" : "connecting"}</Badge>}
            {r.purpose === "eval" && <Badge tone="info" dot={false}>evaluation replay</Badge>}
            {!active && r.purpose !== "eval" && <FeedbackButtons targetType="run" targetId={r.id} />}
          </div>
        </div>
        {active && !r.cancelRequested && (
          <button className="btn btn-danger" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
            <Square size={14} aria-hidden /> Cancel
          </button>
        )}
      </header>
      <ErrorNote error={cancel.error} />

      <div className="stats">
        <div className="stat"><div className="value">{formatTokens(r.totalInputTokens)}</div><div className="label">input tokens</div></div>
        <div className="stat"><div className="value">{formatTokens(r.totalOutputTokens)}</div><div className="label">output tokens</div></div>
        <div className="stat"><div className="value">{formatCost(r.totalCostUsd)}</div><div className="label">cost</div></div>
        <div className="stat"><div className="value">{r.attempt}/{r.maxAttempts}</div><div className="label">attempt</div></div>
      </div>

      {r.error && (
        <p className="error" role="alert"><AlertTriangle size={14} aria-hidden /> <span>{r.error}</span></p>
      )}

      {r.output && (
        <Panel title="Output" flush={false}>
          <Json value={r.output} label="output" maxHeight={600} />
        </Panel>
      )}

      <Panel title="Timeline" count={events.length}>
        <ErrorNote error={eventsError} />
        <ol className="timeline">
          {events.map((ev) => {
            const d = describe(ev);
            return (
              <li key={ev.seq} className={d.tone ? `tone-${d.tone}` : ""}>
                <span className="tl-icon" aria-hidden>{d.icon}</span>
                <div className="tl-main">
                  <div className="tl-title">{d.title}</div>
                  {d.body}
                </div>
                <time className="tl-time mono" dateTime={ev.createdAt} title={formatDate(ev.createdAt)}>
                  #{ev.seq} {new Date(ev.createdAt).toLocaleTimeString()}
                </time>
              </li>
            );
          })}
        </ol>
      </Panel>

      {r.context?.text && (
        <Panel title="Context given to the agent" flush={false}>
          <p className="small muted" style={{ marginTop: 0 }}>
            Added to the agent's instructions when the run started. Rules: {r.context.rules?.length ?? 0}.{" "}
            Relevant memories: {r.context.relevant?.length ?? 0}{r.context.semantic ? " (found by meaning)" : ""}. <Link to="/memory">Manage memory →</Link>
          </p>
          <pre className="json prompt">{r.context.text}</pre>
        </Panel>
      )}

      <Panel title="Input" flush={false}>
        <Json value={r.input} label="input" />
      </Panel>
    </>
  );
}
