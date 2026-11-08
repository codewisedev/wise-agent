import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Download, Plug, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, Empty, ErrorNote, Field, Panel, formatDate, relativeTime } from "../components/ui";

type Integration = Schemas["Integration"];
type Kind = Schemas["IntegrationKind"];

const KIND_LABEL: Record<Kind, string> = { jira: "Jira", clickup: "ClickUp", trello: "Trello" };

const CONFIG_HELP: Record<Kind, { key: string; label: string; hint: string; placeholder: string }> = {
  jira: { key: "jql", label: "JQL filter", hint: "Which issues to import, e.g. project = ABC AND statusCategory != Done", placeholder: "project = ABC" },
  clickup: { key: "listId", label: "List id", hint: "Open the list in ClickUp; the id is in the URL (…/li/<id>).", placeholder: "901234567" },
  trello: { key: "boardId", label: "Board id", hint: "From the board URL: trello.com/b/<id>/…", placeholder: "AbC123xY" },
};

function SourceForm({ projectId, onDone }: { projectId: string; onDone: () => void }) {
  const qc = useQueryClient();
  const accounts = useQuery({ queryKey: ["accounts"], queryFn: () => unwrap(api.GET("/accounts")) });
  const templates = useQuery({ queryKey: ["workflow-templates"], queryFn: () => unwrap(api.GET("/workflow-templates")) });
  const columns = useQuery({
    queryKey: ["columns", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/columns", { params: { path: { projectId } } })),
  });
  const cols = columns.data?.items ?? [];
  const [kind, setKind] = useState<Kind>("jira");
  const [name, setName] = useState("");
  const [accountId, setAccountId] = useState("");
  const [configValue, setConfigValue] = useState("");
  const [lists, setLists] = useState("");
  const [rows, setRows] = useState<{ status: string; column: string }[]>([]);
  const [templateId, setTemplateId] = useState("");
  const [autoStart, setAutoStart] = useState(false);
  const [interval, setIntervalMin] = useState(15);
  const matching = accounts.data?.items.filter((a) => a.kind === kind) ?? [];
  const account = accountId || matching[0]?.id || "";
  const help = CONFIG_HELP[kind];

  const create = useMutation({
    mutationFn: () => {
      const config: Record<string, unknown> = { [help.key]: configValue.trim() };
      if (kind === "trello" && lists.trim()) config.lists = lists.split(",").map((s) => s.trim()).filter(Boolean);
      const statusMap = Object.fromEntries(rows.filter((r) => r.status.trim()).map((r) => [r.status.trim(), r.column]));
      return unwrap(api.POST("/projects/{projectId}/integrations", {
        params: { path: { projectId } },
        body: {
          name: name.trim(), kind, accountId: account, config, statusMap, autoStart, intervalMinutes: interval,
          ...(templateId ? { defaultTemplateId: templateId } : {}),
        },
      }));
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["integrations", projectId] });
      onDone();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };

  return (
    <form onSubmit={submit} className="panel-body" aria-label="New task source">
      <div className="form-grid">
        <Field label="Source">
          <select value={kind} onChange={(e) => { setKind(e.target.value as Kind); setAccountId(""); }}>
            {(Object.keys(KIND_LABEL) as Kind[]).map((k) => <option key={k} value={k}>{KIND_LABEL[k]}</option>)}
          </select>
        </Field>
        <Field label="Account" hint={matching.length ? "Read access is enough; Wise never writes back." : undefined}>
          {matching.length ? (
            <select value={account} onChange={(e) => setAccountId(e.target.value)}>
              {matching.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
          ) : (
            <span className="small">No {KIND_LABEL[kind]} account yet — <Link to="/settings">add one in Settings</Link>.</span>
          )}
        </Field>
        <Field label="Name" grow>
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder={`${KIND_LABEL[kind]} issues`} required maxLength={100} />
        </Field>
      </div>
      <div className="form-grid" style={{ marginTop: 12 }}>
        <Field label={help.label} hint={help.hint} grow>
          <input value={configValue} onChange={(e) => setConfigValue(e.target.value)} placeholder={help.placeholder} required className="mono" />
        </Field>
        {kind === "trello" && (
          <Field label="Lists (optional)" hint="Comma-separated list names to import; empty = all open lists">
            <input value={lists} onChange={(e) => setLists(e.target.value)} placeholder="To Do, Doing" />
          </Field>
        )}
      </div>
      <fieldset className="status-map">
        <legend className="small">Status → column <span className="faint">(unlisted statuses go to the list with the matching role: done, in progress… else the “to do” list)</span></legend>
        {rows.map((r, i) => (
          <div key={i} className="form-grid">
            <input aria-label="External status" value={r.status} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, status: e.target.value } : x)))} placeholder="e.g. Ready for QA" />
            <select aria-label="Board column" value={r.column} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, column: e.target.value } : x)))}>
              {cols.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
            <button type="button" className="btn btn-ghost btn-icon" aria-label="Remove mapping" onClick={() => setRows(rows.filter((_, j) => j !== i))}><X size={14} aria-hidden /></button>
          </div>
        ))}
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => setRows([...rows, { status: "", column: (cols.find((c) => c.role === "todo") ?? cols[0])?.id ?? "" }])} disabled={cols.length === 0}>
          <Plus size={13} aria-hidden /> Add mapping
        </button>
      </fieldset>
      <div className="form-grid" style={{ marginTop: 12 }}>
        <Field label="Workflow for imported tasks">
          <select value={templateId} onChange={(e) => setTemplateId(e.target.value)}>
            <option value="">default template</option>
            {templates.data?.items.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
          </select>
        </Field>
        <Field label="Sync every (minutes)">
          <input type="number" min={5} max={1440} value={interval} onChange={(e) => setIntervalMin(Number(e.target.value))} />
        </Field>
        <label className="checkbox" style={{ marginTop: 23 }}>
          <input type="checkbox" checked={autoStart} onChange={(e) => setAutoStart(e.target.checked)} />
          <span>Run the workflow on new tasks automatically</span>
        </label>
      </div>
      <div className="actions">
        <button type="submit" className="btn btn-primary" disabled={create.isPending || !account || !name.trim()}>
          <Plus size={15} aria-hidden /> Add source
        </button>
        <button type="button" className="btn btn-ghost" onClick={onDone}>Cancel</button>
      </div>
      <ErrorNote error={create.error} />
    </form>
  );
}

function SourceRow({ it, projectId }: { it: Integration; projectId: string }) {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["integrations", projectId] });
    void qc.invalidateQueries({ queryKey: ["board", projectId] });
  };
  const sync = useMutation({
    mutationFn: () => unwrap(api.POST("/integrations/{integrationId}/sync", { params: { path: { integrationId: it.id } } })),
    onSettled: refresh,
  });
  const toggle = useMutation({
    mutationFn: () => unwrap(api.PUT("/integrations/{integrationId}", {
      params: { path: { integrationId: it.id } },
      body: {
        name: it.name, kind: it.kind, accountId: it.accountId, config: it.config, statusMap: it.statusMap,
        autoStart: it.autoStart, intervalMinutes: it.intervalMinutes, enabled: !it.enabled,
        ...(it.defaultTemplateId ? { defaultTemplateId: it.defaultTemplateId } : {}),
      },
    })),
    onSuccess: refresh,
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/integrations/{integrationId}", { params: { path: { integrationId: it.id } } })),
    onSuccess: refresh,
  });
  const r = it.lastResult;
  const filter = String(it.config.jql ?? it.config.listId ?? it.config.boardId ?? "");
  return (
    <li className={it.enabled ? "" : "is-dim"}>
      <Plug size={14} aria-hidden />
      <strong>{it.name}</strong>
      <Badge dot={false}>{KIND_LABEL[it.kind]}</Badge>
      <span className="mono small faint" title={filter}>{filter.slice(0, 50)}</span>
      {it.autoStart && <Badge tone="info" dot={false}>auto-run</Badge>}
      <span className="spacer" />
      <span className="small faint" title={formatDate(it.lastSyncedAt)}>
        {it.lastSyncedAt ? `synced ${relativeTime(it.lastSyncedAt)}` : "never synced"}
        {r && <> · {r.total} items, +{r.created} new, {r.updated} updated{r.started ? `, ${r.started} started` : ""}</>}
        {` · every ${it.intervalMinutes}m`}
      </span>
      <button type="button" className="btn btn-sm" onClick={() => sync.mutate()} disabled={sync.isPending}>
        {sync.isPending ? <RefreshCw size={13} className="spin" aria-hidden /> : <Download size={13} aria-hidden />} Sync now
      </button>
      <button type="button" className="btn btn-ghost btn-sm" onClick={() => toggle.mutate()}>{it.enabled ? "Pause" : "Resume"}</button>
      <button type="button" className="btn btn-ghost btn-icon" aria-label={`Delete ${it.name}`}
        onClick={() => window.confirm(`Remove ${it.name}? Imported tasks stay on the board.`) && del.mutate()}>
        <Trash2 size={14} aria-hidden />
      </button>
      {it.lastError && <p className="error small" role="alert"><span>Last sync failed: {it.lastError}</span></p>}
      <ErrorNote error={sync.error ?? toggle.error ?? del.error} />
    </li>
  );
}

export function IntegrationsPage() {
  const { projectId = "" } = useParams();
  const [adding, setAdding] = useState(false);
  const project = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}", { params: { path: { projectId } } })),
  });
  const list = useQuery({
    queryKey: ["integrations", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/integrations", { params: { path: { projectId } } })),
  });
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/projects">projects</Link>
        <ChevronRight size={12} aria-hidden />
        <Link to={`/projects/${projectId}`}>{project.data?.name ?? "project"}</Link>
        <ChevronRight size={12} aria-hidden />
        <Link to={`/projects/${projectId}/board`}>board</Link>
        <ChevronRight size={12} aria-hidden />
        <span>task sources</span>
      </nav>
      <header className="page-header">
        <div>
          <h1>Task sources</h1>
          <p className="desc">Import issues and cards from Jira, ClickUp or Trello onto this project's board. Import only — Wise never writes back.</p>
        </div>
        <button className="btn btn-primary" onClick={() => setAdding((v) => !v)} aria-expanded={adding}>
          <Plus size={15} aria-hidden /> Add source
        </button>
      </header>
      <Panel title="Sources" count={list.data?.items.length}>
        <ErrorNote error={list.error} />
        {adding && <SourceForm projectId={projectId} onDone={() => setAdding(false)} />}
        {list.data?.items.length === 0 && !adding && (
          <Empty icon={<Plug size={24} aria-hidden />}>No task sources. Add one to pull issues onto the board.</Empty>
        )}
        {(list.data?.items.length ?? 0) > 0 && (
          <ul className="list accounts">{list.data!.items.map((it) => <SourceRow key={it.id} it={it} projectId={projectId} />)}</ul>
        )}
      </Panel>
    </>
  );
}
