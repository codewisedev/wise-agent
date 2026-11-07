import { Play } from "lucide-react";
import { Link } from "react-router";
import type { Schemas } from "../api/client";
import { Badge, Empty, relativeTime, type Tone } from "../components/ui";

export const runTone: Record<Schemas["AgentRunStatus"], Tone> = {
  queued: "info",
  running: "info",
  succeeded: "ok",
  failed: "bad",
  cancelled: "neutral",
};

export function RunStatus({ run }: { run: Schemas["AgentRun"] }) {
  const active = run.status === "queued" || run.status === "running";
  return (
    <Badge tone={runTone[run.status]} pulse={active}>
      {run.cancelRequested && active ? "cancelling" : run.status}
    </Badge>
  );
}

export function formatCost(usd: number): string {
  if (usd === 0) return "$0";
  return usd < 0.01 ? `$${usd.toFixed(4)}` : `$${usd.toFixed(2)}`;
}

export function formatTokens(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n);
}

export function RunsTable({ runs, showAgent }: { runs?: Schemas["AgentRun"][]; showAgent?: boolean }) {
  if (!runs) return <p className="loading">loading…</p>;
  if (runs.length === 0) return <Empty icon={<Play size={24} aria-hidden />}>No runs yet.</Empty>;
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Run</th>
            {showAgent && <th>Agent</th>}
            <th>Status</th>
            <th style={{ textAlign: "right" }}>Tokens</th>
            <th style={{ textAlign: "right" }}>Cost</th>
            <th>Started</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <tr key={r.id}>
              <td>
                <Link to={`/runs/${r.id}`} className="row-link mono">
                  {r.id.slice(0, 8)}
                </Link>
                {r.error && <div className="cell-sub small faint">{r.error.slice(0, 80)}</div>}
              </td>
              {showAgent && <td className="mono small">{r.agentName}</td>}
              <td>
                <RunStatus run={r} />
              </td>
              <td className="num">{formatTokens(r.totalInputTokens + r.totalOutputTokens)}</td>
              <td className="num">{formatCost(r.totalCostUsd)}</td>
              <td className="muted" title={r.createdAt}>
                {relativeTime(r.createdAt)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
