import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, Brain, Check, History, Pencil, Plus, RotateCcw, Search, X } from "lucide-react";
import { useRef, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, Empty, ErrorNote, Field, Panel, relativeTime } from "../components/ui";

type Memory = Schemas["Memory"];
type Input = Schemas["MemoryInput"];

export const KINDS: Schemas["MemoryKind"][] = ["rule", "preference", "decision", "fact", "convention", "example"];
export const CATEGORIES: Schemas["MemoryCategory"][] = ["coding", "architecture", "testing", "api", "database", "security", "tooling", "product", "other"];
const KIND_HINT: Record<string, string> = {
  rule: "always given to agents; must be followed",
  preference: "guides recommendations when relevant",
  decision: "a choice made for the project, with its reason",
  fact: "durable project knowledge",
  convention: "how things are named or organized",
  example: "an approved approach worth reusing",
};
const SOURCE_LABEL: Record<string, string> = {
  manual: "written by you", review_feedback: "from a review comment", report: "from a final report", profile_inference: "inferred from repositories", task: "extracted from a task",
};

export function useMemoryInbox() {
  return useQuery({ queryKey: ["memories", "inbox"], queryFn: () => unwrap(api.GET("/memories/inbox")), refetchInterval: 20000 });
}

/** Form for a memory's content; used to create, edit and edit-then-approve. */
function MemoryForm({ initial, submitLabel, onSubmit, onCancel, pending, error }: {
  initial: Input; submitLabel: string; onSubmit: (v: Input) => void; onCancel: () => void; pending: boolean; error: unknown;
}) {
  const projects = useQuery({ queryKey: ["projects"], queryFn: () => unwrap(api.GET("/projects")) });
  const [v, setV] = useState<Input>(initial);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit(v);
  };
  return (
    <form className="memory-form" onSubmit={submit} aria-label={submitLabel}>
      <div className="form-grid">
        <Field label="Applies to">
          <select value={v.scope === "project" ? v.projectId ?? "" : ""} onChange={(e) => setV({ ...v, scope: e.target.value ? "project" : "developer", projectId: e.target.value || null })}>
            <option value="">me (all projects)</option>
            {projects.data?.items.map((p) => <option key={p.id} value={p.id}>project: {p.name}</option>)}
          </select>
        </Field>
        <Field label="Kind" hint={KIND_HINT[v.kind]}>
          <select value={v.kind} onChange={(e) => setV({ ...v, kind: e.target.value as Input["kind"] })}>
            {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
          </select>
        </Field>
        <Field label="Category">
          <select value={v.category ?? "other"} onChange={(e) => setV({ ...v, category: e.target.value as Input["category"] })}>
            {CATEGORIES.map((c) => <option key={c} value={c}>{c}</option>)}
          </select>
        </Field>
      </div>
      <Field label="Title" grow>
        <input value={v.title} onChange={(e) => setV({ ...v, title: e.target.value })} maxLength={200} required placeholder="Errors use RFC 7807 problem details" />
      </Field>
      <Field label="Details" grow>
        <textarea rows={4} value={v.body} onChange={(e) => setV({ ...v, body: e.target.value })} maxLength={8000} required placeholder="What it means and why." />
      </Field>
      <div className="actions" style={{ margin: 0 }}>
        <button type="submit" className="btn btn-primary btn-sm" disabled={pending || !v.title.trim() || !v.body.trim()}><Check size={13} aria-hidden /> {submitLabel}</button>
        <button type="button" className="btn btn-ghost btn-sm" onClick={onCancel}>Cancel</button>
      </div>
      <ErrorNote error={error} />
    </form>
  );
}

const toInput = (m: Memory): Input => ({ scope: m.scope, projectId: m.projectId ?? null, kind: m.kind, category: m.category, title: m.title, body: m.body });

function MemoryItem({ m, byId }: { m: Memory; byId: Map<string, Memory> }) {
  const qc = useQueryClient();
  const [mode, setMode] = useState<"view" | "edit" | "approve" | "history">("view");
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["memories"] });
    setMode("view");
  };
  const path = { params: { path: { memoryId: m.id } } };
  const act = useMutation({
    mutationFn: (a: "approve" | "reject" | "archive" | "reactivate") => {
      switch (a) {
        case "approve": return unwrap(api.POST("/memories/{memoryId}/approve", path));
        case "reject": return unwrap(api.POST("/memories/{memoryId}/reject", path));
        case "archive": return unwrap(api.POST("/memories/{memoryId}/archive", path));
        default: return unwrap(api.POST("/memories/{memoryId}/reactivate", path));
      }
    },
    onSuccess: refresh,
  });
  const save = useMutation({
    mutationFn: (v: Input) => mode === "approve"
      ? unwrap(api.POST("/memories/{memoryId}/approve", { ...path, body: v }))
      : unwrap(api.PUT("/memories/{memoryId}", { ...path, body: v })),
    onSuccess: refresh,
  });
  const versions = useQuery({
    queryKey: ["memories", m.id, "versions", m.version],
    queryFn: () => unwrap(api.GET("/memories/{memoryId}/versions", path)),
    enabled: mode === "history",
  });
  const restore = useMutation({
    mutationFn: (version: number) => unwrap(api.POST("/memories/{memoryId}/versions/{version}/restore", { params: { path: { memoryId: m.id, version } } })),
    onSuccess: refresh,
  });
  const dup = m.duplicateOf ? byId.get(m.duplicateOf) : undefined;
  return (
    <li className={`memory ${m.status}`}>
      <div className="memory-head">
        <Badge tone={m.kind === "rule" ? "info" : "neutral"} dot={false}>{m.kind}</Badge>
        <strong>{m.title}</strong>
        <span className="small faint">{m.category} · {m.scope === "project" ? m.projectName ?? "project" : "you"}</span>
        {m.status !== "active" && <Badge tone={m.status === "candidate" ? "warn" : "neutral"} dot={false}>{m.status}</Badge>}
        <span className="spacer" />
        <span className="small faint" title={`version ${m.version}`}>{SOURCE_LABEL[m.source]} · {relativeTime(m.updatedAt)}</span>
      </div>
      {mode === "view" && (
        <>
          <p className="memory-body">{m.body}</p>
          {m.rationale && <p className="small muted memory-why">Why: {m.rationale}</p>}
          {m.duplicateOf && (
            <p className="small warn-text">Looks like a duplicate of “{dup?.title ?? "an existing memory"}”.</p>
          )}
          {typeof m.sourceRefs.taskId === "string" && (
            <p className="small"><Link to={`/tasks/${m.sourceRefs.taskId}`}>Source task →</Link></p>
          )}
          <div className="actions memory-actions">
            {m.status === "candidate" && (
              <>
                <button type="button" className="btn btn-primary btn-sm" onClick={() => act.mutate("approve")} disabled={act.isPending}><Check size={13} aria-hidden /> Approve</button>
                <button type="button" className="btn btn-sm" onClick={() => setMode("approve")}><Pencil size={13} aria-hidden /> Edit & approve</button>
                <button type="button" className="btn btn-ghost btn-sm" onClick={() => act.mutate("reject")} disabled={act.isPending}><X size={13} aria-hidden /> {m.duplicateOf ? "Reject duplicate" : "Reject"}</button>
              </>
            )}
            {m.status !== "candidate" && m.status !== "rejected" && (
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => setMode("edit")}><Pencil size={13} aria-hidden /> Edit</button>
            )}
            {m.status === "active" && <button type="button" className="btn btn-ghost btn-sm" onClick={() => act.mutate("archive")}><Archive size={13} aria-hidden /> Archive</button>}
            {m.status === "archived" && <button type="button" className="btn btn-ghost btn-sm" onClick={() => act.mutate("reactivate")}><RotateCcw size={13} aria-hidden /> Use again</button>}
            {m.version > 1 && <button type="button" className="btn btn-ghost btn-sm" onClick={() => setMode("history")}><History size={13} aria-hidden /> History ({m.version})</button>}
          </div>
          <ErrorNote error={act.error} />
        </>
      )}
      {(mode === "edit" || mode === "approve") && (
        <MemoryForm initial={toInput(m)} submitLabel={mode === "approve" ? "Approve" : "Save"} onSubmit={(v) => save.mutate(v)}
          onCancel={() => setMode("view")} pending={save.isPending} error={save.error} />
      )}
      {mode === "history" && (
        <div className="memory-history">
          <ul className="list history">
            {versions.data?.items.map((v) => (
              <li key={v.version} className="small">
                <span className="mono">v{v.version}</span> <strong>{v.title}</strong> <span className="faint">{v.status} · {relativeTime(v.createdAt)}</span>
                <span className="spacer" />
                {v.version !== m.version && <button type="button" className="btn btn-ghost btn-sm" onClick={() => restore.mutate(v.version)}>Restore</button>}
              </li>
            ))}
          </ul>
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => setMode("view")}>Close</button>
          <ErrorNote error={restore.error} />
        </div>
      )}
    </li>
  );
}

const TABS = [
  { id: "inbox", label: "Inbox", query: { status: "candidate" } },
  { id: "developer", label: "Your profile", query: { scope: "developer", status: "active" } },
  { id: "project", label: "Projects", query: { scope: "project", status: "active" } },
  { id: "archived", label: "Archived", query: { status: "archived" } },
] as const;

export function MemoryPage() {
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const inbox = useMemoryInbox();
  // Without ?tab, open the inbox if it has proposals — decided once, so polling never switches tabs.
  const defaultTab = useRef<string | null>(null);
  if (defaultTab.current === null && inbox.data) defaultTab.current = inbox.data.candidates > 0 ? "inbox" : "developer";
  const tab = TABS.find((t) => t.id === (params.get("tab") ?? defaultTab.current ?? "developer")) ?? TABS[1];
  const [q, setQ] = useState("");
  const [kind, setKind] = useState("");
  const [projectId, setProjectId] = useState("");
  const [adding, setAdding] = useState(false);
  const projects = useQuery({ queryKey: ["projects"], queryFn: () => unwrap(api.GET("/projects")) });
  const list = useQuery({
    queryKey: ["memories", tab.id, q, kind, projectId],
    queryFn: () => unwrap(api.GET("/memories", {
      params: { query: { ...tab.query, ...(q ? { q } : {}), ...(kind ? { kind: kind as Schemas["MemoryKind"] } : {}), ...(projectId ? { projectId } : {}) } },
    })),
  });
  const all = useQuery({ queryKey: ["memories", "all-active"], queryFn: () => unwrap(api.GET("/memories", { params: { query: { status: "active", limit: 500 } } })) });
  const byId = new Map((all.data?.items ?? []).map((m) => [m.id, m]));
  const create = useMutation({
    mutationFn: (v: Input) => unwrap(api.POST("/memories", { body: v })),
    onSuccess: () => {
      setAdding(false);
      void qc.invalidateQueries({ queryKey: ["memories"] });
    },
  });
  const items = list.data?.items ?? [];

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Memory</h1>
          <p className="desc">
            What Wise knows about how you work and about each project. Rules are given to every agent; other memories when
            they are relevant to the task. Memories proposed from your reviews and reports wait in the inbox until you approve them.
          </p>
        </div>
        <button type="button" className="btn btn-primary" onClick={() => setAdding((v) => !v)} aria-expanded={adding}>
          <Plus size={15} aria-hidden /> Add memory
        </button>
      </header>
      {adding && (
        <Panel title="New memory" flush={false}>
          <MemoryForm initial={{ scope: "developer", projectId: null, kind: "rule", category: "coding", title: "", body: "" }} submitLabel="Add"
            onSubmit={(v) => create.mutate(v)} onCancel={() => setAdding(false)} pending={create.isPending} error={create.error} />
        </Panel>
      )}
      <div className="tabs" role="tablist" aria-label="Memory">
        {TABS.map((t) => (
          <button key={t.id} type="button" role="tab" className="tab" aria-selected={tab.id === t.id} onClick={() => setParams({ tab: t.id }, { replace: true })}>
            {t.label}{t.id === "inbox" && (inbox.data?.candidates ?? 0) > 0 && <span className="nav-count">{inbox.data!.candidates}</span>}
          </button>
        ))}
      </div>
      <Panel
        title={tab.label}
        count={list.data ? items.length : undefined}
        actions={
          <div className="panel-actions memory-filters">
            <label className="search">
              <Search size={13} aria-hidden />
              <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search" aria-label="Search memories" />
            </label>
            <select aria-label="Kind" value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="">any kind</option>
              {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
            </select>
            {tab.id === "project" && (
              <select aria-label="Project" value={projectId} onChange={(e) => setProjectId(e.target.value)}>
                <option value="">all projects</option>
                {projects.data?.items.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            )}
          </div>
        }
      >
        <ErrorNote error={list.error} />
        {list.data && items.length === 0 && (
          <Empty icon={<Brain size={24} aria-hidden />}>
            {tab.id === "inbox" ? "Nothing to review. New proposals appear here after reviews with comments and finished workflows."
              : "No memories here yet. Add one, or approve proposals from the inbox."}
          </Empty>
        )}
        {items.length > 0 && <ul className="list memories">{items.map((m) => <MemoryItem key={m.id} m={m} byId={byId} />)}</ul>}
      </Panel>
    </>
  );
}
