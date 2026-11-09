import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDownRight, ArrowUpRight, Archive, Sparkles, UserRound } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, Empty, ErrorNote, Field, Panel } from "../components/ui";

type Memory = Schemas["Memory"];

/** The engineering profile's areas (memory categories grouped as in the plan). */
const AREAS: { title: string; categories: string[]; hint: string }[] = [
  { title: "Coding standards", categories: ["coding"], hint: "Naming, organization, types, errors, logging, validation, documentation" },
  { title: "Architecture", categories: ["architecture", "api", "database"], hint: "Modules, dependencies, API design, data and transactions" },
  { title: "Testing", categories: ["testing"], hint: "Unit, integration and end-to-end tests, coverage, mocking" },
  { title: "Technologies & tooling", categories: ["tooling"], hint: "Languages, frameworks, build and lint tools" },
  { title: "Security", categories: ["security"], hint: "Secrets, authentication, input handling" },
  { title: "Other", categories: ["product", "other"], hint: "" },
];
const SECTIONS: { kind: Memory["kind"]; title: string; hint: string }[] = [
  { kind: "rule", title: "Rules", hint: "Always given to agents; they must follow them." },
  { kind: "preference", title: "Preferences", hint: "Guide recommendations when relevant; never mandatory." },
  { kind: "convention", title: "Conventions", hint: "How you name and organize things." },
];

function ProfileItem({ m }: { m: Memory }) {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: ["memories"] });
  const setKind = useMutation({
    mutationFn: (kind: Memory["kind"]) => unwrap(api.PUT("/memories/{memoryId}", {
      params: { path: { memoryId: m.id } },
      body: { scope: m.scope, projectId: m.projectId ?? null, kind, category: m.category, title: m.title, body: m.body },
    })),
    onSuccess: refresh,
  });
  const archive = useMutation({
    mutationFn: () => unwrap(api.POST("/memories/{memoryId}/archive", { params: { path: { memoryId: m.id } } })),
    onSuccess: refresh,
  });
  return (
    <li className="profile-item">
      <div className="profile-text">
        <strong>{m.title}</strong>
        <p className="small muted">{m.body}</p>
        {m.source === "profile_inference" && <span className="small faint">inferred from your repositories</span>}
      </div>
      <div className="profile-actions">
        {m.kind === "preference" && (
          <button type="button" className="btn btn-ghost btn-sm" disabled={setKind.isPending}
            onClick={() => window.confirm(`Make “${m.title}” a rule every agent must follow?`) && setKind.mutate("rule")}>
            <ArrowUpRight size={13} aria-hidden /> Make it a rule
          </button>
        )}
        {m.kind === "rule" && (
          <button type="button" className="btn btn-ghost btn-sm" disabled={setKind.isPending} onClick={() => setKind.mutate("preference")}>
            <ArrowDownRight size={13} aria-hidden /> Make it a preference
          </button>
        )}
        <button type="button" className="btn btn-ghost btn-icon" aria-label={`Archive ${m.title}`} onClick={() => archive.mutate()}>
          <Archive size={13} aria-hidden />
        </button>
      </div>
      <ErrorNote error={setKind.error ?? archive.error} />
    </li>
  );
}

function InferPanel() {
  const qc = useQueryClient();
  const projects = useQuery({ queryKey: ["projects"], queryFn: () => unwrap(api.GET("/projects")) });
  const [projectId, setProjectId] = useState("");
  const pid = projectId || projects.data?.items[0]?.id || "";
  const infer = useMutation({
    mutationFn: () => unwrap(api.POST("/projects/{projectId}/infer-profile", { params: { path: { projectId: pid } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["memories"] }),
  });
  return (
    <Panel title="Infer from repositories" flush={false}>
      <p className="small muted" style={{ marginTop: 0 }}>
        The profile agent reads a project's indexed repositories (dependencies, lint, format and test configuration, test layout,
        naming) and proposes preferences and conventions — never rules. Proposals wait in the <Link to="/memory?tab=inbox">memory inbox</Link>.
      </p>
      <div className="form-grid">
        <Field label="Project">
          <select value={pid} onChange={(e) => setProjectId(e.target.value)}>
            {projects.data?.items.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        </Field>
        <button type="button" className="btn btn-primary" disabled={!pid || infer.isPending} onClick={() => infer.mutate()}>
          <Sparkles size={15} aria-hidden /> Propose preferences
        </button>
      </div>
      {infer.isSuccess && (
        <p className="small" role="status">
          {infer.data.status === "queued" || infer.data.status === "running"
            ? <>Started — <Link to={`/runs/${infer.data.id}`}>follow the run</Link>; proposals appear in the inbox when it finishes.</>
            : <>These commits were already analyzed (<Link to={`/runs/${infer.data.id}`}>see the run</Link>). Commit changes and re-index to analyze again.</>}
        </p>
      )}
      <ErrorNote error={infer.error} />
    </Panel>
  );
}

export function ProfilePage() {
  const profile = useQuery({
    queryKey: ["memories", "profile"],
    queryFn: () => unwrap(api.GET("/memories", { params: { query: { scope: "developer", status: "active", limit: 500 } } })),
  });
  const examples = useQuery({
    queryKey: ["memories", "examples"],
    queryFn: () => unwrap(api.GET("/memories", { params: { query: { kind: "example", status: "active", limit: 200 } } })),
  });
  const items = (profile.data?.items ?? []).filter((m) => m.kind !== "example");
  return (
    <>
      <header className="page-header">
        <div>
          <h1>Engineering profile</h1>
          <p className="desc">
            Your standards, as agents see them. <strong>Rules</strong> are explicit requirements every agent follows;{" "}
            <strong>preferences</strong> guide recommendations; <strong>approved examples</strong> are references to reuse.
            Inferred preferences never become rules on their own.
          </p>
        </div>
        <Link to="/memory" className="btn"><UserRound size={15} aria-hidden /> All memory</Link>
      </header>
      <InferPanel />
      <ErrorNote error={profile.error ?? examples.error} />
      {profile.data && items.length === 0 && (
        <Empty icon={<UserRound size={24} aria-hidden />}>
          Your profile is empty. Add rules and preferences on the <Link to="/memory">Memory</Link> page, approve proposals, or infer them from a project.
        </Empty>
      )}
      {AREAS.map((area) => {
        const inArea = items.filter((m) => area.categories.includes(m.category));
        if (inArea.length === 0) return null;
        return (
          <Panel key={area.title} title={area.title} count={inArea.length}>
            {area.hint && <p className="small faint panel-body" style={{ paddingBottom: 0 }}>{area.hint}</p>}
            <div className="profile-sections">
              {SECTIONS.map((sec) => {
                const list = inArea.filter((m) => m.kind === sec.kind);
                if (list.length === 0) return null;
                return (
                  <section key={sec.kind} aria-label={`${area.title} ${sec.title}`}>
                    <h3>{sec.title} <Badge dot={false}>{list.length}</Badge></h3>
                    <p className="small faint">{sec.hint}</p>
                    <ul className="list profile-list">{list.map((m) => <ProfileItem key={m.id} m={m} />)}</ul>
                  </section>
                );
              })}
            </div>
          </Panel>
        );
      })}
      <Panel title="Approved examples" count={examples.data?.items.length}>
        {examples.data?.items.length === 0 ? (
          <p className="small faint panel-body">
            None yet. Open a file from a project's <strong>Context</strong> page and use <strong>Save as approved example</strong>.
          </p>
        ) : (
          <ul className="list profile-list">{examples.data?.items.map((m) => (
            <li key={m.id} className="profile-item">
              <div className="profile-text">
                <strong>{m.title}</strong> <span className="small faint">{m.projectName}</span>
                <pre className="hit-snippet">{m.body}</pre>
              </div>
            </li>
          ))}</ul>
        )}
      </Panel>
    </>
  );
}
