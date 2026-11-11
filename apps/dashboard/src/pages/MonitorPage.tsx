import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Square } from "lucide-react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import type { components } from "../api/schema.gen";
import { Badge, Empty, ErrorNote, Panel, relativeTime } from "../components/ui";
import { formatCost, formatTokens } from "./RunsTable";

type Run = components["schemas"]["MonitorRun"];

function CancelButton({ run }: { run: Run }) {
  const qc = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => unwrap(api.POST("/agent-runs/{runId}/cancel", { params: { path: { runId: run.id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["monitor"] }),
  });
  if (run.cancelling) return <Badge tone="warn" dot={false}>cancelling</Badge>;
  return (
    <>
      <button type="button" className="btn btn-sm btn-ghost" aria-label={`Cancel ${run.agent} run`} title="Cancel" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
        <Square size={12} aria-hidden />
      </button>
      <ErrorNote error={cancel.error} />
    </>
  );
}

function Where({ run }: { run: Run }) {
  if (run.taskId) return <Link to={`/tasks/${run.taskId}`}>{run.taskTitle || "task"}</Link>;
  if (run.projectId) return <Link to={`/projects/${run.projectId}`}>{run.projectName || "project"}</Link>;
  return <span className="faint">—</span>;
}

function RunTable({ runs, queued }: { runs: Run[]; queued?: boolean }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Agent</th><th>Runtime</th><th>For</th>
            {queued ? <th>Waiting</th> : <><th>Attempt</th><th>Last activity</th><th className="num">Tokens</th><th className="num">Cost</th></>}
            <th />
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <tr key={r.id} className={r.stuck ? "row-warn" : ""}>
              <td><Link to={`/runs/${r.id}`} className="mono">{r.agent}</Link></td>
              <td>{r.runtime === "native" ? <span className="faint">native</span> : <Badge tone="info" dot={false}>{r.runtime}</Badge>}</td>
              <td><Where run={r} /></td>
              {queued ? (
                <td title={r.createdAt}>{relativeTime(r.createdAt)}</td>
              ) : (
                <>
                  <td>{r.attempt}/{r.maxAttempts}</td>
                  <td>
                    {r.lastEvent ? <><span className="mono small">{r.lastEvent}</span> <span className="faint small">{relativeTime(r.lastEventAt)}</span></> : <span className="faint small">starting</span>}
                    {r.stuck && <div><Badge tone="warn">looks stuck: {r.stuck}</Badge></div>}
                  </td>
                  <td className="num">{formatTokens(r.tokens)}</td>
                  <td className="num">{formatCost(r.costUsd)}</td>
                </>
              )}
              <td><CancelButton run={r} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function useMonitor(refetch = 3000) {
  return useQuery({ queryKey: ["monitor"], queryFn: () => unwrap(api.GET("/monitor")), refetchInterval: refetch });
}

/** What Wise is doing now: running and queued agents, stuck runs, work waiting for you. */
export function MonitorPage() {
  const q = useMonitor();
  const m = q.data;
  return (
    <>
      <header className="page-header">
        <div>
          <h1>Monitor</h1>
          <p className="desc">Live: agent runs (Wise's own and Claude Code / Codex runners), the queue, runs that look stuck, and workflows waiting for your approval or a retry.</p>
        </div>
        {m && (
          <div className="stats compact">
            <div className="stat"><div className="value">{m.running.length}</div><div className="label">running</div></div>
            <div className="stat"><div className="value">{m.queued.length}</div><div className="label">queued</div></div>
            <div className="stat"><div className="value">{m.stuck}</div><div className="label">stuck</div></div>
            <div className="stat"><div className="value">{m.waiting.length}</div><div className="label">waiting for you</div></div>
          </div>
        )}
      </header>
      <ErrorNote error={q.error} />
      <Panel title="Running" count={m?.running.length}>
        {m && m.running.length === 0 ? <Empty>Nothing is running.</Empty> : <RunTable runs={m?.running ?? []} />}
      </Panel>
      <Panel title="Queued" count={m?.queued.length}>
        {m && m.queued.length === 0 ? <Empty>The queue is empty.</Empty> : <RunTable runs={m?.queued ?? []} queued />}
      </Panel>
      <Panel title="Waiting for you" count={m?.waiting.length}>
        {m && m.waiting.length === 0 ? <Empty>No workflow is waiting.</Empty> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Task</th><th>Project</th><th>Waiting for</th><th>At step</th><th>Since</th></tr></thead>
              <tbody>
                {m?.waiting.map((w) => (
                  <tr key={w.id}>
                    <td><Link to={`/tasks/${w.taskId}`}>{w.taskTitle}</Link></td>
                    <td>{w.projectName}</td>
                    <td>
                      {w.status === "awaiting_approval" ? <Badge tone="info">your approval</Badge> : <Badge tone="warn">a retry</Badge>}
                      {w.error && <div className="small bad-text">{w.error}</div>}
                    </td>
                    <td className="mono small">{w.stage}</td>
                    <td className="small faint">{relativeTime(w.since)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
      {m && m.evals.length > 0 && (
        <Panel title="Evaluation replays" count={m.evals.length}>
          <p className="small faint pad">Replays of improvement proposals share the queue with real work.</p>
          <RunTable runs={m.evals} />
        </Panel>
      )}
    </>
  );
}
