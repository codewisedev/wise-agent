import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Braces, History, Plus, RotateCcw, Save, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, ErrorNote, Field, Panel, relativeTime } from "../components/ui";

type Setting = Schemas["Setting"];
type Json = Record<string, unknown>;

/* eslint-disable @typescript-eslint/no-explicit-any */
type ModelsValue = {
  providers?: Record<string, { type: string; base_url?: string; api_key_env?: string; cloud?: boolean; timeout_s?: number; input_per_mtok?: number; output_per_mtok?: number }>;
  aliases?: Record<string, { description?: string; targets: { provider: string; model: string }[] }>;
  cache?: { enabled?: boolean; ttl_hours?: number };
  embedding?: { provider: string; model: string; dimensions?: number; batch_size?: number; allow_cloud?: boolean };
};

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/** Embedding models that work with each kind of endpoint (any other model id can be typed). */
function embeddingSuggestions(baseUrl: string): string[] {
  if (baseUrl.includes("generativelanguage.googleapis.com")) return ["gemini-embedding-001", "text-embedding-004"];
  if (baseUrl.includes("api.openai.com")) return ["text-embedding-3-small", "text-embedding-3-large"];
  return ["text-embedding-nomic-embed-text-v1.5", "text-embedding-bge-m3", "text-embedding-3-small"];
}

function EmbeddingSection({ value, set, choices }: {
  value: ModelsValue; set: (v: ModelsValue) => void;
  choices: { name: string; baseUrl: string; cloud: boolean }[];
}) {
  const status = useQuery({
    queryKey: ["embedding-status"], queryFn: () => unwrap(api.GET("/context/embeddings")),
    refetchInterval: (q) => ((q.state.data?.pending ?? 0) > 0 ? 3000 : 15000),
  });
  const e = value.embedding;
  const chosen = choices.find((c) => c.name === e?.provider);
  const put = (patch: Partial<NonNullable<ModelsValue["embedding"]>>) =>
    set({ ...value, embedding: { provider: e?.provider ?? choices[0]?.name ?? "", model: e?.model ?? "", dimensions: e?.dimensions ?? 768, ...e, ...patch } });
  const st = status.data;
  const total = (st?.embedded ?? 0) + (st?.pending ?? 0);
  return (
    <>
      <h3>Embeddings (semantic search)</h3>
      <p className="small faint">
        Lets search and agents find code and docs by meaning, not only by words. Chunks of your repositories (and search
        queries) are sent to this model; secret files are never sent.
      </p>
      {!e ? (
        <button type="button" className="btn btn-sm" disabled={choices.length === 0} onClick={() => put({})}>
          <Plus size={13} aria-hidden /> Turn on semantic search
        </button>
      ) : (
        <div className="settings-group">
          <div className="form-grid">
            <Field label="Account or provider" hint="An OpenAI-compatible account: Google AI Studio, OpenAI, LM Studio…">
              <select value={e.provider} onChange={(ev) => put({ provider: ev.target.value, allow_cloud: false })}>
                {!chosen && <option value={e.provider}>{e.provider || "choose…"} (missing)</option>}
                {choices.map((c) => <option key={c.name} value={c.name}>{c.name}{c.cloud ? " · cloud" : " · local"}</option>)}
              </select>
            </Field>
            <Field label="Model" grow>
              <input className="mono" list="embedding-models" value={e.model} onChange={(ev) => put({ model: ev.target.value })} placeholder="gemini-embedding-001" />
              <datalist id="embedding-models">{embeddingSuggestions(chosen?.baseUrl ?? "").map((m) => <option key={m} value={m} />)}</datalist>
            </Field>
            <Field label="Dimensions" hint="0 = model default">
              <input type="number" min={0} max={4096} value={e.dimensions ?? 0} onChange={(ev) => put({ dimensions: Number(ev.target.value) })} style={{ width: 110 }} />
            </Field>
          </div>
          {chosen?.cloud && (
            <label className="checkbox">
              <input type="checkbox" checked={!!e.allow_cloud} onChange={(ev) => put({ allow_cloud: ev.target.checked })} />
              <span>Allow sending code and docs from my repositories to <strong>{chosen.name}</strong> ({hostOf(chosen.baseUrl)})</span>
            </label>
          )}
          <div className="actions" style={{ margin: 0 }}>
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => { const next = { ...value }; delete next.embedding; set(next); }}>
              <Trash2 size={13} aria-hidden /> Turn off
            </button>
          </div>
          {st && st.configured && (
            <p className="small" role="status">
              {st.available
                ? <><Badge tone="ok" dot={false}>on</Badge> {st.embedded} of {total} chunks embedded{st.pending > 0 ? " — working…" : ""}</>
                : <><Badge tone="warn" dot={false}>paused</Badge> {st.problem}</>}
              {st.lastError && <span className="error small"> Last error: {st.lastError}</span>}
            </p>
          )}
          <p className="small faint">Changes apply after Save; switching model or dimensions re-embeds everything.</p>
        </div>
      )}
    </>
  );
}
type PolicyValue = { cloud_allowed_agents?: string[]; daily_budget_usd?: Record<string, number>; max_retries?: number };
type WorkflowValue = { max_revisions?: number };
type BoardValue = { columns: { name: string; role?: string; color?: string }[] };

export function useSettings() {
  return useQuery({ queryKey: ["settings"], queryFn: () => unwrap(api.GET("/settings")) });
}

function move<T>(list: T[], i: number, d: -1 | 1): T[] {
  const next = [...list];
  const [x] = next.splice(i, 1);
  next.splice(i + d, 0, x!);
  return next;
}

/**
 * Shell shared by every setting: form or raw JSON, save with optimistic
 * version, reset to the shipped default, and the version history.
 */
function SettingCard<T>({ setting, children }: {
  setting: Setting;
  children: (value: T, set: (v: T) => void) => ReactNode;
}) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState<T>(setting.value as T);
  const [raw, setRaw] = useState(false);
  const [text, setText] = useState("");
  const [parseErr, setParseErr] = useState<string | null>(null);
  const [showHistory, setShowHistory] = useState(false);
  const [dirty, setDirty] = useState(false);
  // The version the draft was loaded from: sent on save so a concurrent change is refused (409).
  const [baseVersion, setBaseVersion] = useState(setting.version);
  useEffect(() => {
    if (!dirty) {
      setDraft(setting.value as T);
      setBaseVersion(setting.version);
    }
  }, [setting.value, setting.version, dirty]);
  const set = (v: T) => {
    setDraft(v);
    setDirty(true);
  };
  const refresh = () => void qc.invalidateQueries({ queryKey: ["settings"] });
  const save = useMutation({
    mutationFn: (value: unknown) => unwrap(api.PUT("/settings/{settingKey}", {
      params: { path: { settingKey: setting.key } }, body: { value, version: baseVersion },
    })),
    onSuccess: () => {
      setDirty(false);
      refresh();
    },
  });
  const reset = useMutation({
    mutationFn: () => unwrap(api.POST("/settings/{settingKey}/reset", { params: { path: { settingKey: setting.key } } })),
    onSuccess: () => {
      setDirty(false);
      refresh();
    },
  });
  const history = useQuery({
    queryKey: ["settings", setting.key, "history", setting.version],
    queryFn: () => unwrap(api.GET("/settings/{settingKey}/history", { params: { path: { settingKey: setting.key } } })),
    enabled: showHistory,
  });
  const toggleRaw = () => {
    if (!raw) {
      setText(JSON.stringify(draft, null, 2));
      setParseErr(null);
    } else {
      try {
        set(JSON.parse(text) as T);
      } catch (e) {
        setParseErr(`Not valid JSON: ${(e as Error).message}`);
        return;
      }
    }
    setRaw(!raw);
  };
  const onSave = () => {
    if (raw) {
      try {
        save.mutate(JSON.parse(text));
      } catch (e) {
        setParseErr(`Not valid JSON: ${(e as Error).message}`);
      }
      return;
    }
    save.mutate(draft);
  };
  const isDefault = JSON.stringify(setting.value) === JSON.stringify(setting.default);

  return (
    <Panel
      title={setting.title}
      actions={
        <div className="panel-actions">
          <span className="small faint">v{setting.version} · {relativeTime(setting.updatedAt)}{isDefault ? " · default" : ""}</span>
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => setShowHistory((v) => !v)} aria-pressed={showHistory}>
            <History size={13} aria-hidden /> History
          </button>
          <button type="button" className="btn btn-ghost btn-sm" onClick={toggleRaw} aria-pressed={raw}>
            <Braces size={13} aria-hidden /> {raw ? "Form" : "JSON"}
          </button>
          {!isDefault && (
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => window.confirm(`Reset ${setting.title} to the shipped default?`) && reset.mutate()}>
              <RotateCcw size={13} aria-hidden /> Reset
            </button>
          )}
          <button type="button" className="btn btn-primary btn-sm" onClick={onSave} disabled={(!dirty && !raw) || save.isPending}>
            <Save size={13} aria-hidden /> Save
          </button>
        </div>
      }
    >
      <div className="panel-body">
        <p className="small muted" style={{ marginTop: 0 }}>{setting.description} Changes apply immediately.</p>
        {raw ? (
          <textarea className="mono" rows={16} value={text} onChange={(e) => { setText(e.target.value); setDirty(true); }} spellCheck={false} aria-label={`${setting.title} JSON`} />
        ) : (
          children(draft, set)
        )}
        <ErrorNote error={parseErr ?? save.error ?? reset.error} />
        {showHistory && (
          <ul className="list history">
            {history.data?.items.length === 0 && <li className="small faint">No earlier versions.</li>}
            {history.data?.items.map((h) => (
              <li key={h.version} className="small">
                <span className="mono">v{h.version}</span>
                <span className="faint">{relativeTime(h.createdAt)}</span>
                <span className="spacer" />
                <button type="button" className="btn btn-ghost btn-sm" onClick={() => { set(h.value as T); setRaw(false); }}>Load into editor</button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Panel>
  );
}

function ModelsEditor({ value, set }: { value: ModelsValue; set: (v: ModelsValue) => void }) {
  const accounts = useQuery({ queryKey: ["accounts"], queryFn: () => unwrap(api.GET("/accounts")) });
  const providers = value.providers ?? {};
  const aliases = value.aliases ?? {};
  const targetsChoices = [
    ...Object.keys(providers),
    ...(accounts.data?.items.filter((a) => ["claude_code", "codex_cli", "openai_compatible"].includes(a.kind)).map((a) => a.name) ?? []),
  ];
  const setAlias = (name: string, a: ModelsValue["aliases"] extends Record<string, infer A> | undefined ? A : never) => set({ ...value, aliases: { ...aliases, [name]: a } });
  const [newAlias, setNewAlias] = useState("");
  const [newProvider, setNewProvider] = useState("");

  return (
    <div className="settings-form">
      <h3>Aliases</h3>
      <p className="small faint">Agents name an alias (e.g. reasoning); the gateway tries its targets in order. Targets can be built-in providers or any model account.</p>
      {Object.entries(aliases).map(([name, a]) => (
        <fieldset key={name} className="settings-group">
          <legend><code className="chip">{name}</code></legend>
          <Field label="Description" grow>
            <input value={a.description ?? ""} onChange={(e) => setAlias(name, { ...a, description: e.target.value })} />
          </Field>
          {a.targets.map((t, i) => (
            <div key={i} className="form-grid settings-row">
              <span className="mono small faint">{i + 1}.</span>
              <select aria-label="Provider or account" value={t.provider} onChange={(e) => setAlias(name, { ...a, targets: a.targets.map((x, j) => (j === i ? { ...x, provider: e.target.value } : x)) })}>
                {!targetsChoices.includes(t.provider) && <option value={t.provider}>{t.provider} (missing)</option>}
                {targetsChoices.map((p) => <option key={p} value={p}>{p}</option>)}
              </select>
              <input aria-label="Model" placeholder="model (empty = default)" value={t.model} onChange={(e) => setAlias(name, { ...a, targets: a.targets.map((x, j) => (j === i ? { ...x, model: e.target.value } : x)) })} />
              <button type="button" className="btn btn-ghost btn-icon" aria-label="Move up" disabled={i === 0} onClick={() => setAlias(name, { ...a, targets: move(a.targets, i, -1) })}><ArrowUp size={13} aria-hidden /></button>
              <button type="button" className="btn btn-ghost btn-icon" aria-label="Move down" disabled={i === a.targets.length - 1} onClick={() => setAlias(name, { ...a, targets: move(a.targets, i, 1) })}><ArrowDown size={13} aria-hidden /></button>
              <button type="button" className="btn btn-ghost btn-icon" aria-label="Remove target" disabled={a.targets.length === 1} onClick={() => setAlias(name, { ...a, targets: a.targets.filter((_, j) => j !== i) })}><Trash2 size={13} aria-hidden /></button>
            </div>
          ))}
          <div className="actions" style={{ marginTop: 6 }}>
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => setAlias(name, { ...a, targets: [...a.targets, { provider: targetsChoices[0] ?? "", model: "" }] })}><Plus size={13} aria-hidden /> Fallback</button>
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => { const next = { ...aliases }; delete next[name]; set({ ...value, aliases: next }); }}><Trash2 size={13} aria-hidden /> Remove alias</button>
          </div>
        </fieldset>
      ))}
      <div className="form-grid">
        <input aria-label="New alias name" placeholder="new alias, e.g. fast" value={newAlias} onChange={(e) => setNewAlias(e.target.value.toLowerCase())} />
        <button type="button" className="btn btn-sm" disabled={!newAlias || !!aliases[newAlias] || targetsChoices.length === 0}
          onClick={() => { setAlias(newAlias, { targets: [{ provider: targetsChoices[0]!, model: "" }] }); setNewAlias(""); }}>
          <Plus size={13} aria-hidden /> Add alias
        </button>
      </div>

      <h3>Built-in providers</h3>
      <p className="small faint">Endpoints without stored credentials (keys come from accounts). ${"{ENV}"} references are expanded.</p>
      {Object.entries(providers).map(([name, p]) => (
        <div key={name} className="form-grid settings-row">
          <code className="chip">{name}</code>
          <select aria-label="Type" value={p.type} onChange={(e) => set({ ...value, providers: { ...providers, [name]: { ...p, type: e.target.value } } })}>
            <option value="openai-compatible">openai-compatible</option>
            <option value="claude-code">claude-code</option>
          </select>
          {p.type === "openai-compatible" && (
            <input aria-label="Base URL" placeholder="base URL" value={p.base_url ?? ""} onChange={(e) => set({ ...value, providers: { ...providers, [name]: { ...p, base_url: e.target.value } } })} />
          )}
          <label className="checkbox small"><input type="checkbox" checked={p.cloud ?? false} onChange={(e) => set({ ...value, providers: { ...providers, [name]: { ...p, cloud: e.target.checked } } })} /> cloud</label>
          <input aria-label="Timeout (s)" type="number" min={0} style={{ width: 90 }} placeholder="timeout s" value={p.timeout_s ?? ""} onChange={(e) => set({ ...value, providers: { ...providers, [name]: { ...p, timeout_s: e.target.value ? Number(e.target.value) : undefined } } })} />
          <button type="button" className="btn btn-ghost btn-icon" aria-label={`Remove ${name}`} onClick={() => { const next = { ...providers }; delete next[name]; set({ ...value, providers: next }); }}><Trash2 size={13} aria-hidden /></button>
        </div>
      ))}
      <div className="form-grid">
        <input aria-label="New provider name" placeholder="new provider name" value={newProvider} onChange={(e) => setNewProvider(e.target.value.toLowerCase())} />
        <button type="button" className="btn btn-sm" disabled={!newProvider || !!providers[newProvider]}
          onClick={() => { set({ ...value, providers: { ...providers, [newProvider]: { type: "openai-compatible", base_url: "http://host.docker.internal:1234/v1", cloud: false } } }); setNewProvider(""); }}>
          <Plus size={13} aria-hidden /> Add provider
        </button>
      </div>

      <EmbeddingSection
        value={value} set={set}
        choices={[
          ...Object.entries(providers).filter(([, p]) => p.type === "openai-compatible").map(([n, p]) => ({ name: n, baseUrl: p.base_url ?? "", cloud: !!p.cloud })),
          ...(accounts.data?.items.filter((a) => a.kind === "openai_compatible" && a.enabled).map((a) => ({ name: a.name, baseUrl: a.baseUrl, cloud: a.cloud })) ?? []),
        ]}
      />

      <h3>Response cache</h3>
      <div className="form-grid">
        <label className="checkbox"><input type="checkbox" checked={value.cache?.enabled ?? false} onChange={(e) => set({ ...value, cache: { ...value.cache, enabled: e.target.checked } })} /> Cache deterministic (temperature 0) responses</label>
        <Field label="Keep for (hours)">
          <input type="number" min={1} value={value.cache?.ttl_hours ?? 24} onChange={(e) => set({ ...value, cache: { ...value.cache, ttl_hours: Number(e.target.value) } })} />
        </Field>
      </div>
    </div>
  );
}

function PolicyEditor({ value, set }: { value: PolicyValue; set: (v: PolicyValue) => void }) {
  const agents = useQuery({ queryKey: ["agents"], queryFn: () => unwrap(api.GET("/agents")) });
  const allowed = value.cloud_allowed_agents ?? [];
  const all = allowed.includes("*");
  const budgets = value.daily_budget_usd ?? {};
  const names = agents.data?.items.map((a) => a.name) ?? [];
  return (
    <div className="settings-form">
      <h3>Cloud models</h3>
      <p className="small faint">Agents not ticked only use local models; their data never leaves this machine.</p>
      <label className="checkbox"><input type="checkbox" checked={all} onChange={(e) => set({ ...value, cloud_allowed_agents: e.target.checked ? ["*"] : [] })} /> All agents</label>
      {!all && (
        <div className="checks">
          {names.map((n) => (
            <label key={n} className="checkbox small">
              <input type="checkbox" checked={allowed.includes(n)} onChange={(e) => set({ ...value, cloud_allowed_agents: e.target.checked ? [...allowed, n] : allowed.filter((x) => x !== n) })} />
              <span className="mono">{n}</span>
            </label>
          ))}
        </div>
      )}
      <h3>Daily budgets (USD)</h3>
      <div className="checks">
        {["default", ...names].map((n) => (
          <label key={n} className="field budget">
            <span className="mono">{n}</span>
            <input type="number" min={0} step="0.5" value={budgets[n] ?? ""} placeholder={n === "default" ? "none" : "default"}
              onChange={(e) => {
                const next = { ...budgets };
                if (e.target.value === "") delete next[n];
                else next[n] = Number(e.target.value);
                set({ ...value, daily_budget_usd: next });
              }} />
          </label>
        ))}
      </div>
      <Field label="Retries per target for transient errors">
        <input type="number" min={0} max={10} value={value.max_retries ?? 2} onChange={(e) => set({ ...value, max_retries: Number(e.target.value) })} />
      </Field>
    </div>
  );
}

function WorkflowEditor({ value, set }: { value: WorkflowValue; set: (v: WorkflowValue) => void }) {
  return (
    <div className="form-grid">
      <Field label="Rejections allowed per review" hint="After this many “request changes”, the workflow fails. 0 = the first rejection fails it.">
        <input type="number" min={0} max={50} value={value.max_revisions ?? 5} onChange={(e) => set({ ...value, max_revisions: Number(e.target.value) })} />
      </Field>
    </div>
  );
}

export const ROLES = [
  { v: "", l: "no role" }, { v: "backlog", l: "backlog" }, { v: "todo", l: "to do (unmapped imports)" }, { v: "in_progress", l: "in progress (runs start here)" },
  { v: "review", l: "review (waiting for you)" }, { v: "done", l: "done" },
];

export function ColumnsEditor({ value, set }: { value: BoardValue; set: (v: BoardValue) => void }) {
  const cols = value.columns ?? [];
  const put = (i: number, c: BoardValue["columns"][number]) => set({ columns: cols.map((x, j) => (j === i ? c : x)) });
  return (
    <div className="settings-form">
      {cols.map((c, i) => (
        <div key={i} className="form-grid settings-row">
          <input aria-label="Column name" value={c.name} onChange={(e) => put(i, { ...c, name: e.target.value })} maxLength={40} />
          <select aria-label="Role" value={c.role ?? ""} onChange={(e) => put(i, { ...c, role: e.target.value || undefined })}>
            {ROLES.map((r) => <option key={r.v} value={r.v}>{r.l}</option>)}
          </select>
          <input aria-label="Color" type="color" value={c.color || "#64748b"} onChange={(e) => put(i, { ...c, color: e.target.value })} />
          <button type="button" className="btn btn-ghost btn-icon" aria-label="Move up" disabled={i === 0} onClick={() => set({ columns: move(cols, i, -1) })}><ArrowUp size={13} aria-hidden /></button>
          <button type="button" className="btn btn-ghost btn-icon" aria-label="Move down" disabled={i === cols.length - 1} onClick={() => set({ columns: move(cols, i, 1) })}><ArrowDown size={13} aria-hidden /></button>
          <button type="button" className="btn btn-ghost btn-icon" aria-label="Remove column" disabled={cols.length === 1} onClick={() => set({ columns: cols.filter((_, j) => j !== i) })}><Trash2 size={13} aria-hidden /></button>
        </div>
      ))}
      <button type="button" className="btn btn-ghost btn-sm" onClick={() => set({ columns: [...cols, { name: `Column ${cols.length + 1}` }] })}><Plus size={13} aria-hidden /> Add column</button>
    </div>
  );
}

type IndexingValue = {
  exclude: string[]; include: string[]; docs: string[]; max_file_kb: number; max_files: number;
  chunk_lines: number; auto_index: boolean; check_interval_minutes: number; secret_patterns: string[];
};

function Lines({ label, hint, value, onChange, rows = 6 }: { label: string; hint?: string; value: string[]; onChange: (v: string[]) => void; rows?: number }) {
  // The raw text is kept while typing (new lines, trailing spaces); the list is derived from it.
  const [text, setText] = useState(value.join("\n"));
  const parsed = (t: string) => t.split("\n").map((l) => l.trim()).filter(Boolean);
  useEffect(() => {
    if (parsed(text).join("\n") !== value.join("\n")) setText(value.join("\n"));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);
  return (
    <Field label={label} hint={hint} grow>
      <textarea className="mono" rows={rows} value={text} spellCheck={false}
        onChange={(e) => { setText(e.target.value); onChange(parsed(e.target.value)); }} />
    </Field>
  );
}

function IndexingEditor({ value, set }: { value: IndexingValue; set: (v: IndexingValue) => void }) {
  const num = (k: keyof IndexingValue) => (e: React.ChangeEvent<HTMLInputElement>) => set({ ...value, [k]: Number(e.target.value) });
  return (
    <div className="settings-form">
      <label className="checkbox"><input type="checkbox" checked={value.auto_index} onChange={(e) => set({ ...value, auto_index: e.target.checked })} /> Re-index automatically when a repository's HEAD moves</label>
      <div className="form-grid">
        <Field label="Check every (minutes)"><input type="number" min={1} max={1440} value={value.check_interval_minutes} onChange={num("check_interval_minutes")} /></Field>
        <Field label="Max file size (KB)"><input type="number" min={1} max={4096} value={value.max_file_kb} onChange={num("max_file_kb")} /></Field>
        <Field label="Max files per repository"><input type="number" min={1} max={200000} value={value.max_files} onChange={num("max_files")} /></Field>
        <Field label="Chunk size (lines)"><input type="number" min={10} max={400} value={value.chunk_lines} onChange={num("chunk_lines")} /></Field>
      </div>
      <div className="form-grid">
        <Lines label="Exclude" hint=".gitignore syntax, one per line; the repository's own .gitignore always applies" value={value.exclude} onChange={(v) => set({ ...value, exclude: v })} rows={10} />
        <Lines label="Include only (optional)" hint="Empty = every file not excluded" value={value.include} onChange={(v) => set({ ...value, include: v })} rows={10} />
        <Lines label="Documentation" hint="Split by heading and searched as docs" value={value.docs} onChange={(v) => set({ ...value, docs: v })} rows={10} />
      </div>
      <Lines label="Extra secret patterns" hint="Regular expressions; files that match are never indexed or shown. The built-in guard (.env files, keys, tokens) always applies." value={value.secret_patterns} onChange={(v) => set({ ...value, secret_patterns: v })} rows={3} />
    </div>
  );
}

type MemoryValue = {
  retrieval_k: number; max_context_chars: number; extract_on_review_comments: boolean;
  extract_on_completed_workflows: boolean; duplicate_similarity: number; skip_agents: string[];
};

function MemoryEditor({ value, set }: { value: MemoryValue; set: (v: MemoryValue) => void }) {
  return (
    <div className="settings-form">
      <h3>What agents are given</h3>
      <div className="form-grid">
        <Field label="Relevant memories per run" hint="Rules are always given; 0 = rules only">
          <input type="number" min={0} max={30} value={value.retrieval_k} onChange={(e) => set({ ...value, retrieval_k: Number(e.target.value) })} />
        </Field>
        <Field label="Context size limit (characters)">
          <input type="number" min={500} max={40000} step={500} value={value.max_context_chars} onChange={(e) => set({ ...value, max_context_chars: Number(e.target.value) })} />
        </Field>
      </div>
      <Lines label="Agents without memory context" hint="One agent name per line" value={value.skip_agents} onChange={(v) => set({ ...value, skip_agents: v })} rows={3} />
      <h3>Proposals (always wait for your approval)</h3>
      <label className="checkbox"><input type="checkbox" checked={value.extract_on_review_comments} onChange={(e) => set({ ...value, extract_on_review_comments: e.target.checked })} /> From review comments</label>
      <label className="checkbox"><input type="checkbox" checked={value.extract_on_completed_workflows} onChange={(e) => set({ ...value, extract_on_completed_workflows: e.target.checked })} /> From finished workflows' final reports</label>
      <Field label="Duplicate threshold" hint="How similar in meaning a proposal must be to an existing memory to be flagged as a duplicate (0.5–1)">
        <input type="number" min={0.5} max={1} step={0.01} value={value.duplicate_similarity} onChange={(e) => set({ ...value, duplicate_similarity: Number(e.target.value) })} />
      </Field>
    </div>
  );
}

type ExecutionValue = {
  limits: { cpus: number; memory_mb: number; pids: number };
  egress_allowlist: string[]; command_timeout_s: number; setup_timeout_s: number; max_output_kb: number;
  retention_days: number; max_fix_rounds: number; branch_template: string;
  commit_author: { name: string; email: string }; images: Record<string, string>;
};

function ExecutionEditor({ value, set }: { value: ExecutionValue; set: (v: ExecutionValue) => void }) {
  const lim = (k: keyof ExecutionValue["limits"]) => (e: React.ChangeEvent<HTMLInputElement>) =>
    set({ ...value, limits: { ...value.limits, [k]: Number(e.target.value) } });
  const num = (k: "command_timeout_s" | "setup_timeout_s" | "max_output_kb" | "retention_days" | "max_fix_rounds") =>
    (e: React.ChangeEvent<HTMLInputElement>) => set({ ...value, [k]: Number(e.target.value) });
  const images = Object.entries(value.images ?? {});
  return (
    <div className="settings-form">
      <h3>Sandbox resources (per task)</h3>
      <div className="form-grid">
        <Field label="CPUs"><input type="number" min={0.25} max={32} step={0.25} value={value.limits.cpus} onChange={lim("cpus")} /></Field>
        <Field label="Memory (MB)"><input type="number" min={256} max={131072} step={256} value={value.limits.memory_mb} onChange={lim("memory_mb")} /></Field>
        <Field label="Processes"><input type="number" min={64} max={32768} value={value.limits.pids} onChange={lim("pids")} /></Field>
      </div>
      <Lines label="Hosts sandboxes may reach" hint="Package registries, one host per line; *.example.org allows subdomains. Private and local addresses are always refused." value={value.egress_allowlist} onChange={(v) => set({ ...value, egress_allowlist: v })} rows={8} />
      <h3>Commands</h3>
      <div className="form-grid">
        <Field label="Command timeout (s)" hint="Checks and agents' commands"><input type="number" min={10} max={21600} value={value.command_timeout_s} onChange={num("command_timeout_s")} /></Field>
        <Field label="Setup timeout (s)" hint="Each setup command"><input type="number" min={10} max={21600} value={value.setup_timeout_s} onChange={num("setup_timeout_s")} /></Field>
        <Field label="Output given to agents (KB)"><input type="number" min={4} max={1024} value={value.max_output_kb} onChange={num("max_output_kb")} /></Field>
      </div>
      <h3>Quality gate and delivery</h3>
      <div className="form-grid">
        <Field label="Fix rounds before asking you"><input type="number" min={0} max={10} value={value.max_fix_rounds} onChange={num("max_fix_rounds")} /></Field>
        <Field label="Keep finished workspaces (days)"><input type="number" min={1} max={365} value={value.retention_days} onChange={num("retention_days")} /></Field>
        <Field label="Branch name" hint="{slug} title, {id} short task id, {external} source key" grow>
          <input className="mono" value={value.branch_template} onChange={(e) => set({ ...value, branch_template: e.target.value })} />
        </Field>
      </div>
      <div className="form-grid">
        <Field label="Commit author name" hint="Empty = the repository's git identity">
          <input value={value.commit_author.name} onChange={(e) => set({ ...value, commit_author: { ...value.commit_author, name: e.target.value } })} />
        </Field>
        <Field label="Commit author email">
          <input type="email" value={value.commit_author.email} onChange={(e) => set({ ...value, commit_author: { ...value.commit_author, email: e.target.value } })} />
        </Field>
      </div>
      <h3>Toolchain images offered by Detect</h3>
      <div className="form-grid">
        {images.map(([lang, img]) => (
          <Field key={lang} label={lang}>
            <input className="mono" value={img} onChange={(e) => set({ ...value, images: { ...value.images, [lang]: e.target.value } })} />
          </Field>
        ))}
      </div>
    </div>
  );
}

const EDITORS: Record<string, (v: any, set: (v: any) => void) => ReactNode> = {
  execution: (v, set) => <ExecutionEditor value={v as ExecutionValue} set={set} />,
  models: (v, set) => <ModelsEditor value={v as ModelsValue} set={set} />,
  model_policy: (v, set) => <PolicyEditor value={v as PolicyValue} set={set} />,
  workflow_defaults: (v, set) => <WorkflowEditor value={v as WorkflowValue} set={set} />,
  board_defaults: (v, set) => <ColumnsEditor value={v as BoardValue} set={set} />,
  indexing: (v, set) => <IndexingEditor value={v as IndexingValue} set={set} />,
  memory: (v, set) => <MemoryEditor value={v as MemoryValue} set={set} />,
};

/** Every panel-managed setting; unknown keys fall back to the JSON editor. */
export function SettingsPanels({ only }: { only?: string[] }) {
  const settings = useSettings();
  if (settings.error) return <ErrorNote error={settings.error} />;
  const items = (settings.data?.items ?? []).filter((s) => !only || only.includes(s.key));
  return (
    <>
      {items.map((s) => (
        <SettingCard<Json> key={s.key} setting={s}>
          {(value, set) => (EDITORS[s.key]
            ? EDITORS[s.key]!(value, set)
            : <pre className="json">{JSON.stringify(value, null, 2)}</pre>)}
        </SettingCard>
      ))}
    </>
  );
}
