import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import type { components } from "../api/schema.gen";
import { Empty, ErrorNote, Field, Panel } from "../components/ui";
import { WindowPicker } from "./InsightsPage";
import { formatCost, formatTokens } from "./RunsTable";

type Report = components["schemas"]["CostReport"];
type Window = "7d" | "30d" | "90d";
type GroupBy = "project" | "agent" | "model" | "runtime" | "billing";

const GROUPS: Record<GroupBy, string> = { agent: "agent", project: "project", model: "model", runtime: "runtime", billing: "billing" };
const BILLING_LABEL: Record<string, string> = { subscription: "subscription", api: "pay per use", local: "local" };

/** Series colors: fixed slot order (never cycled); "other" is neutral. */
const color = (keys: string[], k: string) => (k === "other" ? "var(--series-other)" : `var(--series-${(keys.indexOf(k) % 8) + 1})`);

function StackedBars({ r }: { r: Report }) {
  const [hover, setHover] = useState<number | null>(null);
  const W = 720;
  const H = 220;
  const pad = { l: 44, r: 8, t: 8, b: 22 };
  const max = Math.max(...r.days.map((d) => d.total), 0.0001);
  const ticks = [0, max / 2, max];
  const n = r.days.length;
  const bw = (W - pad.l - pad.r) / n;
  const barW = Math.max(2, Math.min(28, bw - 4));
  const y = (v: number) => pad.t + (H - pad.t - pad.b) * (1 - v / max);
  const labelEvery = Math.ceil(n / 8);
  const d = hover !== null ? r.days[hover] : null;
  return (
    <div className="chart">
      <ul className="legend small" aria-label="Legend">
        {r.keys.map((k) => (
          <li key={k}><span className="swatch" style={{ background: color(r.keys, k) }} aria-hidden />{BILLING_LABEL[k] ?? k}</li>
        ))}
      </ul>
      <div className="chart-plot">
        <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Spend per day, stacked by ${r.groupBy}; total ${formatCost(r.total)}`} preserveAspectRatio="none">
          {ticks.map((t) => (
            <g key={t}>
              <line x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} className="grid" />
              <text x={pad.l - 6} y={y(t)} className="axis" textAnchor="end" dominantBaseline="middle">{formatCost(t)}</text>
            </g>
          ))}
          {r.days.map((day, i) => {
            const x = pad.l + i * bw + (bw - barW) / 2;
            let acc = 0;
            const segs = r.keys.filter((k) => (day.by[k] ?? 0) > 0);
            return (
              <g key={day.day} tabIndex={0} role="listitem" aria-label={`${day.day}: ${formatCost(day.total)}`}
                onPointerEnter={() => setHover(i)} onPointerLeave={() => setHover(null)} onFocus={() => setHover(i)} onBlur={() => setHover(null)}
                className={hover === i ? "bar hovered" : "bar"}>
                <rect x={pad.l + i * bw} y={pad.t} width={bw} height={H - pad.t - pad.b} className="hit" />
                {segs.map((k, j) => {
                  const v = day.by[k] ?? 0;
                  const y0 = y(acc + v);
                  const h = y(acc) - y0;
                  acc += v;
                  const top = j === segs.length - 1;
                  // 2px surface gap between stacked segments; a rounded data-end on the top segment only.
                  return <rect key={k} x={x} y={y0 + (top ? 0 : 1)} width={barW} height={Math.max(0, h - (top ? 0 : 2))} rx={top ? 3 : 0} fill={color(r.keys, k)} />;
                })}
                {i % labelEvery === 0 && <text x={pad.l + i * bw + bw / 2} y={H - 6} className="axis" textAnchor="middle">{day.day.slice(5)}</text>}
              </g>
            );
          })}
        </svg>
        {d && (
          <div className="chart-tip small" role="status" style={{ left: `${((pad.l + (hover! + 0.5) * bw) / W) * 100}%` }}>
            <div><strong>{formatCost(d.total)}</strong> <span className="faint">{d.day}</span></div>
            {r.keys.filter((k) => (d.by[k] ?? 0) > 0).map((k) => (
              <div key={k}><span className="line-key" style={{ background: color(r.keys, k) }} aria-hidden /><strong>{formatCost(d.by[k] ?? 0)}</strong> {BILLING_LABEL[k] ?? k}</div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function TableView({ r }: { r: Report }) {
  const days = r.days.filter((d) => d.total > 0);
  if (days.length === 0) return <Empty>No spend in this window.</Empty>;
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th>Day</th>{r.keys.map((k) => <th key={k} className="num">{BILLING_LABEL[k] ?? k}</th>)}<th className="num">Total</th></tr></thead>
        <tbody>
          {days.map((d) => (
            <tr key={d.day}>
              <td className="mono small">{d.day}</td>
              {r.keys.map((k) => <td key={k} className="num">{d.by[k] ? formatCost(d.by[k]!) : "–"}</td>)}
              <td className="num"><strong>{formatCost(d.total)}</strong></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Where model spend goes: per day, by project, agent, model, runtime or billing; against budgets. */
export function CostsPage() {
  const [win, setWin] = useState<Window>("30d");
  const [by, setBy] = useState<GroupBy>("agent");
  const [view, setView] = useState<"chart" | "table">("chart");
  const q = useQuery({ queryKey: ["costs", win, by], queryFn: () => unwrap(api.GET("/costs", { params: { query: { window: win, groupBy: by } } })) });
  const r = q.data;
  return (
    <>
      <header className="page-header">
        <div>
          <h1>Costs</h1>
          <p className="desc">
            Model spend from every call (Wise's agents and Claude Code / Codex runners). Subscriptions report an API-equivalent
            cost — shown apart, it is not what you pay. Budgets are in <Link to="/settings?tab=policy">Policy &amp; budgets</Link>.
          </p>
        </div>
      </header>
      <div className="filter-row">
        <div className="field">
          <span>Window</span>
          <WindowPicker value={win} onChange={setWin} />
        </div>
        <Field label="Group by">
          <select value={by} onChange={(e) => setBy(e.target.value as GroupBy)}>
            {(Object.keys(GROUPS) as GroupBy[]).map((g) => <option key={g} value={g}>{GROUPS[g]}</option>)}
          </select>
        </Field>
      </div>
      <ErrorNote error={q.error} />
      {r && (
        <div className="stats">
          <div className="stat"><div className="value">{formatCost(r.total)}</div><div className="label">total · {r.window}</div></div>
          <div className="stat"><div className="value">{formatCost(r.billing.api ?? 0)}</div><div className="label">pay per use</div></div>
          <div className="stat"><div className="value">{formatCost(r.billing.subscription ?? 0)}</div><div className="label">subscription (API-equivalent)</div></div>
          <div className="stat"><div className="value">{formatTokens(r.tokens)}</div><div className="label">tokens · {r.calls} calls</div></div>
          {r.evalUsd > 0 && <div className="stat"><div className="value">{formatCost(r.evalUsd)}</div><div className="label">evaluations (apart)</div></div>}
        </div>
      )}
      <Panel title="Spend per day" flush={false}
        actions={
          <div className="segmented" role="group" aria-label="View">
            {(["chart", "table"] as const).map((v) => (
              <button key={v} type="button" className={`btn btn-sm${view === v ? " active" : ""}`} aria-pressed={view === v} onClick={() => setView(v)}>{v}</button>
            ))}
          </div>
        }>
        {r && (r.total === 0 ? <Empty>No spend in this window.</Empty> : view === "chart" ? <StackedBars r={r} /> : <TableView r={r} />)}
      </Panel>
      <Panel title="Today against daily budgets" count={r?.budgets.length} flush={false}>
        {r && r.budgets.length === 0 ? <Empty>Nothing spent today.</Empty> : (
          <ul className="budgets">
            {r?.budgets.map((b) => {
              const pct = b.budget ? Math.min(1, b.spent / b.budget) : 0;
              const tone = pct >= 1 ? "bad" : pct >= 0.8 ? "warn" : "ok";
              return (
                <li key={b.agent}>
                  <span className="mono small">{b.agent}</span>
                  <span className="meter" role="meter" aria-valuemin={0} aria-valuemax={b.budget ?? 0} aria-valuenow={b.spent} aria-label={`${b.agent} spend today`}>
                    <span className={`fill tone-${tone}`} style={{ width: `${pct * 100}%` }} />
                  </span>
                  <span className="small">{formatCost(b.spent)} {b.budget != null ? <>of {formatCost(b.budget)}{pct >= 1 && " — cloud calls blocked until tomorrow"}</> : <span className="faint">no budget</span>}</span>
                </li>
              );
            })}
          </ul>
        )}
      </Panel>
      <Panel title="Costliest tasks" count={r?.topTasks.length}>
        {r && r.topTasks.length === 0 ? <Empty>No task spend in this window.</Empty> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Task</th><th className="num">Agent runs</th><th className="num">Cost</th></tr></thead>
              <tbody>
                {r?.topTasks.map((t) => (
                  <tr key={t.id}><td><Link to={`/tasks/${t.id}`}>{t.title}</Link></td><td className="num">{t.runs}</td><td className="num">{formatCost(t.costUsd)}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </>
  );
}
