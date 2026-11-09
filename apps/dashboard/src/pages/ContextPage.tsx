import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ChevronRight, Code2, FileText, Package, ScanSearch, Search, Settings2, X } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, Empty, ErrorNote, Panel } from "../components/ui";

type Hit = Schemas["ContextHit"];
type Kind = Schemas["ContextKind"] | "";

const KIND_ICON: Record<string, typeof Code2> = { symbol: Code2, code: Code2, doc: FileText, config: Settings2 };

/** A file from the working tree, scrolled to the hit's lines. */
function FileViewer({ hit, onClose }: { hit: Hit; onClose: () => void }) {
  const file = useQuery({
    queryKey: ["repo-file", hit.repositoryId, hit.path],
    queryFn: () => unwrap(api.GET("/repositories/{repositoryId}/file", { params: { path: { repositoryId: hit.repositoryId }, query: { path: hit.path } } })),
  });
  const first = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    first.current?.scrollIntoView?.({ block: "center" });
  }, [file.data]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);
  const lines = file.data?.content.split("\n") ?? [];
  return (
    <>
      <div className="drawer-scrim" onClick={onClose} aria-hidden />
      <aside className="drawer drawer-wide" role="dialog" aria-modal="true" aria-label={`${hit.repository}/${hit.path}`}>
        <header className="drawer-head">
          <span className="mono small">{hit.repository}/{hit.path}</span>
          <span className="small faint">lines {hit.startLine}–{hit.endLine}</span>
          {file.data?.headSha && <span className="mono small faint" title="Working tree at this commit">{file.data.headSha.slice(0, 10)}</span>}
          {hit.stale && <Badge tone="warn" dot={false}>index older than HEAD</Badge>}
          <span className="spacer" />
          <button type="button" className="btn btn-ghost btn-icon" aria-label="Close" onClick={onClose} autoFocus><X size={15} aria-hidden /></button>
        </header>
        <ErrorNote error={file.error} />
        {file.data && (
          <pre className="code-view" aria-label="File content">
            {lines.map((l, i) => {
              const n = i + 1;
              const on = n >= hit.startLine && n <= hit.endLine;
              return (
                <span key={n} ref={n === hit.startLine ? first : undefined} className={on ? "hl" : undefined}>
                  <span className="ln" aria-hidden>{n}</span>{l}{"\n"}
                </span>
              );
            })}
            {file.data.truncated && <span className="faint">… truncated</span>}
          </pre>
        )}
      </aside>
    </>
  );
}

function HitRow({ h, onOpen }: { h: Hit; onOpen: (h: Hit) => void }) {
  const Icon = KIND_ICON[h.kind] ?? Code2;
  return (
    <li className="hit">
      <button type="button" className="hit-head linkish" onClick={() => onOpen(h)}>
        <Icon size={14} aria-hidden />
        <span className="mono">{h.repository}/{h.path}</span>
        <span className="faint small">:{h.startLine}</span>
      </button>
      <div className="hit-meta">
        {h.title && <strong className="small">{h.title}</strong>}
        <Badge dot={false}>{h.kind}</Badge>
        <span className="small faint">{h.why}</span>
        {h.stale && <Badge tone="warn" dot={false}>stale</Badge>}
      </div>
      {h.snippet && <pre className="hit-snippet">{h.snippet}</pre>}
    </li>
  );
}

export function ContextPage() {
  const { projectId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") ?? "search";
  const q = params.get("q") ?? "";
  const kind = (params.get("kind") ?? "") as Kind;
  const repositoryId = params.get("repo") ?? "";
  const [text, setText] = useState(q);
  const [open, setOpen] = useState<Hit | null>(null);
  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    setParams(next, { replace: true });
  };
  const project = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}", { params: { path: { projectId } } })),
  });
  const repos = useQuery({
    queryKey: ["project", projectId, "repositories"],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/repositories", { params: { path: { projectId } } })),
  });
  const indexed = repos.data?.items.filter((r) => r.indexedSha) ?? [];
  const search = useQuery({
    queryKey: ["context", projectId, tab, q, kind, repositoryId],
    queryFn: () => tab === "symbols"
      ? unwrap(api.GET("/projects/{projectId}/context/symbols", { params: { path: { projectId }, query: { name: q, ...(repositoryId ? { repositoryId } : {}) } } }))
      : unwrap(api.GET("/projects/{projectId}/context/search", {
        params: { path: { projectId }, query: { q, limit: 50, ...(kind ? { kind } : {}), ...(repositoryId ? { repositoryId } : {}) } },
      })),
    enabled: !!q && tab !== "dependencies",
  });
  const deps = useQuery({
    queryKey: ["context", projectId, "deps", repositoryId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/context/dependencies", { params: { path: { projectId }, query: repositoryId ? { repositoryId } : {} } })),
    enabled: tab === "dependencies",
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    set("q", text.trim());
  };

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/projects">projects</Link>
        <ChevronRight size={12} aria-hidden />
        <Link to={`/projects/${projectId}`}>{project.data?.name ?? "project"}</Link>
        <ChevronRight size={12} aria-hidden />
        <span>context</span>
      </nav>
      <header className="page-header">
        <div>
          <h1>Context</h1>
          <p className="desc">
            What agents can find in this project's repositories: symbols, code, docs and config from the code index, each tied
            to the commit it was indexed at. Results point to places to read; the real file is always one click away.
          </p>
        </div>
      </header>
      {repos.data && indexed.length === 0 && (
        <Empty icon={<ScanSearch size={24} aria-hidden />}>
          Nothing indexed yet. Use <strong>Index</strong> on a repository on the <Link to={`/projects/${projectId}`}>project page</Link>.
        </Empty>
      )}
      <div className="tabs" role="tablist" aria-label="Context">
        {[["search", "Search"], ["symbols", "Symbols"], ["dependencies", "Dependencies"]].map(([id, l]) => (
          <button key={id} type="button" role="tab" className="tab" aria-selected={tab === id} onClick={() => set("tab", id === "search" ? "" : id!)}>{l}</button>
        ))}
      </div>
      <Panel
        title={tab === "dependencies" ? "Dependencies" : tab === "symbols" ? "Symbols" : "Search"}
        count={tab === "dependencies" ? deps.data?.items.length : search.data?.items.length}
        actions={
          <select aria-label="Repository" value={repositoryId} onChange={(e) => set("repo", e.target.value)}>
            <option value="">all repositories</option>
            {repos.data?.items.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        }
      >
        {tab !== "dependencies" && (
          <form className="panel-body context-search" onSubmit={submit} role="search">
            <label className="search grow">
              <Search size={15} aria-hidden />
              <input
                value={text} onChange={(e) => setText(e.target.value)} autoFocus maxLength={tab === "symbols" ? 200 : 500}
                placeholder={tab === "symbols" ? "Symbol name, e.g. Reconfigure" : "Words or identifiers, e.g. retry policy for webhooks"}
                aria-label={tab === "symbols" ? "Symbol name" : "Search the code index"}
              />
            </label>
            {tab === "search" && (
              <select aria-label="Kind" value={kind} onChange={(e) => set("kind", e.target.value)}>
                <option value="">everything</option>
                <option value="symbol">symbols</option>
                <option value="code">code</option>
                <option value="doc">docs</option>
                <option value="config">config</option>
              </select>
            )}
            <button type="submit" className="btn btn-primary" disabled={!text.trim()}>Search</button>
          </form>
        )}
        <ErrorNote error={search.error ?? deps.error} />
        {tab !== "dependencies" && search.data?.note && (
          <p className="small muted panel-body" style={{ paddingTop: 0 }}><AlertTriangle size={13} aria-hidden /> {search.data.note}</p>
        )}
        {tab !== "dependencies" && (search.data?.items.length ?? 0) > 0 && (
          <ul className="list hits">{search.data!.items.map((h, i) => <HitRow key={`${h.path}:${h.startLine}:${i}`} h={h} onOpen={setOpen} />)}</ul>
        )}
        {tab === "dependencies" && deps.data && (
          deps.data.items.length === 0 ? <Empty icon={<Package size={24} aria-hidden />}>No dependencies found in the indexed manifests.</Empty> : (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Name</th><th>Version</th><th>Ecosystem</th><th>Repository</th><th>Manifest</th></tr></thead>
                <tbody>
                  {deps.data.items.map((d, i) => (
                    <tr key={`${d.repository}-${d.manifest}-${d.name}-${i}`} className={d.dev ? "is-dim" : ""}>
                      <td className="mono">{d.name}{d.dev && <span className="faint small"> (dev)</span>}</td>
                      <td className="mono small">{d.version}</td>
                      <td className="small">{d.ecosystem}</td>
                      <td className="small">{d.repository}</td>
                      <td className="mono small faint">{d.manifest}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        )}
      </Panel>
      {open && <FileViewer hit={open} onClose={() => setOpen(null)} />}
    </>
  );
}
