import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { api, unwrap, type Schemas } from "../api/client";
import { ErrorNote, Panel } from "./ui";

type Policy = Schemas["ProjectCodingRuntimes"];
const RUNTIMES: { id: Policy["enabled"][number]; label: string }[] = [
  { id: "claude-code", label: "Claude Code" },
  { id: "codex", label: "Codex" },
];

/** Which external coding runtimes this project's tasks may use (a security policy). */
export function CodingRuntimesPanel({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const qc = useQueryClient();
  const key = ["project", projectId, "coding-runtimes"];
  const path = { params: { path: { projectId } } };
  const cur = useQuery({ queryKey: key, queryFn: () => unwrap(api.GET("/projects/{projectId}/coding-runtimes", path)) });
  const [v, setV] = useState<Policy | null>(null);
  useEffect(() => {
    if (cur.data) setV(cur.data);
  }, [cur.data]);
  const save = useMutation({
    mutationFn: (body: Policy) => unwrap(api.PUT("/projects/{projectId}/coding-runtimes", { ...path, body })),
    onSuccess: (p) => qc.setQueryData(key, p),
  });
  if (!v) return null;
  const dirty = JSON.stringify(v) !== JSON.stringify(cur.data);
  const toggle = (id: Policy["enabled"][number], on: boolean) =>
    setV({ ...v, enabled: on ? [...v.enabled.filter((x) => x !== id), id] : v.enabled.filter((x) => x !== id) });
  return (
    <Panel title="Coding runtimes" flush={false}>
      <p className="small muted" style={{ marginTop: 0 }}>
        Implementing steps of this project's tasks may run on these coding CLIs (configured in Settings → Coding runtimes).
        They work in the task's runner under the same sandbox rules, and Wise checks, reviews and gates their change.
      </p>
      <div className="form-grid">
        {RUNTIMES.map((r) => (
          <label key={r.id} className="checkbox">
            <input type="checkbox" disabled={readOnly} checked={v.enabled.includes(r.id)} onChange={(e) => toggle(r.id, e.target.checked)} /> {r.label}
          </label>
        ))}
      </div>
      <label className="checkbox">
        <input type="checkbox" disabled={readOnly} checked={v.allowSubscription} onChange={(e) => setV({ ...v, allowSubscription: e.target.checked })} />
        Allow subscription credentials (reduced isolation)
      </label>
      {v.allowSubscription && (
        <p className="small warn-text" role="note">
          <ShieldAlert size={13} aria-hidden /> With a subscription the vendor token is inside the runner: any command it runs, including
          this repository's scripts, can read it, and Wise cannot stop a run mid-way on cost. API-key accounts through the Runtime Proxy keep the key out.
        </p>
      )}
      {!readOnly && (
        <div className="actions">
          <button type="button" className="btn btn-primary" disabled={!dirty || save.isPending} onClick={() => save.mutate(v)}>Save</button>
        </div>
      )}
      <ErrorNote error={cur.error ?? save.error} />
    </Panel>
  );
}
