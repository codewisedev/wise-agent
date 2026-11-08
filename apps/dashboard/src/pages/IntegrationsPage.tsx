import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Download, Pencil, Plug, Plus, RefreshCw, Save, Trash2, X } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { useSprints } from "./SprintsPage";
import { Badge, Empty, ErrorNote, Field, Panel, formatDate, relativeTime } from "../components/ui";

type Integration = Schemas["Integration"];
type Kind = Schemas["IntegrationKind"];

const KIND_LABEL: Record<Kind, string> = { jira: "Jira", clickup: "ClickUp", trello: "Trello" };

const CONFIG_HELP: Record<Kind, { key: string; label: string; hint: string; placeholder: string }> = {
  jira: { key: "jql", label: "JQL filter", hint: "Which issues to import, e.g. project = ABC AND statusCategory != Done", placeholder: "project = ABC" },
  clickup: { key: "listId", label: "List", hint: "Paste the list's link (or its id) from the browser address bar.", placeholder: "https://app.clickup.com/…/v/l/6-…-1" },
  trello: { key: "boardId", label: "Board", hint: "Paste the board's link (or its id).", placeholder: "https://trello.com/b/AbC123xY/…" },
};

function SourceForm({ projectId, onDone, existing }: { projectId: string; onDone: () => void; existing?: Integration }) {
  const qc = useQueryClient();
  const accounts = useQuery({ queryKey: ["accounts"], queryFn: () => unwrap(api.GET("/accounts")) });
  const templates = useQuery({ queryKey: ["workflow-templates"], queryFn: () => unwrap(api.GET("/workflow-templates")) });
  const columns = useQuery({
    queryKey: ["columns", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/columns", { params: { path: { projectId } } })),
  });
  const cols = columns.data?.items ?? [];
  const cfg = (existing?.config ?? {}) as Record<string, unknown>;
  const [kind, setKind] = useState<Kind>(existing?.kind ?? "jira");
  const [name, setName] = useState(existing?.name ?? "");
  const [accountId, setAccountId] = useState(existing?.accountId ?? "");
  const [configValue, setConfigValue] = useState(existing ? String(cfg[CONFIG_HELP[existing.kind].key] ?? "") : "");
  const [lists, setLists] = useState(Array.isArray(cfg.lists) ? (cfg.lists as string[]).join(", ") : "");
  const [mine, setMine] = useState(cfg.assignedToMe === true);
  // What to import: one list/board/filter (optionally into a fixed sprint), or follow the tool's sprints.
  const [mode, setMode] = useState<string>(typeof cfg.mode === "string" ? cfg.mode : "single");
  const [follow, setFollow] = useState(String(cfg.folderId ?? cfg.boardId ?? ""));
  const [sprintId, setSprintId] = useState(existing?.sprintId ?? "");
  const sprints = useSprints(projectId);
  const [rows, setRows] = useState<{ status: string; column: string }[]>(
    Object.entries(existing?.statusMap ?? {}).map(([status, column]) => ({ status, column })),
  );
  const [templateId, setTemplateId] = useState(existing?.defaultTemplateId ?? "");
  const [autoStart, setAutoStart] = useState(existing?.autoStart ?? false);
  const [interval, setIntervalMin] = useState(existing?.intervalMinutes ?? 15);
  const matching = accounts.data?.items.filter((a) => a.kind === kind) ?? [];
  const account = accountId || matching[0]?.id || "";
  const help = CONFIG_HELP[kind];

  const create = useMutation({
    mutationFn: () => {
      const following = (kind === "clickup" && mode === "folder") || (kind === "jira" && mode === "board");
      const config: Record<string, unknown> = { assignedToMe: mine };
      if (kind === "clickup" && following) Object.assign(config, { mode: "folder", folderId: follow.trim() });
      else if (kind === "jira" && following) Object.assign(config, { mode: "board", boardId: follow.trim(), jql: configValue.trim() });
      else config[help.key] = configValue.trim();
      if (kind === "trello" && lists.trim()) config.lists = lists.split(",").map((s) => s.trim()).filter(Boolean);
      const statusMap = Object.fromEntries(rows.filter((r) => r.status.trim()).map((r) => [r.status.trim(), r.column]));
      const body = {
        name: name.trim(), kind, accountId: account, config, statusMap, autoStart, intervalMinutes: interval,
        defaultTemplateId: templateId || null, enabled: existing?.enabled ?? true,
        sprintId: following ? null : sprintId || null,
      };
      return existing
        ? unwrap(api.PUT("/integrations/{integrationId}", { params: { path: { integrationId: existing.id } }, body }))
        : unwrap(api.POST("/projects/{projectId}/integrations", { params: { path: { projectId } }, body }));
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
    <form onSubmit={submit} className="panel-body" aria-label={existing ? `Edit ${existing.name}` : "New task source"}>
      <div className="form-grid">
        <Field label="Source">
          <select value={kind} disabled={!!existing} onChange={(e) => { setKind(e.target.value as Kind); setAccountId(""); }}>
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
        {(kind === "clickup" || kind === "jira") && (
          <Field label="Import">
            <select value={mode} onChange={(e) => setMode(e.target.value)}>
              <option value="single">{kind === "clickup" ? "one list" : "a JQL filter"}</option>
              <option value={kind === "clickup" ? "folder" : "board"}>{kind === "clickup" ? "a sprint folder (follow sprints)" : "a board's sprints (follow sprints)"}</option>
            </select>
          </Field>
        )}
        {kind === "clickup" && mode === "folder" ? (
          <Field label="Sprint folder" hint="Paste a link to the folder, or to any sprint list in it. Every current and upcoming list becomes a Wise sprint, with its dates." grow>
            <input value={follow} onChange={(e) => setFollow(e.target.value)} required className="mono" placeholder="https://app.clickup.com/…/v/l/6-…-1" />
          </Field>
        ) : kind === "jira" && mode === "board" ? (
          <>
            <Field label="Board" hint="Link to the Jira board (…/boards/12) or its id. Active and future sprints become Wise sprints.">
              <input value={follow} onChange={(e) => setFollow(e.target.value)} required className="mono" placeholder="https://acme.atlassian.net/jira/software/projects/ABC/boards/12" />
            </Field>
            <Field label="Extra JQL (optional)" grow>
              <input value={configValue} onChange={(e) => setConfigValue(e.target.value)} className="mono" placeholder="type = Story" />
            </Field>
          </>
        ) : (
          <Field label={help.label} hint={help.hint} grow>
            <input value={configValue} onChange={(e) => setConfigValue(e.target.value)} placeholder={help.placeholder} required className="mono" />
          </Field>
        )}
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
        {!((kind === "clickup" && mode === "folder") || (kind === "jira" && mode === "board")) && (sprints.data?.items.length ?? 0) > 0 && (
          <Field label="Sprint" hint="Imported tasks join this sprint">
            <select value={sprintId} onChange={(e) => setSprintId(e.target.value)}>
              <option value="">no sprint</option>
              {sprints.data!.items.filter((sp) => !sp.closed || sp.id === sprintId).map((sp) => <option key={sp.id} value={sp.id}>{sp.name}{sp.current ? " (current)" : ""}</option>)}
            </select>
          </Field>
        )}
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
          <input type="checkbox" checked={mine} onChange={(e) => setMine(e.target.checked)} />
          <span>Only items assigned to me</span>
        </label>
        <label className="checkbox" style={{ marginTop: 23 }}>
          <input type="checkbox" checked={autoStart} onChange={(e) => setAutoStart(e.target.checked)} />
          <span>Run the workflow on new tasks automatically</span>
        </label>
      </div>
      <div className="actions">
        <button type="submit" className="btn btn-primary" disabled={create.isPending || !account || !name.trim()}>
          {existing ? <><Save size={15} aria-hidden /> Save source</> : <><Plus size={15} aria-hidden /> Add source</>}
        </button>
        <button type="button" className="btn btn-ghost" onClick={onDone}>Cancel</button>
      </div>
      <ErrorNote error={create.error} />
    </form>
  );
}

function SourceRow({ it, projectId }: { it: Integration; projectId: string }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
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
        autoStart: it.autoStart, intervalMinutes: it.intervalMinutes, enabled: !it.enabled, sprintId: it.sprintId ?? null,
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
  const filter = String(it.config.folderId ?? it.config.listId ?? it.config.boardId ?? it.config.jql ?? "");
  return (
    <li className={it.enabled ? "" : "is-dim"}>
      <Plug size={14} aria-hidden />
      <strong>{it.name}</strong>
      <Badge dot={false}>{KIND_LABEL[it.kind]}</Badge>
      <span className="mono small faint" title={filter}>{filter.slice(0, 50)}</span>
      {it.autoStart && <Badge tone="info" dot={false}>auto-run</Badge>}
      {it.config.assignedToMe === true && <Badge dot={false}>assigned to me</Badge>}
      {(it.config.mode === "folder" || it.config.mode === "board") && <Badge tone="info" dot={false}>follows sprints</Badge>}
      <span className="spacer" />
      <span className="small faint" title={formatDate(it.lastSyncedAt)}>
        {it.lastSyncedAt ? `synced ${relativeTime(it.lastSyncedAt)}` : "never synced"}
        {r && <> · {r.total} items, +{r.created} new, {r.updated} updated{r.started ? `, ${r.started} started` : ""}{r.archived ? `, ${r.archived} archived` : ""}{r.restored ? `, ${r.restored} restored` : ""}</>}
        {` · every ${it.intervalMinutes}m`}
      </span>
      <button type="button" className="btn btn-sm" onClick={() => sync.mutate()} disabled={sync.isPending}>
        {sync.isPending ? <RefreshCw size={13} className="spin" aria-hidden /> : <Download size={13} aria-hidden />} Sync now
      </button>
      <button type="button" className="btn btn-ghost btn-sm" onClick={() => setEditing((v) => !v)} aria-expanded={editing}><Pencil size={13} aria-hidden /> Edit</button>
      <button type="button" className="btn btn-ghost btn-sm" onClick={() => toggle.mutate()}>{it.enabled ? "Pause" : "Resume"}</button>
      <button type="button" className="btn btn-ghost btn-icon" aria-label={`Delete ${it.name}`}
        onClick={() => window.confirm(`Remove ${it.name}? Imported tasks stay on the board.`) && del.mutate()}>
        <Trash2 size={14} aria-hidden />
      </button>
      {it.lastError && <p className="error small" role="alert"><span>Last sync failed: {it.lastError}</span></p>}
      <ErrorNote error={sync.error ?? toggle.error ?? del.error} />
      {editing && <div className="source-edit"><SourceForm projectId={projectId} existing={it} onDone={() => setEditing(false)} /></div>}
    </li>
  );
}

/** A project's task sources: add, edit, sync, pause and delete. */
export function SourcesPanel({ projectId, adding, setAdding }: { projectId: string; adding: boolean; setAdding: (v: boolean) => void }) {
  const list = useQuery({
    queryKey: ["integrations", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/integrations", { params: { path: { projectId } } })),
  });
  return (
    <Panel
      title="Task sources"
      count={list.data?.items.length}
      actions={!adding && (
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => setAdding(true)}>
          <Plus size={13} aria-hidden /> Add source
        </button>
      )}
    >
      <ErrorNote error={list.error} />
      {adding && <SourceForm projectId={projectId} onDone={() => setAdding(false)} />}
      {list.data?.items.length === 0 && !adding && (
        <Empty icon={<Plug size={24} aria-hidden />}>No task sources. Add Jira, ClickUp or Trello to pull issues onto the board.</Empty>
      )}
      {(list.data?.items.length ?? 0) > 0 && (
        <ul className="list accounts">{list.data!.items.map((it) => <SourceRow key={it.id} it={it} projectId={projectId} />)}</ul>
      )}
    </Panel>
  );
}

export function IntegrationsPage() {
  const { projectId = "" } = useParams();
  const [adding, setAdding] = useState(false);
  const project = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}", { params: { path: { projectId } } })),
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
      <SourcesPanel projectId={projectId} adding={adding} setAdding={setAdding} />
    </>
  );
}
