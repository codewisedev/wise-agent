import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronRight, Play } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api, unwrap } from "../api/client";
import { Json } from "../components/Json";
import { Badge, ErrorNote, Field, Panel } from "../components/ui";
import { RunsTable } from "./RunsTable";

type JsonSchema = { properties?: Record<string, { type?: string; description?: string }>; required?: string[] };

/** A starting input document built from the schema's required properties. */
function templateFor(schema: JsonSchema | undefined): string {
  const out: Record<string, unknown> = {};
  for (const key of schema?.required ?? []) {
    const t = schema?.properties?.[key]?.type;
    out[key] = t === "array" ? [] : t === "object" ? {} : t === "number" || t === "integer" ? 0 : "";
  }
  return JSON.stringify(out, null, 2);
}

export function AgentDetailPage() {
  const { agentName = "" } = useParams();
  const navigate = useNavigate();
  const agent = useQuery({
    queryKey: ["agent", agentName],
    queryFn: () => unwrap(api.GET("/agents/{agentName}", { params: { path: { agentName } } })),
  });
  const runs = useQuery({
    queryKey: ["agent", agentName, "runs"],
    queryFn: () => unwrap(api.GET("/agents/{agentName}/runs", { params: { path: { agentName } } })),
    refetchInterval: 5000,
  });
  const projects = useQuery({ queryKey: ["projects", { showArchived: false }], queryFn: () => unwrap(api.GET("/projects")) });

  const def = agent.data?.definition as
    | { input_schema?: JsonSchema; output_schema?: unknown; responsibilities?: string[]; execution_limits?: Record<string, unknown> }
    | undefined;
  const [input, setInput] = useState("");
  const [projectId, setProjectId] = useState("");
  const [parseError, setParseError] = useState<string | null>(null);
  useEffect(() => {
    if (def && !input) setInput(templateFor(def.input_schema));
  }, [def, input]);

  const run = useMutation({
    mutationFn: (body: { input: Record<string, unknown>; projectId?: string }) =>
      unwrap(api.POST("/agents/{agentName}/runs", { params: { path: { agentName } }, body })),
    onSuccess: (r) => navigate(`/runs/${r.id}`),
  });
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    try {
      const parsed = JSON.parse(input) as Record<string, unknown>;
      setParseError(null);
      run.mutate({ input: parsed, ...(projectId ? { projectId } : {}) });
    } catch (err) {
      setParseError(`Input is not valid JSON: ${(err as Error).message}`);
    }
  };

  if (agent.error) return <ErrorNote error={agent.error} />;
  if (!agent.data) return <p className="loading">loading…</p>;
  const a = agent.data;

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/agents">agents</Link>
        <ChevronRight size={12} aria-hidden />
        <span className="mono">{a.name}</span>
      </nav>
      <header className="page-header">
        <div>
          <h1 className="mono">{a.name}</h1>
          <div className="meta">
            <span className="mono small">v{a.version}</span>
            {a.enabled ? <Badge tone="ok">enabled</Badge> : <Badge>phase {a.phase ?? "?"}</Badge>}
            <span className="small">
              model <span className="mono">{a.modelAlias}</span>
            </span>
            {a.cloudAllowed ? <Badge tone="info">cloud allowed</Badge> : <Badge>local only</Badge>}
            <span className="mono small faint" title={a.contentHash}>
              #{a.contentHash.slice(0, 10)}
            </span>
          </div>
          <p className="desc">{a.description}</p>
        </div>
      </header>

      {a.enabled && (
        <Panel title="Run" flush={false}>
          <form onSubmit={onSubmit}>
            <div className="form-grid">
              <Field label="Input (JSON, validated against input_schema)" grow>
                <textarea
                  className="mono"
                  rows={8}
                  value={input}
                  onChange={(e) => setInput(e.target.value)}
                  spellCheck={false}
                />
              </Field>
            </div>
            <div className="form-grid" style={{ marginTop: 12 }}>
              <Field label="Project" hint="Optional: lets the agent read the project's repositories">
                <select value={projectId} onChange={(e) => setProjectId(e.target.value)}>
                  <option value="">none</option>
                  {projects.data?.items.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </Field>
              <button type="submit" className="btn btn-primary" disabled={run.isPending}>
                <Play size={15} aria-hidden /> Start run
              </button>
            </div>
            <ErrorNote error={parseError ?? run.error} />
          </form>
        </Panel>
      )}

      <Panel title="Runs" count={runs.data?.items.length}>
        <ErrorNote error={runs.error} />
        <RunsTable runs={runs.data?.items} />
      </Panel>

      <Panel title="Contract" flush={false}>
        {def?.responsibilities && (
          <ul className="bullets">
            {def.responsibilities.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
        )}
        <p className="small muted" style={{ margin: "12px 0 6px" }}>
          Tools: {a.tools.length ? a.tools.map((t) => <code key={t} style={{ marginRight: 6 }}>{t}</code>) : "none"}
        </p>
        <details>
          <summary>Input schema</summary>
          <Json value={def?.input_schema} label="input schema" />
        </details>
        <details>
          <summary>Output schema</summary>
          <Json value={def?.output_schema} label="output schema" />
        </details>
        <details>
          <summary>Execution limits</summary>
          <Json value={def?.execution_limits} label="limits" />
        </details>
        {a.prompt && (
          <details>
            <summary>System prompt</summary>
            <pre className="json prompt">{a.prompt}</pre>
          </details>
        )}
      </Panel>
    </>
  );
}
