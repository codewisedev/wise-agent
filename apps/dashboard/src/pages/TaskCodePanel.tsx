import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, FolderGit2, GitBranch, Play, ShieldAlert, Trash2, XCircle } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, CopyText, ErrorNote, Panel, relativeTime, shortSha, type Tone } from "../components/ui";
import type { Artifact, Plan } from "./workflowUi";

type Task = Schemas["Task"];
type Obj = Record<string, unknown>;
const wsTone: Record<Schemas["WorkspaceStatus"], Tone> = { preparing: "info", ready: "ok", failed: "bad", stopped: "neutral", removed: "neutral" };

/** Whether the task works with code (its plan has a workspace step or it has a workspace). */
export function hasCode(plan: Plan, artifacts: Artifact[]) {
  return plan.steps.some((s) => s.type === "workspace" && s.enabled) || artifacts.some((a) => a.kind === "changeset");
}

function latest(artifacts: Artifact[], kind: string): Obj | undefined {
  return artifacts.find((a) => a.kind === kind)?.content as Obj | undefined;
}

type FileChange = { path: string; oldPath?: string; status: string; additions: number; deletions: number; binary?: boolean; secret?: boolean };

/** A unified diff, file by file, with added and removed lines marked. */
export function DiffView({ patch }: { patch: string }) {
  const files = patch ? patch.split(/^(?=diff --git )/m).filter((f) => f.trim()) : [];
  if (!files.length) return <p className="small faint">No changes.</p>;
  return (
    <div className="diff">
      {files.map((f, i) => {
        const name = /^diff --git a\/(.+?) b\/(.+)$/m.exec(f)?.[2] ?? `file ${i + 1}`;
        return (
          <details key={i} open={files.length <= 12} className="diff-file">
            <summary className="mono small">{name}</summary>
            <pre className="diff-body" aria-label={`Diff of ${name}`}>
              {(() => {
                let inHunk = false; // ---/+++ are file headers only before the first hunk
                return f.split("\n").map((l, j) => {
                  let cls = "";
                  if (l.startsWith("@@")) {
                    inHunk = true;
                    cls = "diff-hunk";
                  } else if (!inHunk) cls = "diff-meta";
                  else if (l.startsWith("+")) cls = "diff-add";
                  else if (l.startsWith("-")) cls = "diff-del";
                  return <span key={j} className={cls}>{l}{"\n"}</span>;
                });
              })()}
            </pre>
          </details>
        );
      })}
    </div>
  );
}

function CommitMessage({ taskId, message }: { taskId: string; message: string }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(message);
  const save = useMutation({
    mutationFn: () => unwrap(api.POST("/tasks/{taskId}/commit-message", { params: { path: { taskId } }, body: { message: text } })),
    onSuccess: () => {
      setEditing(false);
      void qc.invalidateQueries({ queryKey: ["task", taskId] });
      void qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
  if (!editing) {
    return (
      <div className="commit-msg-row">
        <pre className="commit-msg small">{message}</pre>
        <button type="button" className="btn btn-sm btn-ghost" onClick={() => { setText(message); setEditing(true); }}
          title="Change the message before your final approval (same changes; the approval is asked again)">Edit message</button>
      </div>
    );
  }
  return (
    <div className="settings-form">
      <textarea aria-label="Commit message" className="mono" rows={6} value={text} maxLength={10000} onChange={(e) => setText(e.target.value)} />
      <div className="row-actions">
        <button type="button" className="btn btn-sm btn-primary" disabled={save.isPending || !text.trim()} onClick={() => save.mutate()}>Save message</button>
        <button type="button" className="btn btn-sm" onClick={() => setEditing(false)}>Cancel</button>
      </div>
      <ErrorNote error={save.error} />
    </div>
  );
}

function Changeset({ cs, taskId }: { cs: Obj; taskId: string }) {
  const files = (cs.files as FileChange[] | undefined) ?? [];
  if (!cs.headSha) return <p className="small faint panel-body">The workspace has no changes.</p>;
  return (
    <div className="panel-body">
      <div className="meta small">
        <span>commit <CopyText value={String(cs.headSha)} display={shortSha(String(cs.headSha))} label="commit" /></span>
        <span>on <span className="mono">{String(cs.branch)}</span> from <span className="mono">{shortSha(String(cs.baseSha))}</span></span>
        <span className="diff-add-text">+{String(cs.additions)}</span> <span className="diff-del-text">−{String(cs.deletions)}</span>
      </div>
      {cs.secretFound === true && (
        <p className="error" role="alert"><ShieldAlert size={14} aria-hidden /> The change contains a credential-like file or token (redacted below). Request changes unless it is intended.</p>
      )}
      {typeof cs.message === "string" && <CommitMessage taskId={taskId} message={cs.message} />}
      <ul className="list file-list">
        {files.map((f) => (
          <li key={f.path} className="small">
            <span className={`chip chip-${f.status}`}>{f.status}</span> <span className="mono">{f.oldPath ? `${f.oldPath} → ` : ""}{f.path}</span>{" "}
            {f.binary ? <span className="faint">binary</span> : <><span className="diff-add-text">+{f.additions}</span> <span className="diff-del-text">−{f.deletions}</span></>}
            {f.secret && <Badge tone="bad" dot={false}>credential-like</Badge>}
          </li>
        ))}
      </ul>
      <DiffView patch={String(cs.patch ?? "")} />
      {cs.truncated === true && <p className="small faint">The diff is long and was cut; the commit has every change.</p>}
    </div>
  );
}

type Check = { name: string; command: string; status: string; exitCode?: number; durationMs: number; output: string };

function Quality({ q }: { q: Obj }) {
  const checks = (q.checks as Check[] | undefined) ?? [];
  return (
    <div className="panel-body">
      {typeof q.note === "string" && <p className="small muted">{q.note}</p>}
      <ul className="list">
        {checks.map((c) => (
          <li key={c.name}>
            <details>
              <summary>
                {c.status === "passed" ? <CheckCircle2 size={14} style={{ color: "var(--ok)" }} aria-hidden /> : <XCircle size={14} style={{ color: "var(--danger)" }} aria-hidden />}{" "}
                <strong>{c.name}</strong> <span className="mono small">{c.command}</span>{" "}
                <Badge tone={c.status === "passed" ? "ok" : "bad"} dot={false}>{c.status.replace("_", " ")}{c.exitCode != null ? ` · exit ${c.exitCode}` : ""}</Badge>{" "}
                <span className="small faint">{(c.durationMs / 1000).toFixed(1)} s</span>
              </summary>
              <pre className="log">{c.output || "(no output)"}</pre>
            </details>
          </li>
        ))}
      </ul>
    </div>
  );
}

type Finding = { severity: string; file: string; line?: number; title: string; detail: string; suggestion?: string };
const sevTone: Record<string, Tone> = { critical: "bad", major: "warn", minor: "neutral", info: "info" };

function Review({ title, r }: { title: string; r: Obj }) {
  const findings = ((r.findings as Finding[] | undefined) ?? []).slice().sort((a, b) =>
    ["critical", "major", "minor", "info"].indexOf(a.severity) - ["critical", "major", "minor", "info"].indexOf(b.severity));
  return (
    <section className="panel-body" aria-label={title}>
      <h3>{title} <Badge tone={r.verdict === "pass" ? "ok" : "warn"} dot={false}>{String(r.verdict ?? "").replace("_", " ")}</Badge></h3>
      {typeof r.summary === "string" && <p className="small muted">{r.summary}</p>}
      {findings.length === 0 ? <p className="small faint">No findings.</p> : (
        <ul className="list">
          {findings.map((f, i) => (
            <li key={i}>
              <Badge tone={sevTone[f.severity] ?? "neutral"} dot={false}>{f.severity}</Badge> <strong>{f.title}</strong>{" "}
              <span className="mono small">{f.file}{f.line ? `:${f.line}` : ""}</span>
              <p className="small">{f.detail}</p>
              {f.suggestion && <p className="small muted">Fix: {f.suggestion}</p>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function GateRounds({ taskId }: { taskId: string }) {
  const rounds = useQuery({
    queryKey: ["task", taskId, "artifacts", "quality_gate"],
    queryFn: () => unwrap(api.GET("/tasks/{taskId}/artifacts", { params: { path: { taskId }, query: { kind: "quality_gate" } } })),
  });
  const items = rounds.data?.items ?? [];
  if (!items.length) return null;
  return (
    <section className="panel-body" aria-label="Quality gate rounds">
      <h3>Quality gate</h3>
      <ol className="list">
        {items.slice().reverse().map((a) => {
          const g = a.content as { round?: number; passed?: boolean; summary?: string };
          return (
            <li key={a.id} className="small">
              <Badge tone={g.passed ? "ok" : "bad"} dot={false}>{g.passed ? "passed" : "failed"}</Badge>{" "}
              {g.round ? `fix round ${g.round}` : "first check"} — {g.summary} <span className="faint">{relativeTime(a.createdAt)}</span>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

/** The task's code: its workspace, the change, checks, reviews and delivered branch. */
export function TaskCodePanel({ task, artifacts }: { task: Task; artifacts: Artifact[] }) {
  const qc = useQueryClient();
  const taskId = task.id;
  const key = ["task", taskId, "workspace"];
  const ws = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET("/tasks/{taskId}/workspace", { params: { path: { taskId } } })),
    refetchInterval: (q) => (q.state.data?.workspace?.status === "preparing" ? 3000 : false),
  });
  const repos = useQuery({
    queryKey: ["project", task.projectId, "repositories"],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/repositories", { params: { path: { projectId: task.projectId } } })),
  });
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: key });
    void qc.invalidateQueries({ queryKey: ["task", taskId] });
  };
  const prepare = useMutation({ mutationFn: () => unwrap(api.POST("/tasks/{taskId}/workspace", { params: { path: { taskId } } })), onSuccess: refresh });
  const remove = useMutation({ mutationFn: () => unwrap(api.DELETE("/tasks/{taskId}/workspace", { params: { path: { taskId } } })), onSuccess: refresh });
  const setRepo = useMutation({
    mutationFn: (repository: string) => unwrap(api.PATCH("/tasks/{taskId}", { params: { path: { taskId } }, body: { repository } })),
    onSuccess: refresh,
  });
  const w = ws.data?.workspace;
  const local = (repos.data?.items ?? []).filter((r) => r.sourceType === "local");
  const cs = latest(artifacts, "changeset");
  const q = latest(artifacts, "quality_report");
  const cr = latest(artifacts, "code_review");
  const sr = latest(artifacts, "security_review");
  const delivery = latest(artifacts, "delivery");
  const branch = w?.deliveredBranch ?? (typeof delivery?.branch === "string" ? delivery.branch : undefined);

  return (
    <Panel
      title="Code"
      actions={
        <div className="panel-actions">
          {local.length > 1 && (
            <select aria-label="Repository the task changes" value={task.targetRepositoryId ?? "auto"} onChange={(e) => setRepo.mutate(e.target.value)}>
              <option value="auto" disabled>choose a repository…</option>
              {local.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
            </select>
          )}
          {(!w || w.status === "failed" || w.status === "removed" || w.status === "stopped") && (
            <button type="button" className="btn btn-sm" onClick={() => prepare.mutate()} disabled={prepare.isPending || !ws.data?.executorAvailable || !ws.data?.repository?.configured}
              title="Clone the repository into a private workspace and start its sandbox (workflows do this in their workspace step)">
              <Play size={13} aria-hidden /> {w?.status === "stopped" ? "Start sandbox" : "Prepare workspace"}
            </button>
          )}
          {w && w.status !== "removed" && w.status !== "preparing" && (
            <button type="button" className="btn btn-sm btn-ghost" onClick={() => confirm("Remove this task's workspace and sandbox? A delivered branch stays in your repository.") && remove.mutate()} disabled={remove.isPending}>
              <Trash2 size={13} aria-hidden /> Remove workspace
            </button>
          )}
        </div>
      }
    >
      <ErrorNote error={ws.error ?? prepare.error ?? remove.error ?? setRepo.error} />
      <div className="panel-body">
        {ws.data?.problem && (
          <p className="small" role="status">
            {ws.data.problem}.{" "}
            <Link to={`/projects/${task.projectId}`}>Open the project</Link> to {ws.data.repository ? "set its Execution settings" : "register or choose a repository"}.
          </p>
        )}
        {ws.data && !ws.data.executorAvailable && <p className="small faint">The executor is not configured, so tasks cannot work with code.</p>}
        {branch && (
          <div className="delivered" role="status">
            <GitBranch size={15} aria-hidden /> Branch <strong className="mono">{branch}</strong> is in your repository.{" "}
            <CopyText value={`git switch ${branch}`} label="git command" />
          </div>
        )}
        {w ? (
          <div className="meta small">
            <FolderGit2 size={14} aria-hidden />
            <span>{w.repositoryName ?? ws.data?.repository?.name}</span>
            <Badge tone={wsTone[w.status]} pulse={w.status === "preparing"}>{w.status}</Badge>
            <span className="mono">{w.branch}</span>
            {w.baseSha && <span>from {w.baseBranch} <span className="mono">{shortSha(w.baseSha)}</span></span>}
            <span className="mono faint">{w.image}</span>
            {w.toolVersions.length > 0 && <span className="faint" title={w.toolVersions.join("\n")}>{w.toolVersions.length} tools checked</span>}
          </div>
        ) : (
          ws.data?.repository && <p className="small faint">Works in <strong>{ws.data.repository.name}</strong>. No workspace yet.</p>
        )}
        {w?.error && <p className="error" role="alert"><XCircle size={14} aria-hidden /> {w.error}</p>}
        {w && w.scrubbed.length > 0 && (
          <p className="small muted">Kept out of the workspace (credential-like): <span className="mono">{w.scrubbed.join(", ")}</span></p>
        )}
        {w && w.log && (
          <details open={w.status === "preparing" || w.status === "failed"}>
            <summary className="small">Preparation log</summary>
            <pre className="log" aria-live={w.status === "preparing" ? "polite" : undefined}>{w.log}</pre>
          </details>
        )}
      </div>
      <GateRounds taskId={taskId} />
      {q && <section aria-label="Quality checks"><h3 className="panel-body" style={{ paddingBottom: 0 }}>Checks</h3><Quality q={q} /></section>}
      {cr && <Review title="Code review" r={cr} />}
      {sr && <Review title="Security review" r={sr} />}
      {cs && <section aria-label="Changes"><h3 className="panel-body" style={{ paddingBottom: 0 }}>Changes</h3><Changeset cs={cs} taskId={taskId} /></section>}
    </Panel>
  );
}
