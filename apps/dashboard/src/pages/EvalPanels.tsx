import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FlaskConical, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import type { components } from "../api/schema.gen";
import { AccountSelect } from "../components/AccountSelect";
import { Badge, Empty, ErrorNote, Field, Panel } from "../components/ui";
import { formatCost } from "./RunsTable";

type Proposal = components["schemas"]["ImprovementProposal"];
type EvalRun = components["schemas"]["EvalRun"];
type EvalResult = components["schemas"]["EvalResult"];
type EvalCase = components["schemas"]["EvalCase"];
type Side = { valid?: boolean; status?: string; invalidAttempts?: number; emptyRequired?: string[]; secret?: boolean };

const KIND: Record<string, string> = { golden: "accepted before", must_fix: "must fix", custom: "yours" };

function SideBadges({ side }: { side?: Side }) {
  if (!side) return null;
  return (
    <span className="side-badges">
      {!side.valid && <Badge tone="bad" dot={false}>{side.status === "succeeded" ? "invalid" : side.status}</Badge>}
      {!!side.invalidAttempts && <Badge tone="warn" dot={false}>{side.invalidAttempts} invalid tries</Badge>}
      {!!side.emptyRequired?.length && <Badge tone="warn" dot={false}>empty: {side.emptyRequired.join(", ")}</Badge>}
      {side.secret && <Badge tone="bad" dot={false}>secret</Badge>}
    </span>
  );
}

function ResultRow({ r }: { r: EvalResult }) {
  const checks = (r.checks ?? {}) as { baseline?: Side; candidate?: Side };
  const score = (n?: number | null, id?: string | null) =>
    n == null ? (id ? <Link to={`/runs/${id}`} className="small faint">run</Link> : "–") : id ? <Link to={`/runs/${id}`}>{n}</Link> : n;
  return (
    <tr>
      <td><Badge tone={r.caseKind === "must_fix" ? "warn" : "neutral"} dot={false}>{KIND[r.caseKind] ?? r.caseKind}</Badge></td>
      <td className="small expectation">{r.expectation}</td>
      <td className="num">{score(r.baselineScore, r.baselineRunId)}<SideBadges side={checks.baseline} /></td>
      <td className="num">{score(r.candidateScore, r.candidateRunId)}<SideBadges side={checks.candidate} /></td>
      <td className="small">
        {r.status === "running" || r.status === "judging" ? <Badge tone="info" pulse>{r.status}</Badge> : r.reasoning}
        {r.error && <div className="faint">{r.error}</div>}
      </td>
    </tr>
  );
}

function EvalRunView({ run }: { run: EvalRun }) {
  const qc = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => unwrap(api.POST("/evals/{evalRunId}/cancel", { params: { path: { evalRunId: run.id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["improvements"] }),
  });
  const s = run.summary;
  const tone = run.status === "succeeded" ? "ok" : run.status === "running" ? "info" : "warn";
  return (
    <div className="eval-run">
      <div className="eval-head small">
        <Badge tone={tone} pulse={run.status === "running"}>evaluation {run.status}</Badge>
        <span className="mono">{run.agentName}</span>
        {s && (
          <span>
            {s.judged} of {s.cases} cases judged · proposal better in {s.candidateBetter}, same in {s.same}, worse in {s.candidateWorse}
            {s.judged ? ` · mean ${s.baselineMean} → ${s.candidateMean}` : ""}
            {s.candidateInvalid ? ` · ${s.candidateInvalid} invalid with the proposal` : ""}
            {s.baselineInvalid ? ` · ${s.baselineInvalid} invalid now` : ""} · {formatCost(s.costUsd ?? 0)}
          </span>
        )}
        {run.error && <span className="faint">{run.error}</span>}
        {run.status === "running" && <button type="button" className="btn btn-sm" onClick={() => cancel.mutate()} disabled={cancel.isPending}>Stop</button>}
      </div>
      {s && (s.cases ?? 0) < 10 && run.status !== "running" && (
        <p className="small faint">With only {s.cases ?? 0} cases this shows tendencies, not proof: read the cases.</p>
      )}
      <div className="table-wrap">
        <table>
          <thead><tr><th>Case</th><th>Expectation</th><th className="num">Now</th><th className="num">Proposal</th><th>Judge</th></tr></thead>
          <tbody>{run.results.map((r) => <ResultRow key={r.id} r={r} />)}</tbody>
        </table>
      </div>
      <ErrorNote error={cancel.error} />
    </div>
  );
}

/** Evaluate a proposal against the current version, and its evaluations so far. */
export function ProposalEvaluation({ p }: { p: Proposal }) {
  const qc = useQueryClient();
  const evaluableKind = p.kind === "prompt" || p.kind === "routing";
  const [agent, setAgent] = useState("");
  const plan = useQuery({
    queryKey: ["improvements", "eval-plan", p.id, agent, p.status],
    queryFn: () => unwrap(api.GET("/improvements/proposals/{proposalId}/eval-plan", { params: { path: { proposalId: p.id }, query: agent ? { agent } : {} } })),
    enabled: evaluableKind,
  });
  const evals = useQuery({
    queryKey: ["improvements", "evals", p.id],
    queryFn: () => unwrap(api.GET("/improvements/proposals/{proposalId}/evals", { params: { path: { proposalId: p.id } } })),
    enabled: evaluableKind,
    refetchInterval: (q) => (q.state.data?.items.some((e) => e.status === "running") ? 3000 : false),
  });
  const d = plan.data;
  const [model, setModel] = useState({ account: "", model: "" });
  const [judge, setJudge] = useState({ account: "", model: "" });
  const [maxCases, setMaxCases] = useState(5);
  const [cap, setCap] = useState(2);
  useEffect(() => {
    if (!d) return;
    setMaxCases(d.defaults.max_cases);
    setCap(d.defaults.cost_cap_usd);
    setJudge({ account: d.defaults.judge.account ?? "", model: d.defaults.judge.model ?? "" });
  }, [d?.defaults.max_cases, d?.defaults.cost_cap_usd, d?.defaults.judge.account, d?.defaults.judge.model]); // eslint-disable-line react-hooks/exhaustive-deps
  const start = useMutation({
    mutationFn: () => unwrap(api.POST("/improvements/proposals/{proposalId}/evaluate", {
      params: { path: { proposalId: p.id } },
      body: { agent: d?.agent, model: d?.choosesModel ? model : undefined, judge, maxCases, costCapUsd: cap },
    })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["improvements"] }),
  });
  if (!evaluableKind) return <p className="small faint">Memory and template proposals are not evaluated: judge them by their evidence and diff.</p>;
  const running = evals.data?.items.some((e) => e.status === "running");
  return (
    <div className="proposal-eval">
      <ErrorNote error={plan.error ?? evals.error} />
      {d && !d.evaluable && !running && <p className="small faint">Not evaluated: {d.reason}</p>}
      {d?.evaluable && !running && (
        <details>
          <summary><FlaskConical size={14} aria-hidden /> Evaluate on {d.cases} case{d.cases === 1 ? "" : "s"} of {d.agent}</summary>
          <div className="settings-form">
            <p className="small faint">
              Each case runs twice — now and with the proposal — then the judge scores both without knowing which is which.
              Replays share the run queue with real work (they can delay it) but don't count against budgets or metrics.
              Cloud models are used only by agents the model policy allows (the judge too).
            </p>
            <div className="form-grid">
              {d.agents && d.agents.length > 1 && (
                <Field label="Agent">
                  <select value={d.agent} onChange={(e) => setAgent(e.target.value)}>{d.agents.map((a) => <option key={a}>{a}</option>)}</select>
                </Field>
              )}
              {d.choosesModel && (
                <Field label="Run both on" hint="Empty = the agent's own routing">
                  <AccountSelect value={model.account} onChange={(a) => setModel({ account: a, model: a ? model.model : "" })} />
                </Field>
              )}
              {d.choosesModel && model.account && (
                <Field label="Model"><input value={model.model} placeholder="account default" onChange={(e) => setModel({ ...model, model: e.target.value })} /></Field>
              )}
              <Field label="Judge on" hint="Empty = the judge's own routing">
                <AccountSelect value={judge.account} onChange={(a) => setJudge({ account: a, model: a ? judge.model : "" })} />
              </Field>
              {judge.account && <Field label="Judge model"><input value={judge.model} placeholder="account default" onChange={(e) => setJudge({ ...judge, model: e.target.value })} /></Field>}
              <Field label="Cases (at most)"><input type="number" min={1} max={20} value={maxCases} onChange={(e) => setMaxCases(Number(e.target.value))} /></Field>
              <Field label="Stop at (USD)"><input type="number" min={0.01} max={100} step={0.5} value={cap} onChange={(e) => setCap(Number(e.target.value))} /></Field>
            </div>
            <p className="small">Estimated cost: about {formatCost(d.estimatedCostUsd * Math.min(maxCases, d.cases) / Math.max(1, Math.min(d.defaults.max_cases, d.cases)))} (from this agent's recent runs; local models cost nothing).</p>
            <div><button type="button" className="btn btn-primary" onClick={() => start.mutate()} disabled={start.isPending}>Start evaluation</button></div>
            <ErrorNote error={start.error} />
          </div>
        </details>
      )}
      {evals.data?.items.map((r) => <EvalRunView key={r.id} run={r} />)}
    </div>
  );
}

function CaseRow({ c }: { c: EvalCase }) {
  const qc = useQueryClient();
  const [exp, setExp] = useState(c.expectation);
  const save = useMutation({
    mutationFn: (enabled: boolean) => unwrap(api.PATCH("/eval-cases/{caseId}", { params: { path: { caseId: c.id } }, body: { enabled, expectation: exp } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["eval-cases", c.agentName] }),
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/eval-cases/{caseId}", { params: { path: { caseId: c.id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["eval-cases", c.agentName] }),
  });
  return (
    <tr className={c.enabled ? "" : "muted"}>
      <td><input type="checkbox" aria-label="Use this case" checked={c.enabled} onChange={(e) => save.mutate(e.target.checked)} /></td>
      <td><Badge tone={c.kind === "must_fix" ? "warn" : "neutral"} dot={false}>{KIND[c.kind]}</Badge></td>
      <td>
        <textarea aria-label="Expectation" rows={2} value={exp} maxLength={4000} onChange={(e) => setExp(e.target.value)}
          onBlur={() => exp !== c.expectation && save.mutate(c.enabled)} />
        <ErrorNote error={save.error ?? del.error} />
      </td>
      <td className="small">{c.sourceRunId ? <Link to={`/runs/${c.sourceRunId}`}>from run</Link> : "added by you"}</td>
      <td>
        {c.kind === "custom" && (
          <button type="button" className="btn btn-ghost btn-icon" aria-label="Delete case" onClick={() => del.mutate()}><Trash2 size={14} aria-hidden /></button>
        )}
      </td>
    </tr>
  );
}

/** An agent's evaluation suite: cases from your reviews and ratings, plus your own. */
export function EvalCasesPanel({ agent }: { agent: string }) {
  const qc = useQueryClient();
  const cases = useQuery({
    queryKey: ["eval-cases", agent],
    queryFn: () => unwrap(api.GET("/agents/{agentName}/eval-cases", { params: { path: { agentName: agent } } })),
  });
  const [adding, setAdding] = useState(false);
  const [input, setInput] = useState("{\n  \n}");
  const [exp, setExp] = useState("");
  const [parseError, setParseError] = useState("");
  const add = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      unwrap(api.POST("/agents/{agentName}/eval-cases", { params: { path: { agentName: agent } }, body: { input: body, expectation: exp } })),
    onSuccess: () => {
      setAdding(false);
      setExp("");
      void qc.invalidateQueries({ queryKey: ["eval-cases", agent] });
    },
  });
  const submit = () => {
    try {
      const v = JSON.parse(input) as unknown;
      if (typeof v !== "object" || v === null || Array.isArray(v)) throw new Error("the input must be a JSON object");
      setParseError("");
      add.mutate(v as Record<string, unknown>);
    } catch (e) {
      setParseError(e instanceof Error ? e.message : String(e));
    }
  };
  const items = cases.data?.items ?? [];
  return (
    <Panel title="Evaluation cases" count={items.length}
      actions={<button type="button" className="btn btn-sm" onClick={() => setAdding(!adding)}><Plus size={14} aria-hidden /> Add case</button>}>
      <ErrorNote error={cases.error} />
      <p className="small faint pad">
        Proposals to change this agent are replayed on these inputs. Outputs you approved become “accepted before” cases; outputs you rejected
        with a comment become “must fix” cases (the comment is the expectation). Switch off cases that no longer apply.
      </p>
      {adding && (
        <div className="settings-form pad">
          <Field label="Input (JSON, matching the agent's input schema)" grow>
            <textarea className="mono" rows={6} value={input} onChange={(e) => setInput(e.target.value)} />
          </Field>
          <Field label="What you expect" grow><textarea rows={2} value={exp} maxLength={4000} onChange={(e) => setExp(e.target.value)} /></Field>
          {parseError && <p className="small bad-text">{parseError}</p>}
          <ErrorNote error={add.error} />
          <div><button type="button" className="btn btn-primary" onClick={submit} disabled={add.isPending}>Add</button></div>
        </div>
      )}
      {cases.data && items.length === 0 ? <Empty>No cases yet: review or rate this agent's runs, or add one.</Empty> : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Use</th><th>Kind</th><th>Expectation</th><th>Source</th><th /></tr></thead>
            <tbody>{items.map((c) => <CaseRow key={c.id} c={c} />)}</tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}
