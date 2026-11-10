import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Sparkles, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { api, unwrap, type Schemas } from "../api/client";
import { ErrorNote, Field } from "./ui";

type Exec = Schemas["RepositoryExecution"];
const CHECKS: { key: keyof Exec["commands"]; label: string; hint: string }[] = [
  { key: "format", label: "Format check", hint: "Must not rewrite files, e.g. npm run format:check" },
  { key: "lint", label: "Lint", hint: "e.g. npm run lint" },
  { key: "typecheck", label: "Type check", hint: "e.g. npm run typecheck" },
  { key: "test", label: "Tests", hint: "e.g. npm test" },
];
const lines = (v: string) => v.split("\n").map((l) => l.trim()).filter(Boolean);

/** Edits how Wise builds and checks a repository in task sandboxes. */
export function RepoExecutionEditor({ repositoryId, onDone }: { repositoryId: string; onDone?: () => void }) {
  const qc = useQueryClient();
  const path = { params: { path: { repositoryId } } };
  const key = ["repository", repositoryId, "execution"];
  const current = useQuery({ queryKey: key, queryFn: () => unwrap(api.GET("/repositories/{repositoryId}/execution", path)) });
  const [v, setV] = useState<Exec | null>(null);
  const [setup, setSetup] = useState("");
  const [tools, setTools] = useState("");
  const [env, setEnv] = useState<[string, string][]>([]);
  const [notes, setNotes] = useState<string[]>([]);
  const load = (e: Exec) => {
    setV(e);
    setSetup(e.setup.join("\n"));
    setTools(e.tools.join(" "));
    setEnv(Object.entries(e.env));
  };
  useEffect(() => {
    if (current.data && !v) load(current.data);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current.data]);
  const detect = useMutation({
    mutationFn: () => unwrap(api.POST("/repositories/{repositoryId}/execution/detect", path)),
    onSuccess: (d) => {
      setNotes([...(d.detected.length ? [`Detected: ${d.detected.join(", ")}`] : []), ...d.notes]);
      // Detection suggests no variables: keep the rows being edited.
      if (d.settings.image) load({ ...d.settings, env: Object.fromEntries(env.filter(([k]) => k.trim())) });
    },
  });
  const save = useMutation({
    mutationFn: (body: Exec) => unwrap(api.PUT("/repositories/{repositoryId}/execution", { ...path, body })),
    onSuccess: (saved) => {
      qc.setQueryData(key, saved);
      onDone?.();
    },
  });
  if (current.error) return <ErrorNote error={current.error} />;
  if (!v) return <p className="loading">loading…</p>;
  const body = (): Exec => ({
    ...v, setup: lines(setup), tools: tools.split(/[\s,]+/).filter(Boolean),
    env: Object.fromEntries(env.filter(([k]) => k.trim()).map(([k, val]) => [k.trim(), val])),
  });
  return (
    <form className="settings-form" onSubmit={(e) => { e.preventDefault(); save.mutate(body()); }} aria-label="Execution settings">
      <p className="small muted" style={{ marginTop: 0 }}>
        How Wise builds and checks this repository in a task's sandbox. The quality gate runs the checks; coding agents run them too.
      </p>
      <div className="actions" style={{ marginTop: 0 }}>
        <button type="button" className="btn btn-sm" onClick={() => detect.mutate()} disabled={detect.isPending}>
          <Sparkles size={13} aria-hidden /> Detect from the repository
        </button>
      </div>
      {notes.length > 0 && <ul className="small muted bullets" role="status">{notes.map((n) => <li key={n}>{n}</li>)}</ul>}
      <div className="form-grid">
        <Field label="Toolchain image" hint="Must include git (make toolchains builds Node, Go and Python ones)" grow>
          <input className="mono" value={v.image} placeholder="wise-toolchain-node:22" onChange={(e) => setV({ ...v, image: e.target.value })} />
        </Field>
        <Field label="Base branch" hint="Empty = the repository's current branch">
          <input className="mono" value={v.baseBranch} onChange={(e) => setV({ ...v, baseBranch: e.target.value })} />
        </Field>
        <Field label="Required tools" hint="Checked before any agent runs, e.g. node npm">
          <input className="mono" value={tools} onChange={(e) => setTools(e.target.value)} />
        </Field>
      </div>
      <Field label="Setup commands" hint="Run once when the workspace is prepared, one per line (e.g. npm ci)" grow>
        <textarea className="mono" rows={3} value={setup} spellCheck={false} onChange={(e) => setSetup(e.target.value)} />
      </Field>
      <div className="form-grid">
        {CHECKS.map((c) => (
          <Field key={c.key} label={c.label} hint={c.hint}>
            <input className="mono" value={v.commands[c.key]} onChange={(e) => setV({ ...v, commands: { ...v.commands, [c.key]: e.target.value } })} />
          </Field>
        ))}
      </div>
      <fieldset className="env-list">
        <legend className="small">Environment variables <span className="faint">— visible to agents; never put secrets here</span></legend>
        {env.map(([k, val], i) => (
          <div key={i} className="form-grid">
            <input className="mono" aria-label="Variable name" value={k} onChange={(e) => setEnv(env.map((p, j) => (j === i ? [e.target.value, p[1]] : p)))} />
            <input className="mono" aria-label={`Value of ${k || "variable"}`} value={val} onChange={(e) => setEnv(env.map((p, j) => (j === i ? [p[0], e.target.value] : p)))} />
            <button type="button" className="btn btn-ghost btn-icon" aria-label={`Remove ${k || "variable"}`} onClick={() => setEnv(env.filter((_, j) => j !== i))}>
              <Trash2 size={13} aria-hidden />
            </button>
          </div>
        ))}
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => setEnv([...env, ["", ""]])}><Plus size={13} aria-hidden /> Add variable</button>
      </fieldset>
      <div className="actions">
        <button type="submit" className="btn btn-primary" disabled={save.isPending}>Save</button>
        {onDone && <button type="button" className="btn btn-ghost" onClick={onDone}>Close</button>}
      </div>
      <ErrorNote error={save.error ?? detect.error} />
    </form>
  );
}
