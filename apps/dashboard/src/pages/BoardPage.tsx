import {
  DndContext, DragOverlay, KeyboardSensor, PointerSensor, closestCorners, useDroppable, useSensor, useSensors,
  type DragEndEvent, type DragOverEvent, type DragStartEvent,
} from "@dnd-kit/core";
import { SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronRight, ExternalLink, Eye, Plus, Workflow } from "lucide-react";
import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { useEventSocket, type WorkflowEvent } from "../api/events";
import { ErrorNote } from "../components/ui";
import { stepName, workflowTone } from "./workflowUi";

type Card = Schemas["BoardCard"];
type Column = Schemas["BoardColumn"];

export const COLUMN_LABEL: Record<Column, string> = {
  backlog: "Backlog", todo: "To do", in_progress: "In progress", review: "Review", done: "Done",
};
const COLUMNS: Column[] = ["backlog", "todo", "in_progress", "review", "done"];
const SOURCE_LABEL: Record<string, string> = { jira: "Jira", clickup: "ClickUp", trello: "Trello" };

/** Position between two neighbours (cards sort ascending by position). */
export function positionBetween(before?: number, after?: number): number {
  if (before === undefined && after === undefined) return 1;
  if (before === undefined) return after! - 1;
  if (after === undefined) return before + 1;
  return (before + after) / 2;
}

function CardView({ card, dragging }: { card: Card; dragging?: boolean }) {
  const t = card.task;
  const stage = card.workflowStage ? stepName(undefined, card.workflowStage) : undefined;
  const status = card.workflowStatus;
  return (
    <article className={`card${dragging ? " dragging" : ""}`}>
      <Link to={`/tasks/${t.id}`} className="card-title" onClick={(e) => dragging && e.preventDefault()}>{t.title}</Link>
      <div className="card-meta">
        {t.priority && t.priority !== "none" && <span className={`chip prio-${t.priority}`}>{t.priority}</span>}
        {t.labels?.map((l) => <span key={l} className="chip">{l}</span>)}
        {t.source && t.source !== "manual" && (
          t.externalUrl
            ? <a href={t.externalUrl} target="_blank" rel="noreferrer" className="chip src" title={`Open in ${SOURCE_LABEL[t.source]}`}>{SOURCE_LABEL[t.source]} {t.externalId}<ExternalLink size={10} aria-hidden /></a>
            : <span className="chip src">{SOURCE_LABEL[t.source]} {t.externalId}</span>
        )}
      </div>
      {status && (
        <div className={`card-wf tone-${workflowTone[status]}`}>
          {card.pendingApproval ? <Eye size={12} aria-hidden /> : status === "awaiting_retry" || status === "failed" ? <AlertTriangle size={12} aria-hidden /> : <Workflow size={12} aria-hidden />}
          <span>
            {card.pendingApproval ? "waiting for your review" : status === "awaiting_retry" ? "step failed" : status.replace(/_/g, " ")}
            {stage && status !== "completed" && <> · {stage}</>}
          </span>
        </div>
      )}
    </article>
  );
}

function SortableCard({ card }: { card: Card }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: card.task.id });
  return (
    <li
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.35 : 1 }}
      {...attributes}
      {...listeners}
      aria-roledescription="draggable card"
    >
      <CardView card={card} />
    </li>
  );
}

function QuickAdd({ projectId, column }: { projectId: string; column: Column }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/projects/{projectId}/tasks", {
      params: { path: { projectId } }, body: { title: title.trim(), column, start: false },
    })),
    onSuccess: () => {
      setTitle("");
      void qc.invalidateQueries({ queryKey: ["board", projectId] });
    },
  });
  if (!open) {
    return (
      <button type="button" className="btn btn-ghost btn-sm col-add" onClick={() => setOpen(true)}>
        <Plus size={13} aria-hidden /> Add task
      </button>
    );
  }
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (title.trim().length >= 3) add.mutate();
  };
  return (
    <form onSubmit={submit} className="col-quickadd">
      <input
        autoFocus value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Task title, Enter to add"
        aria-label={`New task in ${COLUMN_LABEL[column]}`} maxLength={200}
        onKeyDown={(e) => e.key === "Escape" && setOpen(false)} onBlur={() => !title && setOpen(false)}
      />
      <ErrorNote error={add.error} />
    </form>
  );
}

function ColumnView({ projectId, id, cards }: { projectId: string; id: Column; cards: Card[] }) {
  const { setNodeRef, isOver } = useDroppable({ id: `col:${id}` });
  return (
    <section className={`col${isOver ? " over" : ""}`} aria-label={COLUMN_LABEL[id]}>
      <header>
        <h2>{COLUMN_LABEL[id]}</h2>
        <span className="count">{cards.length}</span>
      </header>
      <SortableContext items={cards.map((c) => c.task.id)} strategy={verticalListSortingStrategy}>
        <ul ref={setNodeRef} className="col-cards">
          {cards.map((c) => <SortableCard key={c.task.id} card={c} />)}
        </ul>
      </SortableContext>
      <QuickAdd projectId={projectId} column={id} />
    </section>
  );
}

export function BoardPage() {
  const { projectId = "" } = useParams();
  const qc = useQueryClient();
  const project = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}", { params: { path: { projectId } } })),
  });
  const board = useQuery({
    queryKey: ["board", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/board", { params: { path: { projectId } } })),
  });
  // Local copy for drag-and-drop; the server copy wins whenever it changes.
  const [cols, setCols] = useState<Record<Column, Card[]>>({ backlog: [], todo: [], in_progress: [], review: [], done: [] });
  useEffect(() => {
    if (!board.data) return;
    const next = { backlog: [], todo: [], in_progress: [], review: [], done: [] } as Record<Column, Card[]>;
    for (const c of board.data.columns) next[c.id] = c.cards;
    setCols(next);
  }, [board.data]);

  // Workflows move cards: refresh on any workflow status change.
  useEventSocket<WorkflowEvent>("stream=workflows", true, (ev) => {
    if (ev.type === "workflow.status" || ev.type.startsWith("approval.")) void qc.invalidateQueries({ queryKey: ["board", projectId] });
  });

  const move = useMutation({
    mutationFn: (v: { id: string; column: Column; position: number }) =>
      unwrap(api.PATCH("/tasks/{taskId}", { params: { path: { taskId: v.id } }, body: { column: v.column, position: v.position } })),
    onSettled: () => qc.invalidateQueries({ queryKey: ["board", projectId] }),
  });

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const [active, setActive] = useState<Card | null>(null);
  const where = useMemo(() => {
    const m = new Map<string, Column>();
    for (const c of COLUMNS) for (const card of cols[c]) m.set(card.task.id, c);
    return m;
  }, [cols]);
  const columnOf = (id: string): Column | undefined =>
    id.startsWith("col:") ? (id.slice(4) as Column) : where.get(id);

  const onDragStart = (e: DragStartEvent) => {
    const col = columnOf(String(e.active.id));
    setActive(col ? cols[col].find((c) => c.task.id === e.active.id) ?? null : null);
  };
  // Moving across columns while dragging, so the card shows where it will land.
  const onDragOver = (e: DragOverEvent) => {
    const from = columnOf(String(e.active.id));
    const to = e.over ? columnOf(String(e.over.id)) : undefined;
    if (!from || !to || from === to) return;
    setCols((prev) => {
      const card = prev[from].find((c) => c.task.id === e.active.id);
      if (!card) return prev;
      const target = prev[to].filter((c) => c.task.id !== card.task.id);
      const overIdx = target.findIndex((c) => c.task.id === e.over!.id);
      target.splice(overIdx < 0 ? target.length : overIdx, 0, card);
      return { ...prev, [from]: prev[from].filter((c) => c.task.id !== card.task.id), [to]: target };
    });
  };
  const onDragEnd = (e: DragEndEvent) => {
    setActive(null);
    if (!e.over) return;
    const id = String(e.active.id);
    const overId = String(e.over.id);
    const from = columnOf(id);
    const to = columnOf(overId);
    if (!from || !to) return;
    // The target column comes from the drop target, so a drop lands right even
    // when no drag-over event moved the card (e.g. keyboard jumps).
    const source = cols[from].filter((c) => c.task.id !== id);
    const card = cols[from].find((c) => c.task.id === id);
    if (!card) return;
    let list: Card[];
    let idx: number;
    const curIdx = cols[to].findIndex((c) => c.task.id === id);
    const overIdx = cols[to].findIndex((c) => c.task.id === overId);
    if (curIdx >= 0 && overIdx >= 0) {
      // Both in the target column: take the over card's slot (moves down land after it).
      list = arrayMove(cols[to], curIdx, overIdx);
      idx = overIdx;
    } else {
      list = cols[to].filter((c) => c.task.id !== id);
      idx = overIdx >= 0 ? overIdx : list.length;
      list.splice(idx, 0, card);
    }
    setCols((prev) => (from === to ? { ...prev, [to]: list } : { ...prev, [from]: source, [to]: list }));
    const position = positionBetween(list[idx - 1]?.task.position, list[idx + 1]?.task.position);
    const orig = board.data?.columns.find((c) => c.cards.some((x) => x.task.id === id));
    const origIdx = orig?.cards.findIndex((x) => x.task.id === id);
    if (orig?.id === to && origIdx === idx) return;
    move.mutate({ id, column: to, position });
  };

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/projects">projects</Link>
        <ChevronRight size={12} aria-hidden />
        <Link to={`/projects/${projectId}`}>{project.data?.name ?? "project"}</Link>
        <ChevronRight size={12} aria-hidden />
        <span>board</span>
      </nav>
      <header className="page-header">
        <div>
          <h1>{project.data?.name ?? "Board"}</h1>
          <p className="desc">Drag cards between columns. Running a task's workflow moves its card; reviews land in Review.</p>
        </div>
      </header>
      <ErrorNote error={board.error ?? move.error} />
      <DndContext sensors={sensors} collisionDetection={closestCorners} onDragStart={onDragStart} onDragOver={onDragOver} onDragEnd={onDragEnd} onDragCancel={() => setActive(null)}>
        <div className="board">
          {COLUMNS.map((c) => <ColumnView key={c} projectId={projectId} id={c} cards={cols[c]} />)}
        </div>
        <DragOverlay>{active ? <CardView card={active} dragging /> : null}</DragOverlay>
      </DndContext>
    </>
  );
}
