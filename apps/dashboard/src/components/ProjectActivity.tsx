import { useInfiniteQuery } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import { Badge, Empty, ErrorNote, Panel, formatDate, relativeTime, type Tone } from "./ui";

const KINDS: Record<string, string> = {
  task: "tasks", approval: "approvals", workflow: "workflows", run: "failed runs", branch: "branches", push: "pushes",
  pull_request: "pull requests", memory: "memories",
};

const LABEL: Record<string, string> = {
  "task.created": "task created", "approval.requested": "approval requested", "approval.approved": "approved", "approval.rejected": "changes requested",
  "workflow.started": "workflow started", "workflow.completed": "workflow completed", "workflow.failed": "workflow failed",
  "workflow.cancelled": "workflow cancelled", "run.failed": "agent run failed", "branch.delivered": "branch delivered",
  "branch.pushed": "branch pushed", "push.failed": "push failed", "pull_request.open": "pull request opened",
  "pull_request.merged": "pull request merged", "pull_request.closed": "pull request closed", "pull_request.failed": "pull request failed",
  "memory.active": "memory added", "memory.candidate": "memory proposed",
};

function tone(kind: string): Tone {
  if (kind.endsWith("failed") || kind === "approval.rejected") return "bad";
  if (kind === "workflow.completed" || kind === "approval.approved" || kind === "branch.pushed" || kind === "pull_request.merged") return "ok";
  if (kind.startsWith("approval.requested")) return "warn";
  return "neutral";
}

/** One chronological feed of what happened in a project. */
export function ProjectActivity({ projectId }: { projectId: string }) {
  const [kinds, setKinds] = useState<string[]>([]);
  const q = useInfiniteQuery({
    queryKey: ["project", projectId, "activity", kinds.join(",")],
    queryFn: ({ pageParam }) => unwrap(api.GET("/projects/{projectId}/activity", {
      params: { path: { projectId }, query: { limit: 40, ...(pageParam ? { before: pageParam } : {}), ...(kinds.length ? { kinds: kinds.join(",") } : {}) } },
    })),
    initialPageParam: "",
    getNextPageParam: (last) => last.next ?? undefined,
    refetchInterval: 30000,
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  const toggle = (k: string) => setKinds((cur) => (cur.includes(k) ? cur.filter((x) => x !== k) : [...cur, k]));
  return (
    <Panel title="Activity" flush={false}
      actions={
        <div className="chip-filter" role="group" aria-label="Show">
          {Object.entries(KINDS).map(([k, label]) => (
            <button key={k} type="button" className={`btn btn-sm${kinds.includes(k) ? " active" : ""}`} aria-pressed={kinds.includes(k)} onClick={() => toggle(k)}>{label}</button>
          ))}
        </div>
      }>
      <ErrorNote error={q.error} />
      {q.data && items.length === 0 ? <Empty>Nothing yet.</Empty> : (
        <ol className="timeline">
          {items.map((it) => (
            <li key={`${it.kind}-${it.refId}-${it.at}`}>
              <time className="small faint" dateTime={it.at} title={formatDate(it.at)}>{relativeTime(it.at)}</time>
              <Badge tone={tone(it.kind)} dot={false}>{LABEL[it.kind] ?? it.kind}</Badge>
              {it.taskId ? <Link to={`/tasks/${it.taskId}`}>{it.title}</Link> : it.kind.startsWith("run.") ? <Link to={`/runs/${it.refId}`} className="mono">{it.title}</Link> : <span>{it.title}</span>}
              {it.detail && <span className="small faint detail">{it.detail}</span>}
              {it.url && <a href={it.url} target="_blank" rel="noreferrer noopener" className="small">open <ExternalLink size={11} aria-hidden /></a>}
            </li>
          ))}
        </ol>
      )}
      {q.hasNextPage && (
        <button type="button" className="btn btn-sm" onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage}>Older</button>
      )}
    </Panel>
  );
}
