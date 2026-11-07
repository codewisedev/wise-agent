import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Monitor, Moon, Plus, ShieldAlert, Sun } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, unwrap } from "../api/client";
import { useSession } from "../auth";
import { AccountsPanel } from "./AccountsPanel";
import { Badge, CopyText, Empty, ErrorNote, Field, Panel, formatDate, relativeTime } from "../components/ui";
import { useTheme, type ThemePreference } from "../theme";

const tokensKey = ["auth", "tokens"];
const THEMES: { value: ThemePreference; label: string; icon: typeof Moon }[] = [
  { value: "dark", label: "Dark", icon: Moon },
  { value: "light", label: "Light", icon: Sun },
  { value: "system", label: "System", icon: Monitor },
];

export function SettingsPage() {
  const { data: session } = useSession();
  const { pref, setPref } = useTheme();
  const qc = useQueryClient();
  const tokens = useQuery({ queryKey: tokensKey, queryFn: () => unwrap(api.GET("/auth/tokens")) });

  const [name, setName] = useState("");
  const [days, setDays] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/auth/tokens", { body: { name, ...(days ? { expiresInDays: Number(days) } : {}) } })),
    onSuccess: (res) => {
      setSecret(res.secret);
      setName("");
      setDays("");
      void qc.invalidateQueries({ queryKey: tokensKey });
    },
  });
  const revoke = useMutation({
    mutationFn: (tokenId: string) => unwrap(api.DELETE("/auth/tokens/{tokenId}", { params: { path: { tokenId } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: tokensKey }),
  });
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  const items = tokens.data?.items ?? [];

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Settings</h1>
          <div className="meta">
            <span className="mono">{session?.user.username}</span>
            <Badge dot={false}>{session?.user.role}</Badge>
            {session?.user.id && <CopyText value={session.user.id} display={`id:${session.user.id.slice(0, 8)}`} label="user id" />}
          </div>
        </div>
      </header>

      <Panel title="Appearance" flush={false}>
        <div className="form-grid" role="radiogroup" aria-label="Theme">
          {THEMES.map(({ value, label, icon: Icon }) => (
            <button
              key={value}
              type="button"
              role="radio"
              aria-checked={pref === value}
              className={`btn${pref === value ? " btn-primary" : ""}`}
              onClick={() => setPref(value)}
            >
              <Icon size={15} aria-hidden /> {label}
            </button>
          ))}
          <span className="small faint">
            Press <kbd>t</kbd> anywhere to cycle themes.
          </span>
        </div>
      </Panel>

      <Panel
        title="Personal API tokens"
        count={items.length}
        footer={
          <form onSubmit={onSubmit}>
            <div className="form-grid">
              <Field label="Token name">
                <input value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} placeholder="ci-runner" />
              </Field>
              <Field label="Expires in (days)" hint="Leave empty for no expiry">
                <input type="number" min={1} max={3650} value={days} onChange={(e) => setDays(e.target.value)} placeholder="90" />
              </Field>
              <button type="submit" className="btn btn-primary" disabled={create.isPending}>
                <Plus size={15} aria-hidden /> Create token
              </button>
            </div>
            <ErrorNote error={create.error} />
          </form>
        }
      >
        {secret && (
          <div className="secret" role="status">
            <p>
              <ShieldAlert size={15} aria-hidden /> Copy this token now — it will not be shown again.
            </p>
            <CopyText value={secret} label="API token" />
            <div className="small muted">
              Use as <code>Authorization: Bearer &lt;token&gt;</code>
              <button className="btn btn-ghost btn-sm" style={{ marginLeft: 8 }} onClick={() => setSecret(null)}>
                Dismiss
              </button>
            </div>
          </div>
        )}
        <ErrorNote error={tokens.error ?? revoke.error} />
        {tokens.data && items.length === 0 && <Empty icon={<KeyRound size={24} aria-hidden />}>No tokens yet.</Empty>}
        {items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Prefix</th>
                  <th>Created</th>
                  <th>Last used</th>
                  <th>Expires</th>
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {items.map((t) => (
                  <tr key={t.id} className={t.revokedAt ? "is-dim" : ""}>
                    <td className="row-link">{t.name}</td>
                    <td className="mono small">{t.prefix}…</td>
                    <td className="muted" title={formatDate(t.createdAt)}>
                      {relativeTime(t.createdAt)}
                    </td>
                    <td className="muted" title={formatDate(t.lastUsedAt)}>
                      {t.lastUsedAt ? relativeTime(t.lastUsedAt) : "never"}
                    </td>
                    <td className="muted">{t.expiresAt ? formatDate(t.expiresAt) : "never"}</td>
                    <td className="actions">
                      {t.revokedAt ? (
                        <Badge tone="bad">revoked</Badge>
                      ) : (
                        <button className="btn btn-sm btn-danger" onClick={() => revoke.mutate(t.id)} disabled={revoke.isPending}>
                          Revoke
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <AccountsPanel />

      <Panel title="Workflow engine" flush={false}>
        <p className="small muted">
          Workflows run on Temporal. Its UI shows every workflow's full history (namespace <span className="mono">wise</span>):{" "}
          <a href="http://127.0.0.1:8233/namespaces/wise/workflows" target="_blank" rel="noreferrer">
            Temporal UI on 127.0.0.1:8233
          </a>
          . Approval gates are configured in <span className="mono">configs/workflows/engineering.yaml</span>.
        </p>
      </Panel>
    </>
  );
}
