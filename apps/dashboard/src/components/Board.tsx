import {
  DndContext, DragOverlay, KeyboardSensor, PointerSensor, closestCorners, useDroppable, useSensor, useSensors,
  type DragEndEvent, type DragOverEvent, type DragStartEvent,
} from "@dnd-kit/core";
import {
  SortableContext, arrayMove, horizontalListSortingStrategy, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useMutation, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { AlertTriangle, ExternalLink, Eye, GripVertical, MoreHorizontal, Plus, Trash2, Workflow, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { ROLES } from "../pages/SettingsPanels";
import { stepName, workflowTone } from "../pages/workflowUi";
import { ErrorNote, Field } from "./ui";

export type Card = Schemas["BoardCard"];
export type ColumnMeta = Omit<Schemas["BoardColumn"], "projectId"> & { projectId?: string };
export type BoardList = ColumnMeta & { cards: Card[] };

const SOURCE_LABEL: Record<string, string> = { jira: "Jira", clickup: "ClickUp", trello: "Trello" };

/** Position between two neighbours (items sort ascending by position). */
export function positionBetween(before?: number, after?: number): number {
  if (before === undefined && after === undefined) return 1;
  if (before === undefined) return after! - 1;
  if (after === undefined) return before + 1;
  return (before + after) / 2;
}

export function CardView({ card, dragging, project, onOpen }: { card: Card; dragging?: boolean; project?: string; onOpen?: (c: Card) => void }) {
  const t = card.task;
  const stage = card.workflowStage ? stepName(undefined, card.workflowStage) : undefined;
  const status = card.workflowStatus;
  return (
    <article className={`card${dragging ? " dragging" : ""}`}>
      {onOpen ? (
        <button type="button" className="card-title" onClick={() => !dragging && onOpen(card)}>{t.title}</button>
      ) : (
        <Link to={`/tasks/${t.id}`} className="card-title" onClick={(e) => dragging && e.preventDefault()}>{t.title}</Link>
      )}
      <div className="card-meta">
        {project && <span className="chip proj">{project}</span>}
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

function SortableCard({ card, disabled, project, onOpen }: { card: Card; disabled: boolean; project?: string; onOpen?: (c: Card) => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: card.task.id, disabled });
  return (
    <li
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.35 : 1 }}
      {...attributes}
      {...listeners}
      aria-roledescription={disabled ? undefined : "draggable card"}
    >
      <CardView card={card} project={project} onOpen={onOpen} />
    </li>
  );
}

function QuickAdd({ projectId, column, onAdded }: { projectId: string; column: ColumnMeta; onAdded: () => void }) {
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/projects/{projectId}/tasks", {
      params: { path: { projectId } }, body: { title: title.trim(), column: column.id, start: false },
    })),
    onSuccess: () => {
      setTitle("");
      onAdded();
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
        aria-label={`New task in ${column.name}`} maxLength={200}
        onKeyDown={(e) => e.key === "Escape" && setOpen(false)} onBlur={() => !title && setOpen(false)}
      />
      <ErrorNote error={add.error} />
    </form>
  );
}

type ColumnBody = { name: string; role?: Schemas["ColumnInput"]["role"]; color?: string; wipLimit?: number | null; position?: number };

function bodyOf(c: ColumnMeta, patch: Partial<ColumnBody> = {}): ColumnBody {
  return { name: c.name, role: c.role ?? "", color: c.color ?? "", wipLimit: c.wipLimit ?? null, position: c.position, ...patch };
}

/** Column settings: role, color, WIP limit, delete (moving its cards). */
function ColumnMenu({ column, others, cardCount, onClose, onChanged }: {
  column: ColumnMeta; others: ColumnMeta[]; cardCount: number; onClose: () => void; onChanged: () => void;
}) {
  const [role, setRole] = useState<string>(column.role ?? "");
  const [color, setColor] = useState(column.color ?? "#64748b");
  const [wip, setWip] = useState(column.wipLimit ? String(column.wipLimit) : "");
  const [moveTo, setMoveTo] = useState(others[0]?.id ?? "");
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      const t = e.target as HTMLElement;
      // The ⋯ toggle handles its own click (closing would immediately reopen).
      if (ref.current?.contains(t) || t.closest?.(".col-more")) return;
      onClose();
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [onClose]);
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/columns/{columnId}", {
      params: { path: { columnId: column.id } },
      body: bodyOf(column, { role: role as ColumnBody["role"], color, wipLimit: wip ? Number(wip) : null }),
    })),
    onSuccess: () => { onChanged(); onClose(); },
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/columns/{columnId}", {
      params: { path: { columnId: column.id }, query: cardCount > 0 ? { moveTo } : {} },
    })),
    onSuccess: () => { onChanged(); onClose(); },
  });
  return (
    <div className="col-menu" ref={ref} role="dialog" aria-label={`${column.name} settings`}>
      <Field label="Role" hint="Workflows and imports place cards by role">
        <select value={role} onChange={(e) => setRole(e.target.value)}>
          {ROLES.map((r) => <option key={r.v} value={r.v}>{r.l}</option>)}
        </select>
      </Field>
      <div className="form-grid">
        <Field label="Color"><input type="color" value={color} onChange={(e) => setColor(e.target.value)} /></Field>
        <Field label="WIP limit"><input type="number" min={1} max={1000} value={wip} placeholder="none" onChange={(e) => setWip(e.target.value)} style={{ width: 90 }} /></Field>
      </div>
      <div className="actions">
        <button type="button" className="btn btn-primary btn-sm" onClick={() => save.mutate()} disabled={save.isPending}>Save</button>
        <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
      </div>
      <hr />
      {others.length === 0 ? (
        <p className="small faint">The last list cannot be deleted.</p>
      ) : (
        <div className="form-grid">
          {cardCount > 0 && (
            <Field label={`Move its ${cardCount} card${cardCount === 1 ? "" : "s"} to`}>
              <select value={moveTo} onChange={(e) => setMoveTo(e.target.value)}>
                {others.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}
              </select>
            </Field>
          )}
          <button type="button" className="btn btn-danger btn-sm" onClick={() => window.confirm(`Delete the list “${column.name}”?`) && del.mutate()} disabled={del.isPending}>
            <Trash2 size={13} aria-hidden /> Delete list
          </button>
        </div>
      )}
      <ErrorNote error={save.error ?? del.error} />
    </div>
  );
}

function ColumnTitle({ column, editable, onChanged }: { column: ColumnMeta; editable: boolean; onChanged: () => void }) {
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(column.name);
  useEffect(() => setName(column.name), [column.name]);
  const rename = useMutation({
    mutationFn: (n: string) => unwrap(api.PUT("/columns/{columnId}", { params: { path: { columnId: column.id } }, body: bodyOf(column, { name: n }) })),
    onSuccess: onChanged,
    onError: () => setName(column.name),
  });
  if (!editable) return <h2>{column.name}</h2>;
  if (!editing) {
    return <h2><button type="button" className="col-name" onClick={() => setEditing(true)} title="Rename">{column.name}</button></h2>;
  }
  const commit = () => {
    setEditing(false);
    const n = name.trim();
    if (n && n !== column.name) rename.mutate(n);
    else setName(column.name);
  };
  return (
    <input
      className="col-rename" autoFocus value={name} maxLength={40} aria-label="List name"
      onChange={(e) => setName(e.target.value)} onBlur={commit}
      onKeyDown={(e) => { if (e.key === "Enter") commit(); if (e.key === "Escape") { setName(column.name); setEditing(false); } }}
    />
  );
}

function ColumnView({ list, total, projectId, others, editable, dndDisabled, projectNames, onOpen, onChanged }: {
  list: BoardList; total: number; projectId?: string; others: ColumnMeta[]; editable: boolean; dndDisabled: boolean;
  projectNames?: Record<string, string>; onOpen?: (c: Card) => void; onChanged: () => void;
}) {
  const { setNodeRef, isOver } = useDroppable({ id: `col:${list.id}`, disabled: dndDisabled });
  const sortable = useSortable({ id: `colsort:${list.id}`, disabled: !editable });
  const [menu, setMenu] = useState(false);
  const over = list.wipLimit != null && total > list.wipLimit;
  return (
    <section
      ref={sortable.setNodeRef}
      style={{ transform: CSS.Translate.toString(sortable.transform), transition: sortable.transition, opacity: sortable.isDragging ? 0.5 : 1, ...(list.color ? { ["--col-color" as string]: list.color } : {}) }}
      className={`col${isOver ? " over" : ""}${over ? " over-limit" : ""}${list.color ? " has-color" : ""}`}
      aria-label={list.name}
    >
      <header>
        {editable && (
          <button type="button" className="col-grip" aria-label={`Drag to reorder ${list.name}`} {...sortable.attributes} {...sortable.listeners}>
            <GripVertical size={13} aria-hidden />
          </button>
        )}
        <ColumnTitle column={list} editable={editable} onChanged={onChanged} />
        {list.role && <span className="col-role" title={`Role: ${list.role.replace("_", " ")}`}>{list.role.replace("_", " ")}</span>}
        <span className={`count${over ? " warn" : ""}`} title={over ? "Over the WIP limit" : undefined}>
          {list.cards.length < total ? `${list.cards.length} of ${total}` : total}{list.wipLimit != null && ` / ${list.wipLimit}`}
        </span>
        {editable && (
          <button type="button" className="btn btn-ghost btn-icon col-more" aria-label={`${list.name} settings`} aria-expanded={menu} onClick={() => setMenu((v) => !v)}>
            <MoreHorizontal size={14} aria-hidden />
          </button>
        )}
      </header>
      {menu && <ColumnMenu column={list} others={others} cardCount={total} onClose={() => setMenu(false)} onChanged={onChanged} />}
      <SortableContext items={list.cards.map((c) => c.task.id)} strategy={verticalListSortingStrategy}>
        <ul ref={setNodeRef} className="col-cards">
          {list.cards.map((c) => (
            <SortableCard key={c.task.id} card={c} disabled={dndDisabled} project={projectNames?.[c.task.projectId]} onOpen={onOpen} />
          ))}
        </ul>
      </SortableContext>
      {projectId && <QuickAdd projectId={projectId} column={list} onAdded={onChanged} />}
    </section>
  );
}

function AddList({ projectId, onChanged }: { projectId: string; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/projects/{projectId}/columns", { params: { path: { projectId } }, body: { name: name.trim() } })),
    onSuccess: () => { setName(""); setOpen(false); onChanged(); },
  });
  if (!open) {
    return (
      <button type="button" className="col col-new" onClick={() => setOpen(true)}>
        <Plus size={14} aria-hidden /> Add list
      </button>
    );
  }
  return (
    <form className="col col-new-form" onSubmit={(e) => { e.preventDefault(); if (name.trim()) add.mutate(); }}>
      <input autoFocus value={name} onChange={(e) => setName(e.target.value)} maxLength={40} placeholder="List name" aria-label="New list name"
        onKeyDown={(e) => e.key === "Escape" && setOpen(false)} />
      <div className="actions">
        <button type="submit" className="btn btn-primary btn-sm" disabled={!name.trim() || add.isPending}>Add list</button>
        <button type="button" className="btn btn-ghost btn-icon" aria-label="Cancel" onClick={() => setOpen(false)}><X size={14} aria-hidden /></button>
      </div>
      <ErrorNote error={add.error} />
    </form>
  );
}

/**
 * Trello-style board: vertical lists of cards. With a projectId it is fully
 * editable (drag cards and lists, add tasks and lists, list settings);
 * without one (e.g. all projects) it is read-only.
 */
export function Board({ lists, projectId, queryKey, projectNames, onOpen, filter, toolbar }: {
  lists: BoardList[];
  projectId?: string;
  queryKey: QueryKey;
  projectNames?: Record<string, string>;
  onOpen?: (c: Card) => void;
  filter?: (c: Card) => boolean;
  toolbar?: ReactNode;
}) {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey });
  const editable = !!projectId;
  // Filtering hides cards, so positions computed from the visible ones would be wrong: no card drag then.
  const dndDisabled = !editable || !!filter;
  // Local copy for drag-and-drop; the server copy wins whenever it changes.
  const [cols, setCols] = useState<BoardList[]>(lists);
  useEffect(() => setCols(lists), [lists]);
  const shown = useMemo(() => (filter ? cols.map((c) => ({ ...c, cards: c.cards.filter(filter) })) : cols), [cols, filter]);

  const moveCard = useMutation({
    mutationFn: (v: { id: string; column: string; position: number }) =>
      unwrap(api.PATCH("/tasks/{taskId}", { params: { path: { taskId: v.id } }, body: { column: v.column, position: v.position } })),
    onSettled: refresh,
  });
  const moveList = useMutation({
    mutationFn: (v: { col: ColumnMeta; position: number }) =>
      unwrap(api.PUT("/columns/{columnId}", { params: { path: { columnId: v.col.id } }, body: bodyOf(v.col, { position: v.position }) })),
    onSettled: refresh,
  });

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const [active, setActive] = useState<Card | null>(null);
  const where = useMemo(() => {
    const m = new Map<string, string>();
    for (const c of cols) for (const card of c.cards) m.set(card.task.id, c.id);
    return m;
  }, [cols]);
  const columnOf = (id: string): string | undefined =>
    id.startsWith("col:") ? id.slice(4) : id.startsWith("colsort:") ? id.slice(8) : where.get(id);
  const idx = (colId: string) => cols.findIndex((c) => c.id === colId);

  const onDragStart = (e: DragStartEvent) => {
    const id = String(e.active.id);
    if (id.startsWith("colsort:")) return;
    const col = cols[idx(columnOf(id) ?? "")];
    setActive(col?.cards.find((c) => c.task.id === id) ?? null);
  };
  // Moving a card across lists while dragging, so it shows where it will land.
  const onDragOver = (e: DragOverEvent) => {
    if (String(e.active.id).startsWith("colsort:")) return;
    const from = columnOf(String(e.active.id));
    const to = e.over ? columnOf(String(e.over.id)) : undefined;
    if (!from || !to || from === to) return;
    setCols((prev) => {
      const fi = prev.findIndex((c) => c.id === from);
      const ti = prev.findIndex((c) => c.id === to);
      const card = prev[fi]?.cards.find((c) => c.task.id === e.active.id);
      if (!card || ti < 0) return prev;
      const target = prev[ti]!.cards.filter((c) => c.task.id !== card.task.id);
      const overIdx = target.findIndex((c) => c.task.id === e.over!.id);
      target.splice(overIdx < 0 ? target.length : overIdx, 0, card);
      return prev.map((c, i) => (i === fi ? { ...c, cards: c.cards.filter((x) => x.task.id !== card.task.id) } : i === ti ? { ...c, cards: target } : c));
    });
  };
  const onDragEnd = (e: DragEndEvent) => {
    setActive(null);
    if (!e.over) return;
    const id = String(e.active.id);
    const overId = String(e.over.id);
    if (id.startsWith("colsort:")) {
      const from = idx(id.slice(8));
      const to = idx(columnOf(overId) ?? "");
      if (from < 0 || to < 0 || from === to) return;
      const next = arrayMove(cols, from, to);
      setCols(next);
      moveList.mutate({ col: next[to]!, position: positionBetween(next[to - 1]?.position, next[to + 1]?.position) });
      return;
    }
    const fi = idx(columnOf(id) ?? "");
    const ti = idx(columnOf(overId) ?? "");
    if (fi < 0 || ti < 0) return;
    // The target list comes from the drop target, so a drop lands right even
    // when no drag-over event moved the card (e.g. keyboard jumps).
    const fromCards = cols[fi]!.cards;
    const toCards = cols[ti]!.cards;
    const card = fromCards.find((c) => c.task.id === id);
    if (!card) return;
    let list: Card[];
    let at: number;
    const curIdx = toCards.findIndex((c) => c.task.id === id);
    const overIdx = toCards.findIndex((c) => c.task.id === overId);
    if (curIdx >= 0 && overIdx >= 0) {
      // Both in the target list: take the over card's slot (moves down land after it).
      list = arrayMove(toCards, curIdx, overIdx);
      at = overIdx;
    } else {
      list = toCards.filter((c) => c.task.id !== id);
      at = overIdx >= 0 ? overIdx : list.length;
      list.splice(at, 0, card);
    }
    setCols((prev) => prev.map((c, i) => (i === ti ? { ...c, cards: list } : i === fi ? { ...c, cards: c.cards.filter((x) => x.task.id !== id) } : c)));
    const position = positionBetween(list[at - 1]?.task.position, list[at + 1]?.task.position);
    const orig = lists.find((c) => c.cards.some((x) => x.task.id === id));
    if (orig?.id === cols[ti]!.id && orig.cards.findIndex((x) => x.task.id === id) === at) return;
    moveCard.mutate({ id, column: cols[ti]!.id, position });
  };

  return (
    <>
      {toolbar}
      <ErrorNote error={moveCard.error ?? moveList.error} />
      <DndContext sensors={sensors} collisionDetection={closestCorners} onDragStart={onDragStart} onDragOver={onDragOver} onDragEnd={onDragEnd} onDragCancel={() => { setActive(null); setCols(lists); }}>
        <div className="board">
          <SortableContext items={cols.map((c) => `colsort:${c.id}`)} strategy={horizontalListSortingStrategy}>
            {shown.map((c, i) => (
              <ColumnView
                key={c.id} list={c} total={cols[i]!.cards.length} projectId={projectId} others={cols.filter((o) => o.id !== c.id)} editable={editable}
                dndDisabled={dndDisabled} projectNames={projectNames} onOpen={onOpen} onChanged={refresh}
              />
            ))}
          </SortableContext>
          {projectId && <AddList projectId={projectId} onChanged={refresh} />}
        </div>
        <DragOverlay>{active ? <CardView card={active} dragging project={projectNames?.[active.task.projectId]} /> : null}</DragOverlay>
      </DndContext>
    </>
  );
}
