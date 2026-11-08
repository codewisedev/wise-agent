import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Save, X } from "lucide-react";
import { useEffect, useState, type KeyboardEvent } from "react";
import { api, unwrap, type Schemas } from "../api/client";
import { ErrorNote, Field } from "./ui";

type Task = Schemas["Task"];
export type Priority = Schemas["TaskPriority"];
export const PRIORITIES: Priority[] = ["none", "low", "medium", "high", "urgent"];

/** Tags as chips: type and press Enter or comma; Backspace removes the last one. */
export function LabelInput({ value, onChange, suggestions = [], id }: {
  value: string[]; onChange: (v: string[]) => void; suggestions?: string[]; id?: string;
}) {
  const [text, setText] = useState("");
  const add = (raw: string) => {
    const l = raw.trim().slice(0, 40);
    if (l && !value.includes(l) && value.length < 20) onChange([...value, l]);
    setText("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      add(text);
    } else if (e.key === "Backspace" && !text && value.length) {
      onChange(value.slice(0, -1));
    }
  };
  const listId = id ? `${id}-suggestions` : undefined;
  return (
    <div className="label-input">
      {value.map((l) => (
        <span key={l} className="chip">
          {l}
          <button type="button" aria-label={`Remove label ${l}`} onClick={() => onChange(value.filter((x) => x !== l))}><X size={10} aria-hidden /></button>
        </span>
      ))}
      <input
        id={id} value={text} list={listId} placeholder={value.length ? "" : "add a label, Enter"} aria-label="Add label"
        onChange={(e) => (e.target.value.endsWith(",") ? add(e.target.value.slice(0, -1)) : setText(e.target.value))}
        onKeyDown={onKey} onBlur={() => text && add(text)}
      />
      {listId && (
        <datalist id={listId}>
          {suggestions.filter((s) => !value.includes(s)).map((s) => <option key={s} value={s} />)}
        </datalist>
      )}
    </div>
  );
}

/** The project's lists, for a column picker. */
export function useColumns(projectId: string | undefined) {
  return useQuery({
    queryKey: ["columns", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/columns", { params: { path: { projectId: projectId! } } })),
    enabled: !!projectId,
  });
}

/** Labels already used on a project's board (suggestions). */
export function useProjectLabels(projectId: string | undefined): string[] {
  const board = useQuery({
    queryKey: ["board", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/board", { params: { path: { projectId: projectId! } } })),
    enabled: !!projectId,
  });
  return [...new Set(board.data?.columns.flatMap((c) => c.cards.flatMap((x) => x.task.labels ?? [])) ?? [])].sort();
}

/** Edits a task's card fields: title, description, list, priority and labels. */
export function TaskDetailsForm({ task, onSaved, compact }: { task: Task; onSaved?: (t: Task) => void; compact?: boolean }) {
  const qc = useQueryClient();
  const columns = useColumns(task.projectId);
  const suggestions = useProjectLabels(task.projectId);
  const [title, setTitle] = useState(task.title);
  const [description, setDescription] = useState(task.description);
  const [priority, setPriority] = useState<Priority>(task.priority ?? "none");
  const [labels, setLabels] = useState<string[]>(task.labels ?? []);
  const [column, setColumn] = useState(task.column ?? "");
  const [sprint, setSprint] = useState(task.sprintId ?? "");
  const sprints = useQuery({
    queryKey: ["sprints", task.projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/sprints", { params: { path: { projectId: task.projectId } } })),
  });
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    if (dirty) return;
    setTitle(task.title);
    setDescription(task.description);
    setPriority(task.priority ?? "none");
    setLabels(task.labels ?? []);
    setColumn(task.column ?? "");
    setSprint(task.sprintId ?? "");
  }, [task, dirty]);
  const edit = <T,>(set: (v: T) => void) => (v: T) => { set(v); setDirty(true); };
  const save = useMutation({
    mutationFn: () => unwrap(api.PATCH("/tasks/{taskId}", {
      params: { path: { taskId: task.id } },
      body: {
        title: title.trim(), description, priority, labels, ...(column && column !== task.column ? { column } : {}),
        ...(sprint !== (task.sprintId ?? "") ? { sprint: sprint || "none" } : {}),
      },
    })),
    onSuccess: (updated) => {
      // Show the saved values right away (the effect re-syncs once dirty is cleared).
      qc.setQueryData<Schemas["TaskDetail"]>(["task", task.id], (old) => (old ? { ...old, task: updated } : old));
      setDirty(false);
      void qc.invalidateQueries({ queryKey: ["task", task.id] });
      void qc.invalidateQueries({ queryKey: ["board"] });
      void qc.invalidateQueries({ queryKey: ["tasks"] });
      onSaved?.(updated);
    },
  });
  const reset = () => setDirty(false);

  return (
    <form className="task-fields" onSubmit={(e) => { e.preventDefault(); save.mutate(); }} aria-label="Task details">
      <Field label="Title" grow>
        <input value={title} onChange={(e) => edit(setTitle)(e.target.value)} minLength={3} maxLength={200} required />
      </Field>
      <Field label="Description" grow>
        <textarea rows={compact ? 6 : 5} value={description} onChange={(e) => edit(setDescription)(e.target.value)} maxLength={20000} />
      </Field>
      <div className="form-grid">
        <Field label="List">
          <select value={column} onChange={(e) => edit(setColumn)(e.target.value)}>
            {columns.data?.items.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </select>
        </Field>
        {(sprints.data?.items.length ?? 0) > 0 && (
          <Field label="Sprint">
            <select value={sprint} onChange={(e) => edit(setSprint)(e.target.value)}>
              <option value="">no sprint</option>
              {sprints.data!.items.filter((s) => !s.closed || s.id === sprint).map((s) => (
                <option key={s.id} value={s.id}>{s.name}{s.current ? " (current)" : s.closed ? " (completed)" : ""}</option>
              ))}
            </select>
          </Field>
        )}
        <Field label="Priority">
          <select value={priority} onChange={(e) => edit(setPriority)(e.target.value as Priority)}>
            {PRIORITIES.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </Field>
      </div>
      <Field label="Labels" grow>
        <LabelInput id={`labels-${task.id}`} value={labels} onChange={edit(setLabels)} suggestions={suggestions} />
      </Field>
      <div className="actions">
        <button type="submit" className="btn btn-primary btn-sm" disabled={!dirty || title.trim().length < 3 || save.isPending}>
          <Save size={13} aria-hidden /> Save
        </button>
        {dirty && <button type="button" className="btn btn-ghost btn-sm" onClick={reset}>Discard</button>}
      </div>
      <ErrorNote error={save.error} />
    </form>
  );
}
