import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import type { components } from "../api/schema.gen";
import { Badge, Empty, ErrorNote, Panel } from "../components/ui";
import { formatCost, formatTokens } from "./RunsTable";

type Window = "7d" | "30d" | "90d";
type Cluster = components["schemas"]["InsightCluster"];

export const pct = (x: number) => `${Math.round(x * 100)}%`;
export const secs = (s: number) => (s >= 90 ? `${(s / 60).toFixed(1)}m` : `${Math.round(s)}s`);

export function WindowPicker({ value, onChange }: { value: Window; onChange: (w: Window) => void }) {
  return (
    <div className="segmented" role="group" aria-label="Window">
      {(["7d", "30d", "90d"] as const).map((w) => (
        <button key={w} type="button" className={`btn btn-sm${value === w ? " active" : ""}`} aria-pressed={value === w} onClick={() => onChange(w)}>
          {w}
        </button>
      ))}
    </div>
  );
}

export function Clusters({ items, kind }: { items: Cluster[]; kind: "failure" | "comment" }) {
  if (items.length === 0) return <Empty>{kind === "failure" ? "No failed runs in this window." : "No review rejections with comments."}</Empty>;
  return (
    <ul className="clusters">
      {items.map((c, i) => (
        <li key={i}>
          <span className="count mono">×{c.count}</span>
          <span className="mono small">{c.agent}</span>
          <span className="text">{c.text}</span>
          {c.runIds?.map((id) => <Link key={id} to={`/runs/${id}`} className="small mono">{id.slice(0, 8)}</Link>)}
        </li>
      ))}
    </ul>
  );
}

/** How agents, models and workflows performed — computed from Wise's own records (evaluation replays excluded). */
export function InsightsPage() {
  const [win, setWin] = useState<Window>("30d");
  const q = useQuery({ queryKey: ["insights", win], queryFn: () => unwrap(api.GET("/insights", { params: { query: { window: win } } })) });
  const r = q.data;
  return (
    <>
      <header className="page-header">
        <div>
          <h1>Insights</h1>
          <p className="desc">
            How your agents perform: success, retries, invalid outputs, review rejections (counted against the steps a
            rejection re-runs), quality-gate fix rounds, cost and your ratings.
          </p>
        </div>
        <WindowPicker value={win} onChange={setWin} />
      </header>
      <ErrorNote error={q.error} />

      <Panel title="Agents" count={r?.agents.length}>
        {r && r.agents.length === 0 ? <Empty>No agent runs in this window.</Empty> : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Agent</th><th>Version</th><th className="num">Runs</th><th className="num">Success</th><th className="num">Retries</th>
                  <th className="num">Invalid</th><th className="num">Rejected</th><th className="num">Fix rounds</th>
                  <th className="num">Median / p90</th><th className="num">Cost / run</th><th className="num">Ratings</th>
                </tr>
              </thead>
              <tbody>
                {r?.agents.map((a) => (
                  <tr key={a.definitionId}>
                    <td><Link to={`/agents/${a.agent}`} className="mono">{a.agent}</Link></td>
                    <td className="mono small">{a.version}</td>
                    <td className="num">{a.runs}</td>
                    <td className="num"><Badge tone={a.successRate >= 0.9 ? "ok" : a.successRate >= 0.6 ? "warn" : "bad"} dot={false}>{pct(a.successRate)}</Badge></td>
                    <td className="num">{a.retries}</td>
                    <td className="num">{a.invalidOutputs ? pct(a.invalidRate) : "–"}</td>
                    <td className="num">{a.reviewed ? `${a.rejected}/${a.reviewed}` : "–"}</td>
                    <td className="num">{a.gateFixRounds || "–"}</td>
                    <td className="num small">{secs(a.medianSeconds)} / {secs(a.p90Seconds)}</td>
                    <td className="num" title={`${formatTokens(a.tokens)} tokens`}>{formatCost(a.costPerRunUsd)}</td>
                    <td className="num small">👍 {a.thumbsUp} · 👎 {a.thumbsDown}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <Panel title="Repeated failures" count={r?.failures.length} flush={false}>
        <Clusters items={r?.failures ?? []} kind="failure" />
      </Panel>
      <Panel title="Review rejection comments" count={r?.comments.length} flush={false}>
        <Clusters items={r?.comments ?? []} kind="comment" />
      </Panel>

      <Panel title="Models" count={r?.providers.length}>
        <div className="table-wrap">
          <table>
            <thead><tr><th>Provider</th><th>Model</th><th className="num">Calls</th><th className="num">Errors</th><th className="num">Refused</th><th className="num">Median latency</th><th className="num">Tokens</th><th className="num">Cost</th></tr></thead>
            <tbody>
              {r?.providers.map((p) => (
                <tr key={`${p.provider}/${p.model}`}>
                  <td className="mono">{p.provider}</td><td className="mono small">{p.model}</td><td className="num">{p.calls}</td>
                  <td className="num">{p.errors ? pct(p.errorRate) : "–"}</td><td className="num">{p.rejected || "–"}</td>
                  <td className="num">{Math.round(p.medianLatencyMs)} ms</td><td className="num">{formatTokens(p.tokens)}</td><td className="num">{formatCost(p.costUsd)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>

      <Panel title="Workflows" count={r?.templates.length}>
        <div className="table-wrap">
          <table>
            <thead><tr><th>Template</th><th className="num">Runs</th><th className="num">Completed</th><th className="num">Failed</th><th className="num">Waiting for retry</th><th className="num">Median</th><th className="num">Cost</th></tr></thead>
            <tbody>
              {r?.templates.map((t) => (
                <tr key={t.template}>
                  <td>{t.template}</td><td className="num">{t.runs}</td><td className="num">{t.completed}</td><td className="num">{t.failed}</td>
                  <td className="num">{t.stalled || "–"}</td><td className="num">{t.completed ? secs(t.medianSeconds) : "–"}</td><td className="num">{formatCost(t.costUsd)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {r && r.stalls.length > 0 && (
          <p className="small faint pad">Waiting now: {r.stalls.map((s) => `${s.template} at ${s.stage || "start"} (${s.waiting})`).join(" · ")}</p>
        )}
      </Panel>
    </>
  );
}

/** An agent's own numbers: versions compared, daily trend, failures, review comments and your notes. */
export function AgentInsights({ agent }: { agent: string }) {
  const [win, setWin] = useState<Window>("30d");
  const q = useQuery({
    queryKey: ["insights", "agent", agent, win],
    queryFn: () => unwrap(api.GET("/insights/agents/{agentName}", { params: { path: { agentName: agent }, query: { window: win } } })),
  });
  const d = q.data;
  const max = Math.max(1, ...(d?.days ?? []).map((x) => x.runs));
  return (
    <Panel title="Performance" actions={<WindowPicker value={win} onChange={setWin} />} flush={false}>
      <ErrorNote error={q.error} />
      {d && d.versions.length === 0 ? <Empty>No runs in this window.</Empty> : (
        <>
          <div className="stats">
            {d?.versions.map((v) => (
              <div className="stat" key={v.definitionId}>
                <div className="value">{pct(v.successRate)}</div>
                <div className="label">v{v.version} · {v.runs} runs · {v.reviewed ? `${v.rejected}/${v.reviewed} rejected` : "not reviewed"} · {formatCost(v.costPerRunUsd)}/run</div>
              </div>
            ))}
          </div>
          <div className="sparkbars" role="img" aria-label={`Runs per day: ${d?.days.map((x) => `${x.day} ${x.runs}`).join(", ")}`}>
            {d?.days.map((x) => (
              <span key={x.day} title={`${x.day}: ${x.runs} runs, ${x.succeeded} ok, ${formatCost(x.costUsd)}`}
                style={{ height: `${(x.runs / max) * 100}%` }} className={x.succeeded < x.runs ? "warn" : ""} />
            ))}
          </div>
          {d && d.failures.length > 0 && <><h4>Repeated failures</h4><Clusters items={d.failures} kind="failure" /></>}
          {d && d.comments.length > 0 && <><h4>Review rejection comments</h4><Clusters items={d.comments} kind="comment" /></>}
          {d && d.feedback.length > 0 && (
            <>
              <h4>Your thumbs-down notes</h4>
              <ul className="clusters">
                {d.feedback.map((f) => (
                  <li key={f.id}>
                    <span className="text">{f.note || <span className="faint">(no note)</span>}</span>
                    {f.runId && <Link to={`/runs/${f.runId}`} className="small mono">{f.runId.slice(0, 8)}</Link>}
                  </li>
                ))}
              </ul>
            </>
          )}
        </>
      )}
    </Panel>
  );
}
