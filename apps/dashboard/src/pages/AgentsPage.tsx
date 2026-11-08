import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { api, unwrap } from "../api/client";
import { Badge, Empty, ErrorNote, Field, Panel, relativeTime } from "../components/ui";
import { starterDefinition } from "./AgentEditor";
import { RunsTable } from "./RunsTable";

function NewAgentForm({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/agents", { body: { definition: starterDefinition(name, description.trim()), prompt: "" } })),
    onSuccess: (a) => {
      void qc.invalidateQueries({ queryKey: ["agents"] });
      onDone();
      navigate(`/agents/${a.name}`);
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  return (
    <form className="panel-body" onSubmit={submit} aria-label="New agent">
      <div className="form-grid">
        <Field label="Name" hint="Lowercase, ending in -agent (e.g. qa-agent)">
          <input className="mono" value={name} onChange={(e) => setName(e.target.value.toLowerCase())} pattern="[a-z][a-z0-9-]*-agent" required placeholder="qa-agent" />
        </Field>
        <Field label="What it does" grow>
          <input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Reviews the plan for test coverage gaps" />
        </Field>
        <button type="submit" className="btn btn-primary" disabled={create.isPending || !name}>
          <Plus size={15} aria-hidden /> Create
        </button>
        <button type="button" className="btn btn-ghost" onClick={onDone}>Cancel</button>
      </div>
      <p className="small faint">It starts disabled: write its prompt and contract, then enable it.</p>
      <ErrorNote error={create.error} />
    </form>
  );
}

export function AgentsPage() {
  const [adding, setAdding] = useState(false);
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
            Specialized agents with versioned contracts. Edit any agent's prompt and contract, or create your own.
          </p>
        </div>
        <button type="button" className="btn btn-primary" onClick={() => setAdding((v) => !v)} aria-expanded={adding}>
          <Plus size={15} aria-hidden /> New agent
        </button>
      </header>

      <Panel title="Registry" count={agents.data ? items.length : undefined}>
        <ErrorNote error={agents.error} />
        {adding && <NewAgentForm onDone={() => setAdding(false)} />}
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
                      {a.enabled ? <Badge tone="ok">enabled</Badge> : <Badge>{a.phase ? `phase ${a.phase}` : "disabled"}</Badge>}
                      {a.source && a.source !== "shipped" && <Badge tone="info" dot={false}>{a.source}</Badge>}
                      {a.updateAvailable && <Badge tone="warn" dot={false}>update</Badge>}
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
