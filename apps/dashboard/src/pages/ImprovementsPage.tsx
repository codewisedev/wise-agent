import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Sparkles } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import type { components } from "../api/schema.gen";
import { Badge, Empty, ErrorNote, formatDate, Panel, relativeTime } from "../components/ui";
import { lineDiff, withContext } from "../lib/lineDiff";

type Proposal = components["schemas"]["ImprovementProposal"];
type Analysis = components["schemas"]["ImprovementAnalysis"];
type Status = Proposal["status"];

const STATUS_TONE: Record<Status, "ok" | "warn" | "bad" | "info" | "neutral"> = {
  proposed: "info", evaluating: "info", evaluated: "info", applied: "ok", rejected: "neutral", rolled_back: "warn", stale: "warn",
};
const KIND_LABEL: Record<Proposal["kind"], string> = { prompt: "Agent prompt", memory: "Memory", routing: "Model routing", template: "Workflow template" };

export function useImprovementInbox() {
  return useQuery({
    queryKey: ["improvements", "proposals", "open"],
    queryFn: () => unwrap(api.GET("/improvements/proposals", { params: { query: { status: "proposed" } } })),
    refetchInterval: 30000,
  });
}

export function Diff({ before, after }: { before: string; after: string }) {
  const lines = withContext(lineDiff(before, after));
  return (
    <pre className="text-diff" aria-label="Changes">
      {lines.map((l, i) =>
        l.op === "gap" ? (
          <span key={i} className="gap">… {l.count} unchanged line{l.count === 1 ? "" : "s"}</span>
        ) : (
          <span key={i} className={l.op}>
            <span aria-hidden className="sign">{l.op === "add" ? "+" : l.op === "del" ? "−" : " "}</span>
            {l.text || " "}
          </span>
        ),
      )}
    </pre>
  );
}

const pretty = (v: unknown) => JSON.stringify(v, null, 2) ?? "";

type MemOp = { op: string; ids?: string[]; scope?: string; kind?: string; category?: string; title?: string; body?: string };
type MemBefore = { id: string; title: string; body: string; kind: string; scope: string };

function ChangeView({ p }: { p: Proposal }) {
  const change = p.change as Record<string, unknown>;
  const before = p.before as Record<string, unknown>;
  switch (p.kind) {
    case "prompt":
      return <Diff before={String(before.prompt ?? "")} after={String(change.prompt ?? "")} />;
    case "routing":
      return <Diff before={pretty(before)} after={pretty(change.value)} />;
    case "template":
      return <Diff before={pretty(before.definition)} after={pretty(change.definition)} />;
    case "memory": {
      const mems = new Map(((before.memories as MemBefore[] | undefined) ?? []).map((m) => [m.id, m]));
      return (
        <ul className="memory-ops">
          {((change.ops as MemOp[]) ?? []).map((op, i) => (
            <li key={i}>
              <Badge tone={op.op === "archive" ? "warn" : "info"} dot={false}>{op.op}</Badge>
              {op.ids?.map((id) => (
                <div key={id} className="small mem-old"><s>{mems.get(id)?.title ?? id}</s> <span className="faint">{mems.get(id)?.body}</span></div>
              ))}
              {op.op !== "archive" && (
                <div className="mem-new">
                  <strong>{op.title}</strong> <span className="small faint">{op.scope} · {op.kind} · {op.category}</span>
                  <div className="small">{op.body}</div>
                </div>
              )}
            </li>
          ))}
        </ul>
      );
    }
  }
}

function Evidence({ p }: { p: Proposal }) {
  const e = p.evidence;
  const short = (id: string) => id.slice(0, 8);
  const none = !e.run_ids?.length && !e.approval_ids?.length && !e.feedback_ids?.length && !e.memory_ids?.length;
  if (none) return null;
  return (
    <div className="evidence small">
      <span className="faint">Evidence:</span>
      {e.run_ids?.map((id) => <Link key={id} to={`/runs/${id}`} className="mono">run {short(id)}</Link>)}
      {e.approval_ids?.map((id) => <span key={id} className="mono">review {short(id)}</span>)}
      {e.feedback_ids?.map((id) => <span key={id} className="mono">rating {short(id)}</span>)}
      {e.memory_ids?.map((id) => <Link key={id} to="/memory" className="mono">memory {short(id)}</Link>)}
    </div>
  );
}

function targetLabel(p: Proposal) {
  if (p.kind === "prompt") return <Link to={`/agents/${p.target}`} className="mono">{p.target}</Link>;
  if (p.kind === "template") return <span>{String((p.before as { name?: string }).name ?? p.target)}</span>;
  if (p.kind === "routing") return <Link to={`/settings?tab=${p.target === "models" ? "models" : "policy"}`} className="mono">{p.target}</Link>;
  return null;
}

function ProposalCard({ p }: { p: Proposal }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(p.status === "proposed" || p.status === "evaluated");
  const [note, setNote] = useState("");
  const reject = useMutation({
    mutationFn: () => unwrap(api.POST("/improvements/proposals/{proposalId}/reject", { params: { path: { proposalId: p.id } }, body: { note } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["improvements"] }),
  });
  const decidable = p.status === "proposed" || p.status === "evaluated" || p.status === "stale";
  return (
    <article className="proposal">
      <header>
        <button type="button" className="btn btn-ghost proposal-title" aria-expanded={open} onClick={() => setOpen(!open)}>
          <strong>{p.title}</strong>
        </button>
        <span className="meta small">
          <Badge tone={STATUS_TONE[p.status]}>{p.status.replace("_", " ")}</Badge>
          <span>{KIND_LABEL[p.kind]}</span>
          {targetLabel(p)}
          <span className="faint" title={formatDate(p.createdAt)}>{relativeTime(p.createdAt)}</span>
        </span>
      </header>
      {open && (
        <div className="proposal-body">
          <p>{p.rationale}</p>
          <Evidence p={p} />
          {p.status === "stale" && (
            <p className="small warn-text">The target changed since this was proposed: it can only be rejected (a new analysis will reconsider it).</p>
          )}
          <ChangeView p={p} />
          {p.decisionNote && <p className="small faint">Your note: {p.decisionNote}</p>}
          {decidable && (
            <form className="proposal-actions" onSubmit={(e) => { e.preventDefault(); reject.mutate(); }}>
              <input aria-label="Why not (optional)" placeholder="Why not? (optional, remembered by later analyses)" value={note}
                maxLength={2000} onChange={(e) => setNote(e.target.value)} />
              <button type="submit" className="btn" disabled={reject.isPending}>Reject</button>
            </form>
          )}
          <ErrorNote error={reject.error} />
        </div>
      )}
    </article>
  );
}

function AnalysisRow({ a }: { a: Analysis }) {
  const [open, setOpen] = useState(false);
  const tone = a.status === "succeeded" ? "ok" : a.status === "failed" ? "bad" : a.status === "running" ? "info" : "neutral";
  return (
    <tr>
      <td title={formatDate(a.createdAt)}>{relativeTime(a.createdAt)}</td>
      <td>{a.trigger === "scheduled" ? `weekly ${a.period ?? ""}` : "on demand"}</td>
      <td><Badge tone={tone} pulse={a.status === "running"}>{a.status}</Badge></td>
      <td className="num">{a.proposals}</td>
      <td>
        {a.refused.length > 0 && (
          <button type="button" className="btn btn-ghost btn-sm" aria-expanded={open} onClick={() => setOpen(!open)}>
            {a.refused.length} refused
          </button>
        )}
        {open && (
          <ul className="small refused">
            {a.refused.map((r, i) => <li key={i}><strong>{r.title || r.kind}</strong> — {r.reason}</li>)}
          </ul>
        )}
        {a.error && <div className="small bad-text">{a.error}</div>}
        {a.status === "skipped" && <span className="small faint">too little to analyse yet</span>}
      </td>
      <td>{a.agentRunId && <Link to={`/runs/${a.agentRunId}`} className="small mono">run</Link>}</td>
    </tr>
  );
}

/** The Improvements inbox: proposals from Wise's analyses of its own performance, with their evidence and diff. */
export function ImprovementsPage() {
  const qc = useQueryClient();
  const [filter, setFilter] = useState<"open" | "all">("open");
  const proposals = useQuery({
    queryKey: ["improvements", "proposals", filter],
    queryFn: () => unwrap(api.GET("/improvements/proposals", { params: { query: filter === "open" ? { status: "proposed" } : {} } })),
  });
  const analyses = useQuery({
    queryKey: ["improvements", "analyses"],
    queryFn: () => unwrap(api.GET("/improvements/analyses")),
    refetchInterval: (q) => (q.state.data?.items.some((a) => a.status === "running") ? 3000 : false),
  });
  const running = analyses.data?.items.some((a) => a.status === "running");
  const analyse = useMutation({
    mutationFn: () => unwrap(api.POST("/improvements/analyses")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["improvements"] }),
  });
  const items = proposals.data?.items ?? [];
  return (
    <>
      <header className="page-header">
        <div>
          <h1>Improvements</h1>
          <p className="desc">
            Wise analyses its own performance — see <Link to="/insights">Insights</Link> — and proposes changes to agent prompts,
            memory, model routing and workflow templates, each with its evidence. Nothing changes until you apply it. Proposals can
            never touch tools, permissions, approvals, security, sandboxes, secrets, git access or runtime policies. Schedule in{" "}
            <Link to="/settings?tab=improvement">Settings → Improvement</Link>.
          </p>
        </div>
        <button type="button" className="btn btn-primary" onClick={() => analyse.mutate()} disabled={analyse.isPending || running}>
          <Sparkles size={14} aria-hidden /> {running ? "Analysing…" : "Analyse now"}
        </button>
      </header>
      <ErrorNote error={analyse.error ?? proposals.error} />

      <Panel title="Proposals" count={filter === "open" ? proposals.data?.openCount : items.length} flush={false}
        actions={
          <div className="segmented" role="group" aria-label="Show">
            {(["open", "all"] as const).map((f) => (
              <button key={f} type="button" className={`btn btn-sm${filter === f ? " active" : ""}`} aria-pressed={filter === f} onClick={() => setFilter(f)}>
                {f === "open" ? "Waiting for you" : "All"}
              </button>
            ))}
          </div>
        }>
        {proposals.data && items.length === 0 ? (
          <Empty>{filter === "open" ? "Nothing waiting. Run an analysis, or wait for the weekly one." : "No proposals yet."}</Empty>
        ) : (
          <div className="proposals">{items.map((p) => <ProposalCard key={p.id} p={p} />)}</div>
        )}
      </Panel>

      <Panel title="Analyses" count={analyses.data?.items.length}>
        <div className="table-wrap">
          <table>
            <thead><tr><th>When</th><th>Trigger</th><th>Status</th><th className="num">Proposals</th><th>Refused or notes</th><th /></tr></thead>
            <tbody>{analyses.data?.items.map((a) => <AnalysisRow key={a.id} a={a} />)}</tbody>
          </table>
        </div>
      </Panel>
    </>
  );
}
