import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, History, Plus, RotateCcw, Save, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, ErrorNote, Field, Panel, relativeTime } from "../components/ui";
import { useSettings } from "./SettingsPanels";

type Detail = Schemas["AgentDetail"];
type Def = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

/** Line diff (LCS) between the shipped prompt and the edited one. */
export function lineDiff(a: string, b: string): { op: " " | "-" | "+"; line: string }[] {
  const x = a.split("\n");
  const y = b.split("\n");
  const n = x.length;
  const m = y.length;
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i]![j] = x[i] === y[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!);
  const out: { op: " " | "-" | "+"; line: string }[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (x[i] === y[j]) { out.push({ op: " ", line: x[i]! }); i++; j++; }
    else if (dp[i + 1]![j]! >= dp[i]![j + 1]!) { out.push({ op: "-", line: x[i]! }); i++; }
    else { out.push({ op: "+", line: y[j]! }); j++; }
  }
  while (i < n) out.push({ op: "-", line: x[i++]! });
  while (j < m) out.push({ op: "+", line: y[j++]! });
  return out;
}

const SOURCE_LABEL = { shipped: "shipped default", edited: "edited in the panel", custom: "created in the panel" } as const;

function JsonField({ label, value, onChange, onValidity, hint }: { label: string; value: unknown; onChange: (v: unknown) => void; onValidity: (ok: boolean) => void; hint?: string }) {
  const [text, setText] = useState(() => JSON.stringify(value ?? {}, null, 2));
  const [err, setErr] = useState<string | null>(null);
  return (
    <Field label={label} hint={err ?? hint} grow>
      <textarea
        className={`mono${err ? " invalid" : ""}`} rows={10} value={text} spellCheck={false}
        onChange={(e) => {
          setText(e.target.value);
          try {
            onChange(JSON.parse(e.target.value));
            setErr(null);
            onValidity(true);
          } catch (x) {
            setErr(`Not valid JSON: ${(x as Error).message}`);
            onValidity(false);
          }
        }}
      />
    </Field>
  );
}

const num = (v: string) => (v === "" ? undefined : Number(v));

/** Edits an agent's prompt and full contract; saving creates a new version. */
export function AgentEditor({ agent }: { agent: Detail }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const tools = useQuery({ queryKey: ["tools"], queryFn: () => unwrap(api.GET("/tools")) });
  const settings = useSettings();
  const aliases = useMemo(() => {
    const models = settings.data?.items.find((s) => s.key === "models")?.value as { aliases?: Record<string, unknown> } | undefined;
    return Object.keys(models?.aliases ?? {});
  }, [settings.data]);
  const [tab, setTab] = useState<"prompt" | "contract" | "model" | "versions">("prompt");
  const [def, setDef] = useState<Def>(agent.definition);
  const [prompt, setPrompt] = useState(agent.prompt);
  const [showDiff, setShowDiff] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [invalid, setInvalid] = useState<Record<string, boolean>>({});
  const validity = (key: string) => (ok: boolean) => setInvalid((m) => ({ ...m, [key]: !ok }));
  const hasInvalid = Object.values(invalid).some(Boolean);
  useEffect(() => {
    if (!dirty) {
      setDef(agent.definition);
      setPrompt(agent.prompt);
    }
  }, [agent.contentHash, dirty]); // eslint-disable-line react-hooks/exhaustive-deps
  const upd = (patch: Def) => { setDef({ ...def, ...patch }); setDirty(true); };
  const sub = (key: string, patch: Def) => upd({ [key]: { ...(def[key] ?? {}), ...patch } });
  const applied = (d: Detail) => {
    qc.setQueryData(["agent", agent.name], d);
    setDef(d.definition);
    setPrompt(d.prompt);
    setInvalid({});
    setDirty(false);
    refresh();
  };
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["agent", agent.name] });
    void qc.invalidateQueries({ queryKey: ["agents"] });
  };
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/agents/{agentName}", { params: { path: { agentName: agent.name } }, body: { definition: def, prompt } })),
    onSuccess: applied,
  });
  const reset = useMutation({
    mutationFn: () => unwrap(api.POST("/agents/{agentName}/reset", { params: { path: { agentName: agent.name } } })),
    onSuccess: applied,
  });
  const dup = useMutation({
    mutationFn: (name: string) => unwrap(api.POST("/agents/{agentName}/duplicate", { params: { path: { agentName: agent.name } }, body: { name } })),
    onSuccess: (a) => { refresh(); navigate(`/agents/${a.name}`); },
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/agents/{agentName}", { params: { path: { agentName: agent.name } } })),
    onSuccess: () => { refresh(); navigate("/agents"); },
  });
  const versions = useQuery({
    queryKey: ["agent", agent.name, "versions", agent.contentHash],
    queryFn: () => unwrap(api.GET("/agents/{agentName}/versions", { params: { path: { agentName: agent.name } } })),
    enabled: tab === "versions",
  });
  const restore = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/agents/{agentName}/versions/{versionId}/restore", { params: { path: { agentName: agent.name, versionId: id } } })),
    onSuccess: applied,
  });
  const mc = def.model_config ?? {};
  const lim = def.execution_limits ?? {};
  const eh = def.error_handling ?? {};
  const consumes: [string, string][] = Object.entries(def.consumes ?? {});

  return (
    <Panel
      title="Configure"
      actions={
        <div className="panel-actions">
          {agent.source && <Badge tone={agent.source === "shipped" ? "neutral" : "info"} dot={false}>{SOURCE_LABEL[agent.source]}</Badge>}
          {agent.updateAvailable && <Badge tone="warn" dot={false}>newer shipped version</Badge>}
          {agent.source === "edited" && (
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => window.confirm("Reset to the shipped default? Your edit stays in Versions.") && reset.mutate()}>
              <RotateCcw size={13} aria-hidden /> Reset
            </button>
          )}
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => { const n = window.prompt("Name for the copy (must end in -agent)", `${agent.name.replace(/-agent$/, "")}-copy-agent`); if (n) dup.mutate(n); }}>
            <Copy size={13} aria-hidden /> Duplicate
          </button>
          {agent.source === "custom" && (
            <button type="button" className="btn btn-ghost btn-icon" aria-label="Delete agent" onClick={() => window.confirm(`Delete ${agent.name}?`) && del.mutate()}>
              <Trash2 size={14} aria-hidden />
            </button>
          )}
          <button type="button" className="btn btn-primary btn-sm" onClick={() => save.mutate()} disabled={!dirty || hasInvalid || save.isPending} title={hasInvalid ? "Fix the invalid JSON first" : undefined}>
            <Save size={13} aria-hidden /> Save
          </button>
        </div>
      }
    >
      <div className="tabs" role="tablist" aria-label="Agent configuration">
        {(["prompt", "contract", "model", "versions"] as const).map((t) => (
          <button key={t} type="button" role="tab" className="tab" aria-selected={tab === t} onClick={() => setTab(t)}>
            {{ prompt: "Prompt", contract: "Contract", model: "Model & limits", versions: "Versions" }[t]}
          </button>
        ))}
      </div>
      <div className="panel-body agent-editor">
        {tab === "prompt" && (
          <>
            <Field label="System prompt" hint="Applies to new runs as soon as you save." grow>
              <textarea className="mono" rows={22} value={prompt} onChange={(e) => { setPrompt(e.target.value); setDirty(true); }} spellCheck={false} aria-label="System prompt" />
            </Field>
            {agent.shippedPrompt != null && agent.shippedPrompt !== prompt && (
              <>
                <button type="button" className="btn btn-ghost btn-sm" onClick={() => setShowDiff((v) => !v)} aria-pressed={showDiff}>
                  {showDiff ? "Hide" : "Show"} changes from the shipped prompt
                </button>
                {showDiff && (
                  <pre className="json diff" aria-label="Prompt diff">
                    {lineDiff(agent.shippedPrompt, prompt).map((d, i) => (
                      <span key={i} className={d.op === "+" ? "add" : d.op === "-" ? "del" : ""}>{d.op} {d.line}{"\n"}</span>
                    ))}
                  </pre>
                )}
              </>
            )}
          </>
        )}
        {tab === "contract" && (
          <div className="settings-form">
            <label className="checkbox"><input type="checkbox" checked={!!def.enabled} onChange={(e) => upd({ enabled: e.target.checked })} /> Enabled (can run and be used in workflows)</label>
            <Field label="Description" grow><input value={def.description ?? ""} onChange={(e) => upd({ description: e.target.value })} /></Field>
            <Field label="Responsibilities" hint="One per line" grow>
              <textarea rows={4} value={(def.responsibilities ?? []).join("\n")} onChange={(e) => upd({ responsibilities: e.target.value.split("\n").filter((l) => l.trim()) })} />
            </Field>
            <Field label="Version label" hint="Your own label; every save is versioned anyway"><input value={def.version ?? ""} onChange={(e) => upd({ version: e.target.value })} /></Field>
            <h3>Tools</h3>
            <div className="checks">
              {tools.data?.items.map((t) => (
                <label key={t.name} className="checkbox small" title={t.description}>
                  <input type="checkbox" checked={(def.tools ?? []).includes(t.name)}
                    onChange={(e) => upd({ tools: e.target.checked ? [...(def.tools ?? []), t.name] : (def.tools ?? []).filter((x: string) => x !== t.name) })} />
                  <span className="mono">{t.name}</span>
                </label>
              ))}
            </div>
            <Field label="Permissions" hint="Comma-separated, e.g. project:read, repo:read" grow>
              <input value={(def.permissions ?? []).join(", ")} onChange={(e) => upd({ permissions: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })} />
            </Field>
            <Field label="May change only (workspace files)" hint="Comma-separated globs, e.g. docs/**, *.md. Empty = no limit beyond its tools. Enforced by Wise." grow>
              <input className="mono" value={(def.write_paths ?? []).join(", ")}
                onChange={(e) => {
                  const v = e.target.value.split(",").map((s) => s.trim()).filter(Boolean);
                  upd({ write_paths: v.length ? v : undefined });
                }} />
            </Field>
            <h3>Workflow wiring</h3>
            <Field label="Produces (artifact kind)" hint="How its output is stored and passed to later steps">
              <input className="mono" value={def.produces ?? ""} onChange={(e) => upd({ produces: e.target.value || undefined })} />
            </Field>
            <p className="small faint">Consumes: input property ← artifact kind produced by an earlier step</p>
            {consumes.map(([prop, kind], i) => (
              <div key={i} className="form-grid settings-row">
                <input aria-label="Input property" className="mono" value={prop} onChange={(e) => { const c = [...consumes]; c[i] = [e.target.value, kind]; upd({ consumes: Object.fromEntries(c) }); }} />
                <span className="faint">←</span>
                <input aria-label="Artifact kind" className="mono" value={kind} onChange={(e) => { const c = [...consumes]; c[i] = [prop, e.target.value]; upd({ consumes: Object.fromEntries(c) }); }} />
                <button type="button" className="btn btn-ghost btn-icon" aria-label="Remove" onClick={() => upd({ consumes: Object.fromEntries(consumes.filter((_, j) => j !== i)) })}><X size={13} aria-hidden /></button>
              </div>
            ))}
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => upd({ consumes: Object.fromEntries([...consumes, ["input_name", "artifact_kind"]]) })}><Plus size={13} aria-hidden /> Input from an earlier step</button>
            <div className="form-grid">
              <JsonField key={`in-${agent.contentHash}`} label="Input schema" value={def.input_schema} onChange={(v) => upd({ input_schema: v })} onValidity={validity("input")} hint="JSON Schema the run input must match" />
              <JsonField key={`out-${agent.contentHash}`} label="Output schema" value={def.output_schema} onChange={(v) => upd({ output_schema: v })} onValidity={validity("output")} hint="The model's final answer must match it" />
            </div>
            <Field label="Evaluation criteria" hint="One per line" grow>
              <textarea rows={3} value={(def.evaluation?.criteria ?? []).join("\n")} onChange={(e) => sub("evaluation", { criteria: e.target.value.split("\n").filter((l) => l.trim()) })} />
            </Field>
          </div>
        )}
        {tab === "model" && (
          <div className="settings-form">
            <div className="form-grid">
              <Field label="Model alias" hint="Defined in Settings → Models">
                <select value={mc.alias ?? ""} onChange={(e) => sub("model_config", { alias: e.target.value })}>
                  {!aliases.includes(mc.alias) && mc.alias && <option value={mc.alias}>{mc.alias} (missing)</option>}
                  {aliases.map((a) => <option key={a} value={a}>{a}</option>)}
                </select>
              </Field>
              <Field label="Temperature"><input type="number" min={0} max={2} step="0.1" value={mc.temperature ?? ""} onChange={(e) => sub("model_config", { temperature: num(e.target.value) })} /></Field>
              <Field label="Max output tokens"><input type="number" min={1} value={mc.max_output_tokens ?? ""} onChange={(e) => sub("model_config", { max_output_tokens: num(e.target.value) })} /></Field>
            </div>
            <p className="small faint">Fallback aliases, tried in order when the main one fails</p>
            <div className="checks">
              {aliases.filter((a) => a !== mc.alias).map((a) => (
                <label key={a} className="checkbox small">
                  <input type="checkbox" checked={(mc.fallbacks ?? []).includes(a)}
                    onChange={(e) => sub("model_config", { fallbacks: e.target.checked ? [...(mc.fallbacks ?? []), a] : (mc.fallbacks ?? []).filter((x: string) => x !== a) })} />
                  <span className="mono">{a}</span>
                </label>
              ))}
            </div>
            <h3>Limits per run</h3>
            <div className="form-grid">
              <Field label="Turns"><input type="number" min={1} max={200} value={lim.max_turns ?? ""} onChange={(e) => sub("execution_limits", { max_turns: num(e.target.value) })} /></Field>
              <Field label="Tool calls"><input type="number" min={0} value={lim.max_tool_calls ?? ""} onChange={(e) => sub("execution_limits", { max_tool_calls: num(e.target.value) })} /></Field>
              <Field label="Tokens"><input type="number" min={0} value={lim.max_tokens ?? ""} onChange={(e) => sub("execution_limits", { max_tokens: num(e.target.value) })} /></Field>
              <Field label="Cost (USD)"><input type="number" min={0} step="0.1" value={lim.max_cost_usd ?? ""} onChange={(e) => sub("execution_limits", { max_cost_usd: num(e.target.value) })} /></Field>
              <Field label="Timeout (s)"><input type="number" min={1} value={lim.timeout_s ?? ""} onChange={(e) => sub("execution_limits", { timeout_s: num(e.target.value) })} /></Field>
            </div>
            <div className="form-grid">
              <Field label="Attempts" hint="Runs retried after a failure"><input type="number" min={1} max={10} value={eh.max_attempts ?? ""} onChange={(e) => sub("error_handling", { max_attempts: num(e.target.value) })} /></Field>
              <Field label="Invalid-output retries"><input type="number" min={0} max={10} value={eh.invalid_output_retries ?? ""} onChange={(e) => sub("error_handling", { invalid_output_retries: num(e.target.value) })} /></Field>
            </div>
          </div>
        )}
        {tab === "versions" && (
          <ul className="list history">
            {versions.data?.items.map((v) => (
              <li key={v.id} className="small">
                <History size={13} aria-hidden />
                <span className="mono">{v.version}</span>
                <span className="mono faint">#{v.contentHash.slice(0, 10)}</span>
                <Badge dot={false}>{v.source === "code" ? "shipped" : "panel"}</Badge>
                {v.current && <Badge tone="ok" dot={false}>current</Badge>}
                <span className="faint">{relativeTime(v.createdAt)}</span>
                <span className="spacer" />
                {!v.current && <button type="button" className="btn btn-ghost btn-sm" onClick={() => restore.mutate(v.id)}>Restore</button>}
              </li>
            ))}
          </ul>
        )}
        <ErrorNote error={save.error ?? reset.error ?? dup.error ?? del.error ?? restore.error} />
      </div>
    </Panel>
  );
}

/** A minimal valid definition for a new agent (disabled until it has a prompt). */
export function starterDefinition(name: string, description: string): Def {
  return {
    name, version: "1.0.0", description: description || name, enabled: false,
    responsibilities: [description || "Describe what this agent does"],
    produces: name.replace(/-agent$/, "").replace(/-/g, "_") + "_output",
    input_schema: { type: "object", additionalProperties: false, required: ["request"], properties: { request: { type: "string", minLength: 3 }, feedback: { type: "string" }, previous_output: { type: "object" } } },
    output_schema: { type: "object", additionalProperties: false, required: ["summary"], properties: { summary: { type: "string" } } },
    tools: ["get_project_context", "list_files", "read_file", "search_code"], permissions: ["project:read", "repo:read"],
    model_config: { alias: "reasoning", fallbacks: ["local"], temperature: 0.2, max_output_tokens: 4096 },
    execution_limits: { max_turns: 12, max_tool_calls: 20, max_tokens: 200000, max_cost_usd: 1, timeout_s: 600 },
    error_handling: { max_attempts: 2, invalid_output_retries: 2 },
    evaluation: { criteria: [] },
  };
}
