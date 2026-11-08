import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Columns3, List, ListChecks, Plus, Search } from "lucide-react";
import { useCallback, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { useEventSocket, type WorkflowEvent } from "../api/events";
import { Board, type BoardList, type Card } from "../components/Board";
import { Empty, ErrorNote, Field, Panel, label, relativeTime } from "../components/ui";
import { TaskDrawer } from "./TaskDrawer";
import { TaskStatus } from "./workflowUi";

const STATUSES: Schemas["TaskStatus"][] = ["in_progress", "awaiting_approval", "completed", "failed", "cancelled", "open"];

function NewTaskForm({ defaultProject }: { defaultProject?: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const projects = useQuery({ queryKey: ["projects"], queryFn: () => unwrap(api.GET("/projects")) });
  const [projectId, setProjectId] = useState(defaultProject ?? "");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [templateId, setTemplateId] = useState("");
  const [mode, setMode] = useState<"run" | "step" | "later">("later");
  const templates = useQuery({ queryKey: ["workflow-templates"], queryFn: () => unwrap(api.GET("/workflow-templates")) });
  const create = useMutation({
    mutationFn: (pid: string) =>
      unwrap(api.POST("/projects/{projectId}/tasks", {
        params: { path: { projectId: pid } },
        body: { title, description, start: mode !== "later", stepByStep: mode === "step", ...(templateId ? { templateId } : {}) },
      })),
    onSuccess: (d) => {
      void qc.invalidateQueries({ queryKey: ["tasks"] });
      navigate(`/tasks/${d.task.id}`);
    },
  });
  const project = projectId || projects.data?.items[0]?.id || "";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate(project);
  };

  return (
    <Panel title="New task" flush={false}>
      <form onSubmit={submit}>
        <div className="form-grid">
          <Field label="Project">
            <select value={project} onChange={(e) => setProjectId(e.target.value)} required>
              {projects.data?.items.map((p) => (
                <option key={p.id} value={p.id}>{p.name}</option>
              ))}
            </select>
          </Field>
          <Field label="Title" grow>
            <input value={title} onChange={(e) => setTitle(e.target.value)} minLength={3} maxLength={200} required placeholder="Add a weekly summary page" />
          </Field>
        </div>
        <div className="form-grid" style={{ marginTop: 12 }}>
          <Field label="Description" hint="What you want and why. The product and requirements agents start from this." grow>
            <textarea rows={4} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={20000} />
          </Field>
        </div>
        <div className="form-grid" style={{ marginTop: 12 }}>
          <Field label="Workflow" hint="Steps, accounts and reviews can be adjusted on the task before running.">
            <select value={templateId} onChange={(e) => setTemplateId(e.target.value)}>
              <option value="">default template</option>
              {templates.data?.items.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
            </select>
          </Field>
          <Field label="After creating">
            <select value={mode} onChange={(e) => setMode(e.target.value as typeof mode)}>
              <option value="later">set up, run later</option>
              <option value="step">run step by step</option>
              <option value="run">run now</option>
            </select>
          </Field>
        </div>
        <div className="actions">
          <button type="submit" className="btn btn-primary" disabled={create.isPending || !project || title.trim().length < 3}>
            <Plus size={15} aria-hidden /> Create task
          </button>
        </div>
        <ErrorNote error={create.error} />
      </form>
    </Panel>
  );
}

const store = {
  get: (k: string) => { try { return localStorage.getItem(k) ?? ""; } catch { return ""; } },
  set: (k: string, v: string) => { try { localStorage.setItem(k, v); } catch { /* private mode */ } },
};
const ALL = "all";

type Filters = { q: string; label: string; priority: string; source: string; review: boolean };
const NO_FILTERS: Filters = { q: "", label: "", priority: "", source: "", review: false };

/** Lists of every project, merged by list name (projects on the default layout line up). */
export function mergeBoards(boards: { columns: BoardList[] }[]): BoardList[] {
  const byName = new Map<string, BoardList & { rank: number }>();
  for (const b of boards) {
    b.columns.forEach((c, i) => {
      // Relative place on its board (0 = first list, 1 = last), so boards of different lengths line up.
      const rel = b.columns.length > 1 ? i / (b.columns.length - 1) : 0;
      const key = c.name.trim().toLowerCase();
      const cur = byName.get(key);
      if (cur) {
        cur.cards = [...cur.cards, ...c.cards];
        cur.rank = Math.min(cur.rank, rel);
      } else {
        byName.set(key, { ...c, id: `all:${key}`, projectId: undefined, wipLimit: undefined, cards: [...c.cards], rank: rel });
      }
    });
  }
  return [...byName.values()].sort((a, b) => a.rank - b.rank).map(({ rank: _rank, ...c }) => c);
}

export function TasksPage() {
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const projects = useQuery({ queryKey: ["projects"], queryFn: () => unwrap(api.GET("/projects")) });
  const [chosen, setChosen] = useState(() => params.get("projectId") ?? store.get("wise.tasks.project"));
  const projectList = projects.data?.items ?? [];
  // Fall back to the first project when the remembered one is gone.
  const selected = chosen === ALL ? ALL : projectList.find((p) => p.id === chosen)?.id ?? projectList[0]?.id ?? "";
  const choose = (v: string) => {
    setChosen(v);
    store.set("wise.tasks.project", v);
    const next = new URLSearchParams(params);
    next.delete("projectId");
    setParams(next, { replace: true });
  };
  const [view, setViewState] = useState<"board" | "list">(() => (store.get("wise.tasks.view") === "list" ? "list" : "board"));
  const setView = (v: "board" | "list") => { setViewState(v); store.set("wise.tasks.view", v); };
  const [showForm, setShowForm] = useState(false);
  const [f, setF] = useState<Filters>(NO_FILTERS);
  const [open, setOpen] = useState<string | null>(null);

  const boardIds = selected === ALL ? projectList.map((p) => p.id) : selected ? [selected] : [];
  const boards = useQueries({
    queries: boardIds.map((id) => ({
      queryKey: ["board", id],
      queryFn: () => unwrap(api.GET("/projects/{projectId}/board", { params: { path: { projectId: id } } })),
      enabled: view === "board",
    })),
  });
  const boardErr = boards.find((b) => b.error)?.error;
  const boardsReady = boards.length > 0 && boards.every((b) => b.data);
  // A fixed-size key of every board's data version (hook deps must not change length).
  const dataKey = `${selected}|${boards.map((b) => b.dataUpdatedAt).join(",")}`;
  const lists = useMemo(() => {
    const ready = boards.map((b) => b.data).filter((b): b is NonNullable<typeof b> => !!b);
    if (ready.length !== boards.length) return [];
    return selected === ALL ? mergeBoards(ready) : (ready[0]?.columns ?? []);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataKey]);
  const projectNames = useMemo(() => Object.fromEntries(projectList.map((p) => [p.id, p.name])), [projectList]);
  const allCards = useMemo(() => lists.flatMap((c) => c.cards), [lists]);
  const labels = useMemo(() => [...new Set(allCards.flatMap((c) => c.task.labels ?? []))].sort(), [allCards]);

  const active = f.q || f.label || f.priority || f.source || f.review;
  const filter = useCallback((c: Card) => {
    const t = c.task;
    if (f.q && !`${t.title} ${t.description} ${t.externalId ?? ""}`.toLowerCase().includes(f.q.toLowerCase())) return false;
    if (f.label && !(t.labels ?? []).includes(f.label)) return false;
    if (f.priority && t.priority !== f.priority) return false;
    if (f.source && t.source !== f.source) return false;
    if (f.review && !c.pendingApproval) return false;
    return true;
  }, [f]);

  // Workflows move cards: refresh on any workflow status change.
  useEventSocket<WorkflowEvent>("stream=workflows", true, (ev) => {
    if (ev.type === "workflow.status" || ev.type.startsWith("approval.")) {
      void qc.invalidateQueries({ queryKey: ["board"] });
      void qc.invalidateQueries({ queryKey: ["tasks"] });
    }
  });

  const status = (params.get("status") ?? undefined) as Schemas["TaskStatus"] | undefined;
  const tasks = useQuery({
    queryKey: ["tasks", selected === ALL ? undefined : selected, status],
    queryFn: () => unwrap(api.GET("/tasks", { params: { query: { projectId: selected === ALL ? undefined : selected || undefined, status } } })),
    enabled: view === "list" && !!selected,
    refetchInterval: 15000,
  });
  const setStatus = (value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set("status", value);
    else next.delete("status");
    setParams(next, { replace: true });
  };

  const toolbar = (
    <div className="tasks-toolbar" role="toolbar" aria-label="Board filters">
      <label className="search">
        <Search size={14} aria-hidden />
        <input type="search" placeholder="Search cards" value={f.q} onChange={(e) => setF({ ...f, q: e.target.value })} aria-label="Search cards" />
      </label>
      <select aria-label="Label" value={f.label} onChange={(e) => setF({ ...f, label: e.target.value })}>
        <option value="">any label</option>
        {labels.map((l) => <option key={l} value={l}>{l}</option>)}
      </select>
      <select aria-label="Priority" value={f.priority} onChange={(e) => setF({ ...f, priority: e.target.value })}>
        <option value="">any priority</option>
        {["urgent", "high", "medium", "low", "none"].map((p) => <option key={p} value={p}>{p}</option>)}
      </select>
      <select aria-label="Source" value={f.source} onChange={(e) => setF({ ...f, source: e.target.value })}>
        <option value="">any source</option>
        {[["manual", "Manual"], ["jira", "Jira"], ["clickup", "ClickUp"], ["trello", "Trello"]].map(([v, l]) => <option key={v} value={v}>{l}</option>)}
      </select>
      <label className="checkbox small">
        <input type="checkbox" checked={f.review} onChange={(e) => setF({ ...f, review: e.target.checked })} /> waiting for my review
      </label>
      {active && <button type="button" className="btn btn-ghost btn-sm" onClick={() => setF(NO_FILTERS)}>Clear</button>}
      {active && selected !== ALL && <span className="small faint">Dragging is off while filtering.</span>}
      {selected === ALL && <span className="small faint">All projects: lists with the same name are combined; pick a project to drag or edit lists.</span>}
    </div>
  );

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Tasks</h1>
          <p className="desc">Every task as a card on its project's lists. Click a card for a quick view; runs move cards by list role.</p>
        </div>
        <div className="actions" style={{ margin: 0 }}>
          <select aria-label="Project" value={selected} onChange={(e) => choose(e.target.value)} className="project-pick">
            <option value={ALL}>All projects</option>
            {projectList.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
          <div className="seg" role="radiogroup" aria-label="View">
            <button type="button" role="radio" aria-checked={view === "board"} className={`btn btn-sm${view === "board" ? " btn-primary" : ""}`} onClick={() => setView("board")}>
              <Columns3 size={14} aria-hidden /> Board
            </button>
            <button type="button" role="radio" aria-checked={view === "list"} className={`btn btn-sm${view === "list" ? " btn-primary" : ""}`} onClick={() => setView("list")}>
              <List size={14} aria-hidden /> List
            </button>
          </div>
          <button className="btn btn-primary" onClick={() => setShowForm((v) => !v)} aria-expanded={showForm}>
            <Plus size={15} aria-hidden /> New task
          </button>
        </div>
      </header>
      {showForm && <NewTaskForm defaultProject={selected === ALL ? undefined : selected} />}
      <ErrorNote error={projects.error ?? boardErr} />
      {projects.data && projectList.length === 0 && (
        <Empty icon={<ListChecks size={24} aria-hidden />}>No projects yet. <Link to="/projects">Create a project</Link> to add tasks.</Empty>
      )}

      {view === "board" && boardsReady && (
        <Board
          lists={lists}
          projectId={selected === ALL ? undefined : selected}
          queryKey={["board"]}
          projectNames={selected === ALL ? projectNames : undefined}
          onOpen={(c) => setOpen(c.task.id)}
          filter={active ? filter : undefined}
          toolbar={toolbar}
        />
      )}
      {view === "board" && !boardsReady && boardIds.length > 0 && !boardErr && <p className="loading">loading…</p>}

      {view === "list" && (
        <Panel
          title="Tasks"
          count={tasks.data?.items.length}
          actions={
            <select aria-label="Filter by status" value={status ?? ""} onChange={(e) => setStatus(e.target.value)}>
              <option value="">all statuses</option>
              {STATUSES.map((s) => <option key={s} value={s}>{label(s)}</option>)}
            </select>
          }
        >
          <ErrorNote error={tasks.error} />
          {tasks.data && tasks.data.items.length === 0 && (
            <Empty icon={<ListChecks size={24} aria-hidden />}>No tasks yet. Create one to start a workflow.</Empty>
          )}
          {tasks.data && tasks.data.items.length > 0 && (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr><th>Task</th><th>Project</th><th>Status</th><th>Updated</th></tr>
                </thead>
                <tbody>
                  {tasks.data.items.map((t) => (
                    <tr key={t.id}>
                      <td><button type="button" className="row-link linkish" onClick={() => setOpen(t.id)}>{t.title}</button></td>
                      <td className="small"><Link to={`/projects/${t.projectId}`}>{t.projectName}</Link></td>
                      <td><TaskStatus status={t.status} /></td>
                      <td className="small faint">{relativeTime(t.updatedAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      )}

      {open && <TaskDrawer taskId={open} onClose={() => setOpen(null)} invalidate={[["board"], ["tasks"]]} />}
    </>
  );
}
