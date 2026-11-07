import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, ArrowRight, ChevronRight, FileText, GitBranch, Link2, Plus, RefreshCw, ScanSearch, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api, unwrap, type Repository, type Schemas } from "../api/client";
import {
  Badge, CopyText, Empty, ErrorNote, Field, Panel, formatDate, label, relativeTime, shortSha, type Tone,
} from "../components/ui";
import { projectsKey } from "./ProjectsPage";

const ROLES: Schemas["RepositoryRole"][] = [
  "backend", "frontend", "mobile", "shared_library", "infrastructure", "documentation", "other",
];
const RELATIONSHIP_KINDS: Schemas["RepositoryRelationshipKind"][] = ["depends_on", "consumes_api", "shares_library", "deploys"];
const RESOURCE_KINDS: Schemas["ProjectResourceKind"][] = ["documentation", "external_service", "database_definition", "other"];

const indexTone: Record<Schemas["RepositoryIndexStatus"], Tone> = {
  not_indexed: "neutral",
  queued: "info",
  indexing: "info",
  indexed: "ok",
  failed: "bad",
};

export function ProjectDetailPage() {
  const { projectId = "" } = useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const path = { params: { path: { projectId } } };

  const project = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}", path)),
  });
  const repos = useQuery({
    queryKey: ["project", projectId, "repositories"],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/repositories", path)),
  });
  const invalidate = () => qc.invalidateQueries({ queryKey: ["project", projectId] });

  const archive = useMutation({
    mutationFn: () => unwrap(api.DELETE("/projects/{projectId}", path)),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: projectsKey });
      navigate("/projects");
    },
  });

  if (project.error) return <ErrorNote error={project.error} />;
  if (!project.data) return <p className="loading">loading…</p>;
  const p = project.data;
  const repoList = repos.data?.items ?? [];
  const readOnly = !!p.archivedAt;
  const indexed = repoList.filter((r) => r.indexStatus === "indexed").length;
  const gitErrors = repoList.filter((r) => r.gitError).length;

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/projects">projects</Link>
        <ChevronRight size={12} aria-hidden />
        <span className="mono">{p.slug}</span>
      </nav>
      <header className="page-header">
        <div>
          <h1>{p.name}</h1>
          <div className="meta">
            <CopyText value={p.id} display={`id:${p.id.slice(0, 8)}`} label="project id" />
            <CopyText value={p.slug} label="slug" />
            {readOnly ? <Badge tone="warn">archived {relativeTime(p.archivedAt)}</Badge> : <Badge tone="ok">active</Badge>}
            <span className="small faint" title={formatDate(p.updatedAt)}>
              updated {relativeTime(p.updatedAt)}
            </span>
          </div>
          {p.description && <p className="desc">{p.description}</p>}
        </div>
        {!readOnly && (
          <button
            className="btn btn-danger"
            disabled={archive.isPending}
            onClick={() => confirm(`Archive project "${p.name}"?`) && archive.mutate()}
          >
            <Archive size={15} aria-hidden /> Archive
          </button>
        )}
      </header>
      <ErrorNote error={archive.error} />

      <div className="stats">
        <Stat value={repoList.length} label="repositories" />
        <Stat value={`${indexed}/${repoList.length}`} label="indexed" />
        <Stat value={gitErrors} label="git errors" />
      </div>

      <Panel
        title="Repositories"
        count={repoList.length}
        footer={!readOnly && <AddRepositoryForm projectId={projectId} onCreated={invalidate} />}
      >
        <ErrorNote error={repos.error} />
        {repos.data && repoList.length === 0 ? (
          <Empty icon={<GitBranch size={28} aria-hidden />}>No repositories registered yet.</Empty>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Repository</th>
                  <th>Role</th>
                  <th>Branch</th>
                  <th>HEAD</th>
                  <th>Index</th>
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {repoList.map((r) => (
                  <RepositoryRow key={r.id} repo={r} onChange={invalidate} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <RelationshipsPanel projectId={projectId} repos={repoList} readOnly={readOnly} />
      <ResourcesPanel projectId={projectId} readOnly={readOnly} />
    </>
  );
}

function Stat({ value, label: text }: { value: number | string; label: string }) {
  return (
    <div className="stat">
      <div className="value">{value}</div>
      <div className="label">{text}</div>
    </div>
  );
}

function RepositoryRow({ repo, onChange }: { repo: Repository; onChange: () => void }) {
  const path = { params: { path: { repositoryId: repo.id } } };
  const inspect = useMutation({ mutationFn: () => unwrap(api.POST("/repositories/{repositoryId}/inspect", path)), onSuccess: onChange });
  const index = useMutation({ mutationFn: () => unwrap(api.POST("/repositories/{repositoryId}/index", path)), onSuccess: onChange });
  const remove = useMutation({ mutationFn: () => unwrap(api.DELETE("/repositories/{repositoryId}", path)), onSuccess: onChange });
  const error = inspect.error ?? index.error ?? remove.error;
  const location = repo.localPath ?? repo.remoteUrl ?? "";

  return (
    <>
      <tr>
        <td>
          <div className="row-link">{repo.name}</div>
          <div className="cell-sub">
            <CopyText value={location} label={repo.sourceType === "local" ? "path" : "remote URL"} maxWidth={360} />
          </div>
        </td>
        <td>
          <span className="small">{label(repo.role)}</span>
          <div className="cell-sub small faint">{repo.sourceType}</div>
        </td>
        <td className="mono">
          {repo.currentBranch ?? <span className="faint">—</span>}
          {repo.defaultBranch && repo.defaultBranch !== repo.currentBranch && (
            <div className="cell-sub small faint">default: {repo.defaultBranch}</div>
          )}
        </td>
        <td>
          {repo.headSha ? (
            <CopyText value={repo.headSha} display={shortSha(repo.headSha)} label="commit SHA" />
          ) : (
            <span className="faint mono small">{repo.sourceType === "local" ? "no commits" : "—"}</span>
          )}
          {repo.gitError && (
            <div className="cell-sub">
              <Badge tone="bad">git error</Badge>
            </div>
          )}
        </td>
        <td>
          <Badge tone={indexTone[repo.indexStatus]} pulse={repo.indexStatus === "queued" || repo.indexStatus === "indexing"}>
            {label(repo.indexStatus)}
          </Badge>
        </td>
        <td className="actions">
          {repo.sourceType === "local" && (
            <button
              className="btn btn-sm"
              onClick={() => inspect.mutate()}
              disabled={inspect.isPending}
              title={`Refresh git info · last ${formatDate(repo.gitInspectedAt)}`}
            >
              <RefreshCw size={13} aria-hidden /> Inspect
            </button>
          )}
          <button className="btn btn-sm" onClick={() => index.mutate()} disabled={index.isPending} title="Queue for indexing">
            <ScanSearch size={13} aria-hidden /> Index
          </button>
          <button
            className="btn btn-sm btn-danger btn-icon"
            onClick={() => confirm(`Unregister "${repo.name}"? Files on disk are not touched.`) && remove.mutate()}
            disabled={remove.isPending}
            aria-label={`Unregister ${repo.name}`}
            title="Unregister"
          >
            <Trash2 size={13} aria-hidden />
          </button>
        </td>
      </tr>
      {(error || repo.gitError) && (
        <tr className="detail-row">
          <td colSpan={6}>
            <ErrorNote error={error ?? repo.gitError} />
          </td>
        </tr>
      )}
    </>
  );
}

function AddRepositoryForm({ projectId, onCreated }: { projectId: string; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [role, setRole] = useState<Schemas["RepositoryRole"]>("backend");
  const [sourceType, setSourceType] = useState<Schemas["RepositorySourceType"]>("local");
  const [location, setLocation] = useState("");
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/projects/{projectId}/repositories", {
          params: { path: { projectId } },
          body: {
            name,
            role,
            sourceType,
            ...(sourceType === "local" ? { localPath: location } : { remoteUrl: location }),
          },
        }),
      ),
    onSuccess: () => {
      setName("");
      setLocation("");
      onCreated();
    },
  });
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  return (
    <form onSubmit={onSubmit}>
      <div className="form-grid">
        <Field label="Name">
          <input value={name} onChange={(e) => setName(e.target.value)} required placeholder="api" />
        </Field>
        <Field label="Role">
          <select value={role} onChange={(e) => setRole(e.target.value as Schemas["RepositoryRole"])}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {label(r)}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Source">
          <select value={sourceType} onChange={(e) => setSourceType(e.target.value as Schemas["RepositorySourceType"])}>
            <option value="local">local</option>
            <option value="remote">remote</option>
          </select>
        </Field>
        <Field
          label={sourceType === "local" ? "Absolute path" : "Remote URL"}
          hint={sourceType === "local" ? "Must be inside WISE_REPO_ROOTS" : "https://, ssh:// or git@host:path — no credentials"}
          grow
        >
          <input
            className="mono"
            placeholder={sourceType === "local" ? "/home/you/Projects/api" : "git@github.com:org/repo.git"}
            value={location}
            onChange={(e) => setLocation(e.target.value)}
            required
          />
        </Field>
        <button type="submit" className="btn btn-primary" disabled={create.isPending}>
          <Plus size={15} aria-hidden /> Add repository
        </button>
      </div>
      <ErrorNote error={create.error} />
    </form>
  );
}

function RelationshipsPanel({ projectId, repos, readOnly }: { projectId: string; repos: Repository[]; readOnly: boolean }) {
  const qc = useQueryClient();
  const key = ["project", projectId, "relationships"];
  const rels = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET("/projects/{projectId}/repository-relationships", { params: { path: { projectId } } })),
  });
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [kind, setKind] = useState<Schemas["RepositoryRelationshipKind"]>("depends_on");
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/projects/{projectId}/repository-relationships", {
          params: { path: { projectId } },
          body: { fromRepositoryId: from, toRepositoryId: to, kind },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const remove = useMutation({
    mutationFn: (id: string) =>
      unwrap(api.DELETE("/repository-relationships/{relationshipId}", { params: { path: { relationshipId: id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const nameOf = (id: string) => repos.find((r) => r.id === id)?.name ?? id.slice(0, 8);
  const items = rels.data?.items ?? [];

  return (
    <Panel
      title="Relationships"
      count={items.length}
      footer={
        !readOnly &&
        repos.length >= 2 && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <div className="form-grid">
              <Field label="From">
                <RepoSelect repos={repos} value={from} onChange={setFrom} />
              </Field>
              <Field label="Relation">
                <select value={kind} onChange={(e) => setKind(e.target.value as Schemas["RepositoryRelationshipKind"])}>
                  {RELATIONSHIP_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {label(k)}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="To">
                <RepoSelect repos={repos} value={to} onChange={setTo} />
              </Field>
              <button type="submit" className="btn btn-primary" disabled={create.isPending || !from || !to}>
                <Link2 size={15} aria-hidden /> Link
              </button>
            </div>
            <ErrorNote error={create.error} />
          </form>
        )
      }
    >
      <ErrorNote error={rels.error ?? remove.error} />
      {rels.data && items.length === 0 && (
        <Empty icon={<Link2 size={24} aria-hidden />}>
          {repos.length < 2 ? "Add at least two repositories to link them." : "No relationships defined."}
        </Empty>
      )}
      <ul className="list">
        {items.map((rel) => (
          <li key={rel.id}>
            <span className="edge">
              {nameOf(rel.fromRepositoryId)}
              <ArrowRight size={13} aria-hidden />
              <Badge tone="info" dot={false}>
                {label(rel.kind)}
              </Badge>
              <ArrowRight size={13} aria-hidden />
              {nameOf(rel.toRepositoryId)}
            </span>
            {rel.note && <span className="muted small">— {rel.note}</span>}
            <span className="spacer" />
            {!readOnly && (
              <button className="btn btn-ghost btn-icon" onClick={() => remove.mutate(rel.id)} aria-label="Remove relationship" title="Remove">
                <Trash2 size={13} aria-hidden />
              </button>
            )}
          </li>
        ))}
      </ul>
    </Panel>
  );
}

function RepoSelect({ repos, value, onChange }: { repos: Repository[]; value: string; onChange: (v: string) => void }) {
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)} required>
      <option value="">select…</option>
      {repos.map((r) => (
        <option key={r.id} value={r.id}>
          {r.name}
        </option>
      ))}
    </select>
  );
}

function ResourcesPanel({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const qc = useQueryClient();
  const key = ["project", projectId, "resources"];
  const resources = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET("/projects/{projectId}/resources", { params: { path: { projectId } } })),
  });
  const [kind, setKind] = useState<Schemas["ProjectResourceKind"]>("documentation");
  const [name, setName] = useState("");
  const [uri, setUri] = useState("");
  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/projects/{projectId}/resources", { params: { path: { projectId } }, body: { kind, name, uri } })),
    onSuccess: () => {
      setName("");
      setUri("");
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/project-resources/{resourceId}", { params: { path: { resourceId: id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const items = resources.data?.items ?? [];

  return (
    <Panel
      title="Resources"
      count={items.length}
      footer={
        !readOnly && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <div className="form-grid">
              <Field label="Kind">
                <select value={kind} onChange={(e) => setKind(e.target.value as Schemas["ProjectResourceKind"])}>
                  {RESOURCE_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {label(k)}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="Name">
                <input value={name} onChange={(e) => setName(e.target.value)} required placeholder="Product requirements" />
              </Field>
              <Field label="URI" grow>
                <input className="mono" value={uri} onChange={(e) => setUri(e.target.value)} placeholder="https://… (optional)" />
              </Field>
              <button type="submit" className="btn btn-primary" disabled={create.isPending}>
                <Plus size={15} aria-hidden /> Add resource
              </button>
            </div>
            <ErrorNote error={create.error} />
          </form>
        )
      }
    >
      <ErrorNote error={resources.error ?? remove.error} />
      {resources.data && items.length === 0 && (
        <Empty icon={<FileText size={24} aria-hidden />}>No documentation, services or database definitions attached.</Empty>
      )}
      <ul className="list">
        {items.map((r) => (
          <li key={r.id}>
            <Badge dot={false}>{label(r.kind)}</Badge>
            <strong>{r.name}</strong>
            {r.uri && <CopyText value={r.uri} label="URI" maxWidth={420} />}
            <span className="spacer" />
            {!readOnly && (
              <button className="btn btn-ghost btn-icon" onClick={() => remove.mutate(r.id)} aria-label={`Remove ${r.name}`} title="Remove">
                <Trash2 size={13} aria-hidden />
              </button>
            )}
          </li>
        ))}
      </ul>
    </Panel>
  );
}
