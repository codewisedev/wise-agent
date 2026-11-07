import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FolderGit2, Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import { Badge, CopyText, Empty, ErrorNote, Field, Panel, relativeTime } from "../components/ui";

export const projectsKey = ["projects"] as const;

export function ProjectsPage() {
  const qc = useQueryClient();
  const [showArchived, setShowArchived] = useState(false);
  const projects = useQuery({
    queryKey: [...projectsKey, { showArchived }],
    queryFn: () => unwrap(api.GET("/projects", { params: { query: { includeArchived: showArchived } } })),
  });

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/projects", { body: { name, description } })),
    onSuccess: () => {
      setName("");
      setDescription("");
      void qc.invalidateQueries({ queryKey: projectsKey });
    },
  });
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  const items = projects.data?.items ?? [];

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Projects</h1>
          <p className="desc">Products Wise works on, each grouping its repositories and resources.</p>
        </div>
      </header>

      <Panel
        title="Projects"
        count={projects.data ? items.length : undefined}
        actions={
          <label className="checkbox">
            <input type="checkbox" checked={showArchived} onChange={(e) => setShowArchived(e.target.checked)} />
            Show archived
          </label>
        }
      >
        <ErrorNote error={projects.error} />
        {projects.isPending && <p className="loading">loading…</p>}
        {projects.data && items.length === 0 && (
          <Empty icon={<FolderGit2 size={28} aria-hidden />}>No projects yet. Create your first one below.</Empty>
        )}
        {items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Slug</th>
                  <th style={{ textAlign: "right" }}>Repos</th>
                  <th>Status</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {items.map((p) => (
                  <tr key={p.id} className={p.archivedAt ? "is-dim" : ""}>
                    <td>
                      <Link to={`/projects/${p.id}`} className="row-link">
                        {p.name}
                      </Link>
                      {p.description && <div className="cell-sub small muted">{p.description}</div>}
                    </td>
                    <td>
                      <CopyText value={p.slug} label="slug" />
                    </td>
                    <td className="num">{p.repositoryCount}</td>
                    <td>{p.archivedAt ? <Badge tone="warn">archived</Badge> : <Badge tone="ok">active</Badge>}</td>
                    <td className="muted" title={p.updatedAt}>
                      {relativeTime(p.updatedAt)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <Panel title="New project" flush={false}>
        <form className="form-grid" onSubmit={onSubmit}>
          <Field label="Name" hint="Slug is derived automatically">
            <input value={name} onChange={(e) => setName(e.target.value)} required maxLength={200} placeholder="Salon Booking Platform" />
          </Field>
          <Field label="Description" grow>
            <input value={description} onChange={(e) => setDescription(e.target.value)} maxLength={5000} placeholder="Optional" />
          </Field>
          <button type="submit" className="btn btn-primary" disabled={create.isPending}>
            <Plus size={15} aria-hidden /> Create project
          </button>
        </form>
        <ErrorNote error={create.error} />
      </Panel>
    </>
  );
}
