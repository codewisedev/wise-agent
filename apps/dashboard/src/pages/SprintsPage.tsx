import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarRange, Check, ChevronRight, Pencil, Play, Plug, Plus, Trash2, X } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, Empty, ErrorNote, Field, Panel } from "../components/ui";

type Sprint = Schemas["Sprint"];

export function useSprints(projectId: string | undefined) {
  return useQuery({
    queryKey: ["sprints", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/sprints", { params: { path: { projectId: projectId! } } })),
    enabled: !!projectId,
  });
}

/** "Nov 4 – Nov 15" (dates are calendar days, shown without time zones). */
export function sprintDates(s: Pick<Sprint, "startsOn" | "endsOn">): string {
  const f = (d?: string | null) => (d ? new Date(`${d}T00:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "");
  if (!s.startsOn && !s.endsOn) return "no dates";
  return `${f(s.startsOn) || "…"} – ${f(s.endsOn) || "…"}`;
}

function SprintForm({ projectId, sprint, onDone }: { projectId: string; sprint?: Sprint; onDone: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(sprint?.name ?? "");
  const [startsOn, setStartsOn] = useState(sprint?.startsOn ?? "");
  const [endsOn, setEndsOn] = useState(sprint?.endsOn ?? "");
  const save = useMutation({
    mutationFn: () => {
      const body = { name: name.trim(), startsOn: startsOn || null, endsOn: endsOn || null };
      return sprint
        ? unwrap(api.PUT("/sprints/{sprintId}", { params: { path: { sprintId: sprint.id } }, body }))
        : unwrap(api.POST("/projects/{projectId}/sprints", { params: { path: { projectId } }, body }));
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["sprints", projectId] });
      onDone();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  return (
    <form onSubmit={submit} className="sprint-form" aria-label={sprint ? `Edit ${sprint.name}` : "New sprint"}>
      <div className="form-grid">
        <Field label="Name" grow>
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required placeholder="Sprint 25" autoFocus />
        </Field>
        <Field label="Starts">
          <input type="date" value={startsOn} onChange={(e) => setStartsOn(e.target.value)} />
        </Field>
        <Field label="Ends">
          <input type="date" value={endsOn} min={startsOn || undefined} onChange={(e) => setEndsOn(e.target.value)} />
        </Field>
        <button type="submit" className="btn btn-primary" disabled={!name.trim() || save.isPending}>
          <Check size={15} aria-hidden /> {sprint ? "Save" : "Add sprint"}
        </button>
        <button type="button" className="btn btn-ghost btn-icon" aria-label="Cancel" onClick={onDone}><X size={15} aria-hidden /></button>
      </div>
      <ErrorNote error={save.error} />
    </form>
  );
}

function SprintRow({ s, projectId }: { s: Sprint; projectId: string }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["sprints", projectId] });
    void qc.invalidateQueries({ queryKey: ["board"] });
  };
  const start = useMutation({ mutationFn: () => unwrap(api.POST("/sprints/{sprintId}/start", { params: { path: { sprintId: s.id } } })), onSuccess: refresh });
  const complete = useMutation({ mutationFn: () => unwrap(api.POST("/sprints/{sprintId}/complete", { params: { path: { sprintId: s.id } } })), onSuccess: refresh });
  const del = useMutation({ mutationFn: () => unwrap(api.DELETE("/sprints/{sprintId}", { params: { path: { sprintId: s.id } } })), onSuccess: refresh });
  if (editing) return <li><SprintForm projectId={projectId} sprint={s} onDone={() => setEditing(false)} /></li>;
  return (
    <li className={s.closed ? "is-dim" : ""}>
      <CalendarRange size={14} aria-hidden />
      <strong>{s.name}</strong>
      {s.current && <Badge tone="ok">current</Badge>}
      {s.closed && <Badge dot={false}>completed</Badge>}
      {s.sourceName && <Badge tone="info" dot={false}>from {s.sourceName}</Badge>}
      <span className="small muted">{sprintDates(s)}</span>
      <span className="small faint">{s.openCount} open · {s.taskCount} tasks</span>
      <span className="spacer" />
      <Link to={`/tasks?projectId=${projectId}&sprint=${s.id}`} className="btn btn-ghost btn-sm">Open board</Link>
      {!s.current && (
        <button type="button" className="btn btn-sm" onClick={() => start.mutate()} disabled={start.isPending}
          title="Make this the current sprint; unfinished tasks created in Wise move to it">
          <Play size={13} aria-hidden /> Start
        </button>
      )}
      {!s.closed && (
        <button type="button" className="btn btn-ghost btn-sm" disabled={complete.isPending}
          onClick={() => window.confirm(`Complete ${s.name}? Unfinished tasks created in Wise move to the next sprint.`) && complete.mutate()}>
          <Check size={13} aria-hidden /> Complete
        </button>
      )}
      <button type="button" className="btn btn-ghost btn-icon" aria-label={`Edit ${s.name}`} onClick={() => setEditing(true)}><Pencil size={13} aria-hidden /></button>
      <button type="button" className="btn btn-ghost btn-icon" aria-label={`Delete ${s.name}`}
        onClick={() => window.confirm(`Delete ${s.name}? Its tasks stay, without a sprint.${s.sourceName ? " It comes back on the next sync while the source still has it." : ""}`) && del.mutate()}>
        <Trash2 size={13} aria-hidden />
      </button>
      <ErrorNote error={start.error ?? complete.error ?? del.error} />
    </li>
  );
}

export function SprintsPage() {
  const { projectId = "" } = useParams();
  const [adding, setAdding] = useState(false);
  const project = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}", { params: { path: { projectId } } })),
  });
  const sprints = useSprints(projectId);
  const items = sprints.data?.items ?? [];
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/projects">projects</Link>
        <ChevronRight size={12} aria-hidden />
        <Link to={`/projects/${projectId}`}>{project.data?.name ?? "project"}</Link>
        <ChevronRight size={12} aria-hidden />
        <span>sprints</span>
      </nav>
      <header className="page-header">
        <div>
          <h1>Sprints</h1>
          <p className="desc">
            The Tasks board shows the current sprint. When a sprint's end date passes, the next running one becomes current and
            unfinished tasks created in Wise move with it; imported tasks follow their tool. To pick up each new sprint
            automatically, add a task source that follows a ClickUp sprint folder or a Jira board.
          </p>
        </div>
        <div className="actions" style={{ margin: 0 }}>
          <Link to={`/projects/${projectId}/integrations`} className="btn btn-sm"><Plug size={14} aria-hidden /> Task sources</Link>
          <button type="button" className="btn btn-primary" onClick={() => setAdding((v) => !v)} aria-expanded={adding}>
            <Plus size={15} aria-hidden /> New sprint
          </button>
        </div>
      </header>
      <Panel title="Sprints" count={sprints.data ? items.length : undefined}>
        <ErrorNote error={sprints.error} />
        {adding && <div className="panel-body"><SprintForm projectId={projectId} onDone={() => setAdding(false)} /></div>}
        {sprints.data && items.length === 0 && !adding && (
          <Empty icon={<CalendarRange size={24} aria-hidden />}>No sprints yet. Add one, or let a task source create them.</Empty>
        )}
        {items.length > 0 && <ul className="list accounts sprints">{items.map((s) => <SprintRow key={s.id} s={s} projectId={projectId} />)}</ul>}
      </Panel>
    </>
  );
}
