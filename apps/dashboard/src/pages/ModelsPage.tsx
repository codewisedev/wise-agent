import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, unwrap } from "../api/client";
import { Badge, Empty, ErrorNote, Panel } from "../components/ui";
import { formatCost, formatTokens } from "./RunsTable";

export function ModelsPage() {
  const [days, setDays] = useState(7);
  const overview = useQuery({
    queryKey: ["models", days],
    queryFn: () => unwrap(api.GET("/models", { params: { query: { days } } })),
  });
  const o = overview.data;
  const totals = (o?.usage ?? []).reduce(
    (t, u) => ({ calls: t.calls + u.calls, tokens: t.tokens + u.inputTokens + u.outputTokens, cost: t.cost + u.costUsd }),
    { calls: 0, tokens: 0, cost: 0 },
  );

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Models</h1>
          <p className="desc">
            Every model call goes through the Model Gateway. Aliases and policies live in{" "}
            <code>configs/models/models.yaml</code> and <code>configs/policies/models.yaml</code>.
          </p>
        </div>
      </header>
      <ErrorNote error={overview.error} />

      <div className="stats">
        <div className="stat"><div className="value">{totals.calls}</div><div className="label">calls · {days}d</div></div>
        <div className="stat"><div className="value">{formatTokens(totals.tokens)}</div><div className="label">tokens · {days}d</div></div>
        <div className="stat"><div className="value">{formatCost(totals.cost)}</div><div className="label">cost · {days}d</div></div>
      </div>

      <Panel title="Providers" count={o?.providers.length}>
        <div className="table-wrap">
          <table>
            <thead><tr><th>Provider</th><th>Type</th><th>Location</th><th>Status</th></tr></thead>
            <tbody>
              {o?.providers.map((p) => (
                <tr key={p.name}>
                  <td className="mono row-link">{p.name}</td>
                  <td className="mono small">{p.type}</td>
                  <td>{p.cloud ? <Badge tone="info" dot={false}>cloud</Badge> : <Badge dot={false}>local</Badge>}</td>
                  <td>
                    {p.healthy ? <Badge tone="ok">ok</Badge> : <Badge tone="bad">unavailable</Badge>}
                    {p.error && <div className="cell-sub small faint">{p.error}</div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>

      <Panel title="Aliases" count={o?.aliases.length}>
        <div className="table-wrap">
          <table>
            <thead><tr><th>Alias</th><th>Targets (in order)</th><th>Description</th></tr></thead>
            <tbody>
              {o?.aliases.map((a) => (
                <tr key={a.name}>
                  <td className="mono row-link">{a.name}</td>
                  <td className="mono small">{a.targets.map((t) => `${t.provider}/${t.model || "loaded model"}`).join(" → ")}</td>
                  <td className="small muted">{a.description}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>

      <Panel
        title="Usage"
        actions={
          <select value={days} onChange={(e) => setDays(Number(e.target.value))} aria-label="Period">
            <option value={1}>24 hours</option>
            <option value={7}>7 days</option>
            <option value={30}>30 days</option>
          </select>
        }
      >
        {o && o.usage.length === 0 ? (
          <Empty>No model calls in this period.</Empty>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr><th>Agent</th><th>Alias</th><th>Provider / model</th><th style={{ textAlign: "right" }}>Calls</th>
                  <th style={{ textAlign: "right" }}>Errors</th><th style={{ textAlign: "right" }}>Tokens</th><th style={{ textAlign: "right" }}>Cost</th></tr>
              </thead>
              <tbody>
                {o?.usage.map((u) => (
                  <tr key={`${u.agentName}|${u.alias}|${u.provider}|${u.model}`}>
                    <td className="mono small">{u.agentName || "—"}</td>
                    <td className="mono small">{u.alias}</td>
                    <td className="mono small">{u.provider}/{u.model || "default"}</td>
                    <td className="num">{u.calls}</td>
                    <td className="num">{u.errors}</td>
                    <td className="num">{formatTokens(u.inputTokens + u.outputTokens)}</td>
                    <td className="num">{formatCost(u.costUsd)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </>
  );
}
