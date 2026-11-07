import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ListChecks, Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Empty, ErrorNote, Field, Panel, label, relativeTime } from "../components/ui";
import { TaskStatus } from "./workflowUi";

const STATUSES: Schemas["TaskStatus"][] = ["in_progress", "awaiting_approval", "completed", "failed", "cancelled", "open"];

function NewTaskForm({ defaultProject }: { defaultProject?: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const projects = useQuery({ queryKey: ["projects"], queryFn: () => unwrap(api.GET("/projects")) });
  const [projectId, setProjectId] = useState(defaultProject ?? "");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: (pid: string) =>
      unwrap(api.POST("/projects/{projectId}/tasks", { params: { path: { projectId: pid } }, body: { title, description, start: true } })),
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
        <div className="actions">
          <button type="submit" className="btn btn-primary" disabled={create.isPending || !project || title.trim().length < 3}>
            <Plus size={15} aria-hidden /> Create and start workflow
          </button>
        </div>
        <ErrorNote error={create.error} />
      </form>
    </Panel>
  );
}

export function TasksPage() {
  const [params, setParams] = useSearchParams();
  const projectId = params.get("projectId") ?? undefined;
  const status = (params.get("status") ?? undefined) as Schemas["TaskStatus"] | undefined;
  const [showForm, setShowForm] = useState(false);
  const tasks = useQuery({
    queryKey: ["tasks", projectId, status],
    queryFn: () => unwrap(api.GET("/tasks", { params: { query: { projectId, status } } })),
    refetchInterval: 15000,
  });
  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  };

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Tasks</h1>
          <p className="desc">Each task runs the engineering workflow: analysis → requirements → scope approval → plan → architecture → plan approval → report.</p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowForm((v) => !v)} aria-expanded={showForm}>
          <Plus size={15} aria-hidden /> New task
        </button>
      </header>
      {showForm && <NewTaskForm defaultProject={projectId} />}

      <Panel
        title="Tasks"
        count={tasks.data?.items.length}
        actions={
          <select aria-label="Filter by status" value={status ?? ""} onChange={(e) => setFilter("status", e.target.value)}>
            <option value="">all statuses</option>
            {STATUSES.map((s) => (
              <option key={s} value={s}>{label(s)}</option>
            ))}
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
                <tr>
                  <th>Task</th>
                  <th>Project</th>
                  <th>Status</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {tasks.data.items.map((t) => (
                  <tr key={t.id}>
                    <td>
                      <Link to={`/tasks/${t.id}`} className="row-link">{t.title}</Link>
                    </td>
                    <td className="small">
                      <Link to={`/projects/${t.projectId}`}>{t.projectName}</Link>
                    </td>
                    <td><TaskStatus status={t.status} /></td>
                    <td className="small faint">{relativeTime(t.updatedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </>
  );
}
