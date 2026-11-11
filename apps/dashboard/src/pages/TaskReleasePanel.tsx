import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, GitPullRequest, Upload } from "lucide-react";
import { useState } from "react";
import { api, unwrap } from "../api/client";
import type { components } from "../api/schema.gen";
import { Badge, CopyText, ErrorNote, Panel, relativeTime } from "../components/ui";

type Release = components["schemas"]["TaskRelease"];
type PendingApproval = Release["approvals"][number];

const short = (sha?: string) => (sha ? sha.slice(0, 12) : "");
const web = (remote?: string) => (remote ?? "").replace(/\.git$/, "");

function ApprovalCard({ ap, rel, taskId }: { ap: PendingApproval; rel: Release; taskId: string }) {
  const qc = useQueryClient();
  const [comment, setComment] = useState("");
  const done = () => {
    void qc.invalidateQueries({ queryKey: ["task", taskId] });
    void qc.invalidateQueries({ queryKey: ["approvals"] });
  };
  const approve = useMutation({
    mutationFn: () => unwrap(api.POST("/approvals/{approvalId}/approve", { params: { path: { approvalId: ap.id } }, body: { combinedHash: ap.combinedHash } })),
    onSuccess: done,
  });
  const decline = useMutation({
    mutationFn: () => unwrap(api.POST("/approvals/{approvalId}/reject", {
      params: { path: { approvalId: ap.id } }, body: { combinedHash: ap.combinedHash, comment: comment || (ap.kind === "push" ? "Not pushing now." : "No pull request.") },
    })),
    onSuccess: done,
  });
  const r = rel.request;
  const push = rel.pushes[0];
  return (
    <div className="approval-card release-approval" role="group" aria-label={ap.kind === "push" ? "Push approval" : "Pull request approval"}>
      {ap.kind === "push" && r ? (
        <>
          <p><strong>Push this branch?</strong> The git worker pushes exactly this commit; it never force-pushes, never touches {r.baseBranch} and never merges.</p>
          <dl className="kv small">
            <dt>Commit</dt><dd><CopyText value={r.sha ?? ""} display={short(r.sha)} label="commit" /></dd>
            <dt>Branch</dt><dd className="mono">{r.branch}</dd>
            <dt>To</dt><dd className="mono">{r.remote}</dd>
            <dt>As</dt><dd>{r.accountName}</dd>
            <dt>Base</dt><dd className="mono">{r.baseBranch}</dd>
          </dl>
        </>
      ) : (
        <p><strong>Open a pull request</strong> from <span className="mono">{push?.branch}</span> into <span className="mono">{push?.baseBranch}</span>? Merging stays with you.</p>
      )}
      <div className="proposal-actions">
        <button type="button" className="btn btn-primary" disabled={approve.isPending} onClick={() => approve.mutate()}>
          {ap.kind === "push" ? <><Upload size={14} aria-hidden /> Approve push</> : <><GitPullRequest size={14} aria-hidden /> Open pull request</>}
        </button>
        <input aria-label="Why not (optional)" placeholder="Why not? (optional)" value={comment} onChange={(e) => setComment(e.target.value)} maxLength={2000} />
        <button type="button" className="btn" disabled={decline.isPending} onClick={() => decline.mutate()}>{ap.kind === "push" ? "Don't push" : "No pull request"}</button>
      </div>
      <ErrorNote error={approve.error ?? decline.error} />
    </div>
  );
}

/** Release: push approval, pushes and pull requests of a task. */
export function TaskReleasePanel({ taskId }: { taskId: string }) {
  const q = useQuery({
    queryKey: ["task", taskId, "release"],
    queryFn: () => unwrap(api.GET("/tasks/{taskId}/release", { params: { path: { taskId } } })),
    refetchInterval: (s) => (s.state.data?.pushes.some((p) => p.status === "granted") || s.state.data?.pullRequests.some((p) => p.status === "granted") ? 3000 : 15000),
  });
  const rel = q.data;
  if (!rel) return <ErrorNote error={q.error} />;
  if (!rel.request && rel.pushes.length === 0 && rel.approvals.length === 0) return null;
  return (
    <Panel title="Release" flush={false}>
      <ErrorNote error={q.error} />
      {rel.approvals.map((ap) => <ApprovalCard key={ap.id} ap={ap} rel={rel} taskId={taskId} />)}
      {rel.pushes.map((p) => (
        <div key={p.id} className="release-row small">
          <Badge tone={p.status === "pushed" ? "ok" : p.status === "failed" ? "bad" : "info"} pulse={p.status === "granted"}>
            {p.status === "pushed" ? (p.existed ? "already pushed" : "pushed") : p.status === "granted" ? "pushing" : "push failed"}
          </Badge>
          <span className="mono">{p.branch}</span> at <CopyText value={p.sha} display={short(p.sha)} label="commit" /> to <span className="mono">{web(p.remote)}</span>
          {p.status === "pushed" && (
            <a href={`${web(p.remote)}/tree/${encodeURIComponent(p.branch)}`} target="_blank" rel="noreferrer noopener">
              branch <ExternalLink size={11} aria-hidden />
            </a>
          )}
          {p.pushedAt && <span className="faint">{relativeTime(p.pushedAt)}</span>}
          {p.attempts > 1 && <span className="faint">attempt {p.attempts}</span>}
          {p.error && <div className="bad-text">{p.error} — fix it (e.g. the token's access), then Retry on the workflow.</div>}
        </div>
      ))}
      {rel.pullRequests.map((pr) => (
        <div key={pr.id} className="release-row small">
          <Badge tone={pr.state === "merged" ? "ok" : pr.status === "failed" ? "bad" : pr.state === "closed" ? "neutral" : "info"}>
            {pr.status === "failed" ? "pull request failed" : pr.state ?? pr.status}
          </Badge>
          {pr.draft && <Badge dot={false}>draft</Badge>}
          {pr.url ? <a href={pr.url} target="_blank" rel="noreferrer noopener">#{pr.number} {pr.title} <ExternalLink size={11} aria-hidden /></a> : <span>{pr.title}</span>}
          {pr.error && <div className="bad-text">{pr.error}</div>}
        </div>
      ))}
    </Panel>
  );
}
