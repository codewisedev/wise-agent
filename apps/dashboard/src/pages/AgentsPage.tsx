import { useQuery } from "@tanstack/react-query";
import { Bot } from "lucide-react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import { Badge, Empty, ErrorNote, Panel, relativeTime } from "../components/ui";
import { RunsTable } from "./RunsTable";

export function AgentsPage() {
  const agents = useQuery({ queryKey: ["agents"], queryFn: () => unwrap(api.GET("/agents")) });
  const runs = useQuery({
    queryKey: ["agent-runs"],
    queryFn: () => unwrap(api.GET("/agent-runs")),
    refetchInterval: 5000,
  });
  const items = agents.data?.items ?? [];

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Agents</h1>
          <p className="desc">
            Specialized agents with versioned contracts. Disabled agents are declared now and become executable in
            their roadmap phase.
          </p>
        </div>
      </header>

      <Panel title="Registry" count={agents.data ? items.length : undefined}>
        <ErrorNote error={agents.error} />
        {agents.data && items.length === 0 && <Empty icon={<Bot size={28} aria-hidden />}>No agents registered.</Empty>}
        {items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Agent</th>
                  <th>Version</th>
                  <th>Status</th>
                  <th>Model</th>
                  <th style={{ textAlign: "right" }}>Tools</th>
                  <th style={{ textAlign: "right" }}>Runs</th>
                  <th>Last run</th>
                </tr>
              </thead>
              <tbody>
                {items.map((a) => (
                  <tr key={a.id} className={a.enabled ? "" : "is-dim"}>
                    <td>
                      <Link to={`/agents/${a.name}`} className="row-link mono">
                        {a.name}
                      </Link>
                      <div className="cell-sub small muted">{a.description}</div>
                    </td>
                    <td className="mono small">{a.version}</td>
                    <td>
                      {a.enabled ? <Badge tone="ok">enabled</Badge> : <Badge>phase {a.phase ?? "?"}</Badge>}
                    </td>
                    <td className="mono small">{a.modelAlias}</td>
                    <td className="num">{a.tools.length}</td>
                    <td className="num">{a.runCount}</td>
                    <td className="muted">{relativeTime(a.lastRunAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <Panel title="Recent runs" count={runs.data?.items.length}>
        <ErrorNote error={runs.error} />
        <RunsTable runs={runs.data?.items} showAgent />
      </Panel>
    </>
  );
}
