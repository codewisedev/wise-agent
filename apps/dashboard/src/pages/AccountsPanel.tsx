import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, KeyRound, Plus, Trash2, XCircle } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, Empty, ErrorNote, Field, Panel } from "../components/ui";

type Account = Schemas["Account"];
type Kind = Schemas["AccountKind"];

interface Preset {
  label: string;
  kind: Kind;
  baseUrl?: string;
  model?: string;
  secretLabel: string;
  secretHint: string;
  secretOptional?: boolean;
  multiline?: boolean;
  needsBaseUrl?: boolean;
  meta?: { key: string; label: string; hint?: string }[];
}

/** Presets fill sensible defaults; every field stays editable. */
export const PRESETS: Record<string, Preset> = {
  lmstudio: {
    label: "LM Studio", kind: "openai_compatible", baseUrl: "http://host.docker.internal:1234/v1", needsBaseUrl: true,
    secretLabel: "API token", secretHint: "Only if “Require Authentication” is on in LM Studio.", secretOptional: true,
  },
  deepseek: {
    label: "DeepSeek", kind: "openai_compatible", baseUrl: "https://api.deepseek.com/v1", model: "deepseek-chat", needsBaseUrl: true,
    secretLabel: "API key", secretHint: "From platform.deepseek.com → API keys.",
  },
  openai: {
    label: "OpenAI-compatible", kind: "openai_compatible", baseUrl: "https://api.openai.com/v1", needsBaseUrl: true,
    secretLabel: "API key", secretHint: "Any /v1/chat/completions endpoint (OpenAI, OpenRouter, vLLM…).", secretOptional: true,
  },
  claude: {
    label: "Claude Code", kind: "claude_code", model: "sonnet",
    secretLabel: "OAuth token", secretHint: "Run `claude setup-token` and paste the token. Uses that Claude subscription.",
  },
  codex: {
    label: "Codex", kind: "codex_cli", multiline: true,
    secretLabel: "auth.json", secretHint: "Run `codex login`, then paste the contents of ~/.codex/auth.json. Wise keeps it refreshed.",
  },
  jira: {
    label: "Jira", kind: "jira", baseUrl: "https://your-site.atlassian.net", needsBaseUrl: true,
    secretLabel: "API token", secretHint: "id.atlassian.com → Security → API tokens. Read access is enough.",
    meta: [{ key: "email", label: "Atlassian email" }],
  },
  clickup: { label: "ClickUp", kind: "clickup", secretLabel: "Personal API token", secretHint: "ClickUp → Settings → Apps → API token." },
  trello: {
    label: "Trello", kind: "trello", secretLabel: "Token", secretHint: "Generate a read-only token for your Power-Up key.",
    meta: [{ key: "apiKey", label: "API key", hint: "trello.com/power-ups/admin → API key" }],
  },
};

const KIND_LABEL: Record<Kind, string> = {
  claude_code: "Claude Code", codex_cli: "Codex", openai_compatible: "OpenAI-compatible",
  jira: "Jira", clickup: "ClickUp", trello: "Trello",
};

function NewAccountForm({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient();
  const [presetKey, setPresetKey] = useState("lmstudio");
  const preset = PRESETS[presetKey]!;
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState(preset.baseUrl ?? "");
  const [model, setModel] = useState(preset.model ?? "");
  const [secret, setSecret] = useState("");
  const [meta, setMeta] = useState<Record<string, string>>({});
  const choose = (key: string) => {
    const p = PRESETS[key]!;
    setPresetKey(key);
    setBaseUrl(p.baseUrl ?? "");
    setModel(p.model ?? "");
    setMeta({});
    setSecret(""); // never carry a credential over to another provider
  };
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/accounts", {
          body: {
            name: name.trim(), kind: preset.kind,
            baseUrl: preset.needsBaseUrl ? baseUrl.trim() : undefined,
            defaultModel: model.trim() || undefined,
            secret: secret.trim() || undefined,
            metadata: preset.meta ? meta : undefined,
          },
        }),
      ),
    onSuccess: () => {
      setSecret("");
      void qc.invalidateQueries({ queryKey: ["accounts"] });
      onDone();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  const isModel = ["openai_compatible", "claude_code", "codex_cli"].includes(preset.kind);

  return (
    <form onSubmit={submit} className="panel-body" aria-label="New account">
      <div className="form-grid">
        <Field label="Type">
          <select value={presetKey} onChange={(e) => choose(e.target.value)}>
            {Object.entries(PRESETS).map(([k, p]) => <option key={k} value={k}>{p.label}</option>)}
          </select>
        </Field>
        <Field label="Name" hint="Lowercase, e.g. claude-work, lmstudio-desktop">
          <input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} pattern="[a-z0-9][a-z0-9-]{0,62}" required />
        </Field>
        {preset.needsBaseUrl && (
          <Field label="Base URL" grow>
            <input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} required />
          </Field>
        )}
        {isModel && (
          <Field label="Default model" hint={preset.kind === "openai_compatible" ? "Empty = the loaded/first model" : "e.g. sonnet, opus, gpt-5-codex"}>
            <input value={model} onChange={(e) => setModel(e.target.value)} />
          </Field>
        )}
        {preset.meta?.map((m) => (
          <Field key={m.key} label={m.label} hint={m.hint}>
            <input value={meta[m.key] ?? ""} onChange={(e) => setMeta({ ...meta, [m.key]: e.target.value })} required />
          </Field>
        ))}
      </div>
      <div className="form-grid" style={{ marginTop: 12 }}>
        <Field label={`${preset.secretLabel}${preset.secretOptional ? " (optional)" : ""}`} hint={preset.secretHint} grow>
          {preset.multiline ? (
            <textarea className="mono" rows={5} value={secret} onChange={(e) => setSecret(e.target.value)} required={!preset.secretOptional} spellCheck={false} autoComplete="off" />
          ) : (
            <input type="password" value={secret} onChange={(e) => setSecret(e.target.value)} required={!preset.secretOptional} autoComplete="new-password" />
          )}
        </Field>
      </div>
      <p className="small faint">Encrypted at rest. Wise never shows it again and never sends it to the agent runtime.</p>
      <div className="actions">
        <button type="submit" className="btn btn-primary" disabled={create.isPending || !name}>
          <Plus size={15} aria-hidden /> Add account
        </button>
        <button type="button" className="btn btn-ghost" onClick={onDone}>Cancel</button>
      </div>
      <ErrorNote error={create.error} />
    </form>
  );
}

function AccountRow({ a }: { a: Account }) {
  const qc = useQueryClient();
  const [result, setResult] = useState<Schemas["AccountTestResult"] | null>(null);
  const [rotating, setRotating] = useState(false);
  const [secret, setSecret] = useState("");
  const refresh = () => void qc.invalidateQueries({ queryKey: ["accounts"] });
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/accounts/{accountId}/test", { params: { path: { accountId: a.id } } })),
    onSuccess: setResult,
  });
  const patch = useMutation({
    mutationFn: (body: Schemas["UpdateAccountRequest"]) =>
      unwrap(api.PATCH("/accounts/{accountId}", { params: { path: { accountId: a.id } }, body })),
    onSuccess: () => {
      setRotating(false);
      setSecret("");
      setResult(null);
      refresh();
    },
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/accounts/{accountId}", { params: { path: { accountId: a.id } } })),
    onSuccess: refresh,
  });

  return (
    <li className={a.enabled ? "" : "is-dim"}>
      <KeyRound size={14} aria-hidden />
      <span className="mono">{a.name}</span>
      <Badge dot={false}>{KIND_LABEL[a.kind]}</Badge>
      {a.cloud ? <Badge tone="info" dot={false}>cloud</Badge> : <Badge tone="ok" dot={false}>local</Badge>}
      {a.defaultModel && <span className="small faint mono">{a.defaultModel}</span>}
      {a.baseUrl && <span className="small faint mono" title={a.baseUrl}>{a.baseUrl.replace(/^https?:\/\//, "").slice(0, 40)}</span>}
      <span className="small faint mono">{a.hasSecret ? `secret ${a.secretHint || "set"}` : "no secret"}</span>
      {result && (result.ok
        ? <span className="small" style={{ color: "var(--ok)" }}><CheckCircle2 size={13} aria-hidden /> works</span>
        : <span className="small" style={{ color: "var(--danger)" }} title={result.error}><XCircle size={13} aria-hidden /> {result.error?.slice(0, 80)}</span>)}
      <span className="spacer" />
      <button type="button" className="btn btn-ghost btn-sm" onClick={() => test.mutate()} disabled={test.isPending}>
        {test.isPending ? "Testing…" : "Test"}
      </button>
      <button type="button" className="btn btn-ghost btn-sm" onClick={() => setRotating((v) => !v)}>Replace secret</button>
      <button type="button" className="btn btn-ghost btn-sm" onClick={() => patch.mutate({ enabled: !a.enabled })}>
        {a.enabled ? "Disable" : "Enable"}
      </button>
      <button
        type="button"
        className="btn btn-ghost btn-icon"
        aria-label={`Delete ${a.name}`}
        onClick={() => window.confirm(`Delete account ${a.name}? Workflow steps using it will fail until changed.`) && del.mutate()}
      >
        <Trash2 size={14} aria-hidden />
      </button>
      {rotating && (
        <form
          className="rotate"
          onSubmit={(e) => {
            e.preventDefault();
            patch.mutate({ secret: secret.trim() });
          }}
        >
          {a.kind === "codex_cli" ? (
            <textarea className="mono" rows={4} value={secret} onChange={(e) => setSecret(e.target.value)} aria-label="New auth.json" required />
          ) : (
            <input type="password" value={secret} onChange={(e) => setSecret(e.target.value)} aria-label="New secret" autoComplete="new-password" required />
          )}
          <button type="submit" className="btn btn-primary btn-sm" disabled={patch.isPending}>Save</button>
        </form>
      )}
      <ErrorNote error={test.error ?? patch.error ?? del.error} />
    </li>
  );
}

export function useAccounts() {
  return useQuery({ queryKey: ["accounts"], queryFn: () => unwrap(api.GET("/accounts")) });
}

export function AccountsPanel() {
  const accounts = useAccounts();
  const [adding, setAdding] = useState(false);
  const data = accounts.data;
  return (
    <Panel
      title="Accounts"
      count={data?.items.length}
      actions={
        <button type="button" className="btn btn-sm" onClick={() => setAdding((v) => !v)} disabled={data && !data.secretsEnabled} aria-expanded={adding}>
          <Plus size={14} aria-hidden /> Add account
        </button>
      }
    >
      <ErrorNote error={accounts.error} />
      {data && !data.secretsEnabled && (
        <p className="error" role="alert"><span>Set WISE_SECRETS_KEY in .env (openssl rand -base64 32) and restart the API to store accounts.</span></p>
      )}
      {adding && <NewAccountForm onDone={() => setAdding(false)} />}
      {data && data.items.length === 0 && !adding && (
        <Empty icon={<KeyRound size={24} aria-hidden />}>
          No accounts yet. Add Claude, Codex, LM Studio or DeepSeek accounts and pick one per workflow step.
        </Empty>
      )}
      {data && data.items.length > 0 && <ul className="list accounts">{data.items.map((a) => <AccountRow key={a.id} a={a} />)}</ul>}
      {data && data.builtinProviders.length > 0 && (
        <p className="panel-footer small faint">
          Also available from configs/models/models.yaml: {data.builtinProviders.map((p) => <code key={p} className="chip">{p}</code>)}
        </p>
      )}
    </Panel>
  );
}
