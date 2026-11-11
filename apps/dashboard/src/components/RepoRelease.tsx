import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api, unwrap } from "../api/client";
import { ErrorNote, Field } from "./ui";

/** Where a repository pushes: its https remote, the git account and the base branch. */
export function RepoReleaseEditor({ repositoryId, onDone }: { repositoryId: string; onDone?: () => void }) {
  const qc = useQueryClient();
  const cur = useQuery({
    queryKey: ["repository-release", repositoryId],
    queryFn: () => unwrap(api.GET("/repositories/{repositoryId}/release", { params: { path: { repositoryId } } })),
  });
  const accounts = useQuery({ queryKey: ["accounts"], queryFn: () => unwrap(api.GET("/accounts")) });
  const git = (accounts.data?.items ?? []).filter((a) => a.kind === "github" || a.kind === "gitlab");
  const [remote, setRemote] = useState("");
  const [accountId, setAccountId] = useState("");
  const [base, setBase] = useState("");
  const [note, setNote] = useState("");
  useEffect(() => {
    if (!cur.data) return;
    setRemote(cur.data.remote ?? "");
    setAccountId(cur.data.accountId ?? "");
    setBase(cur.data.baseBranch ?? "");
  }, [cur.data]);
  const detect = useMutation({
    mutationFn: () => unwrap(api.POST("/repositories/{repositoryId}/release/detect", { params: { path: { repositoryId } } })),
    onSuccess: (s) => {
      if (s.remote) setRemote(s.remote);
      if (s.baseBranch) setBase(s.baseBranch);
      if (s.accounts.length === 1 && s.accounts[0]) setAccountId(s.accounts[0]);
      setNote(s.problem ?? "");
    },
  });
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/repositories/{repositoryId}/release", {
      params: { path: { repositoryId } }, body: { remote, accountId: accountId || null, baseBranch: base },
    })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["repository-release", repositoryId] });
      onDone?.();
    },
  });
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/repositories/{repositoryId}/release/test", { params: { path: { repositoryId } } })),
  });
  return (
    <div className="settings-form">
      <p className="small faint">
        After you approve a push, Wise's git worker pushes the task's <span className="mono">wise/…</span> branch here with the
        account's token. It never pushes to the base branch, never force-pushes and never merges. Agents never see the token.
      </p>
      <div className="form-grid">
        <Field label="Remote (https)" grow>
          <input value={remote} placeholder="https://github.com/owner/repo.git" onChange={(e) => setRemote(e.target.value)} />
        </Field>
        <Field label="Git account" hint="A GitHub or GitLab token from Settings → Accounts, for the remote's host">
          <select value={accountId} onChange={(e) => setAccountId(e.target.value)}>
            <option value="">— none —</option>
            {git.map((a) => <option key={a.id} value={a.id}>{a.name} ({a.kind})</option>)}
          </select>
        </Field>
        <Field label="Base branch" hint="Pull requests target it">
          <input value={base} placeholder="main" onChange={(e) => setBase(e.target.value)} />
        </Field>
      </div>
      {note && <p className="small warn-text">{note}</p>}
      {test.data && <p className="small">The account can read the repository (default branch {test.data.defaultBranch}). The first push proves write access.</p>}
      <ErrorNote error={cur.error ?? detect.error ?? save.error ?? test.error} />
      <div className="row-actions">
        <button type="button" className="btn btn-sm" onClick={() => detect.mutate()} disabled={detect.isPending}>Detect</button>
        <button type="button" className="btn btn-sm" onClick={() => test.mutate()} disabled={test.isPending || !cur.data?.accountId}>Test</button>
        <button type="button" className="btn btn-sm btn-primary" onClick={() => save.mutate()} disabled={save.isPending}>Save</button>
      </div>
    </div>
  );
}

/** A project's pull request policy (after an approved push). */
export function ReleasePolicyPanel({ projectId, readOnly }: { projectId: string; readOnly?: boolean }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["release-policy", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/release-policy", { params: { path: { projectId } } })),
  });
  const save = useMutation({
    mutationFn: (body: { pullRequests: "off" | "ask" | "auto"; draft: boolean }) =>
      unwrap(api.PUT("/projects/{projectId}/release-policy", { params: { path: { projectId } }, body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["release-policy", projectId] }),
  });
  const p = q.data;
  return (
    <div className="settings-form">
      <h3>Pull requests</h3>
      {p && (
        <div className="form-grid">
          <Field label="After an approved push">
            <select value={p.pullRequests} disabled={readOnly || save.isPending}
              onChange={(e) => save.mutate({ ...p, pullRequests: e.target.value as "off" | "ask" | "auto" })}>
              <option value="off">push only</option>
              <option value="ask">ask me to open a pull request</option>
              <option value="auto">open a pull request</option>
            </select>
          </Field>
          <label className="checkbox">
            <input type="checkbox" checked={p.draft} disabled={readOnly || save.isPending || p.pullRequests === "off"}
              onChange={(e) => save.mutate({ ...p, draft: e.target.checked })} /> as a draft
          </label>
        </div>
      )}
      <p className="small faint">Merging always stays with you.</p>
      <ErrorNote error={q.error ?? save.error} />
    </div>
  );
}
