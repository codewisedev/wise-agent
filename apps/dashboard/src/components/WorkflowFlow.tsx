import {
  BaseEdge, EdgeLabelRenderer, Handle, PanOnScrollMode, Position, ReactFlow, getStraightPath,
  type Edge, type EdgeProps, type Node, type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { AlertTriangle, ArrowLeft, ArrowRight, Bot, Diamond, FileText, Flag, Inbox, KeyRound, Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import type { Schemas } from "../api/client";
import { useTheme } from "../theme";
import { AccountSelect } from "./AccountSelect";
import { Field, label } from "./ui";

export type TemplateStep = Schemas["TemplateStep"];
export type TemplateDef = Schemas["TemplateDefinition"];
type Issue = Schemas["PlanIssue"];
type Agent = Schemas["AgentSummary"];

/** What the flow shows for a step (task mode passes the task's overrides applied). */
export interface StepView {
  enabled: boolean;
  account?: string;
  model?: string;
  review: boolean;
}

const COLUMN_LABEL: Record<string, string> = { backlog: "Backlog", todo: "To do", in_progress: "In progress", review: "Review", done: "Done" };

export const viewOf = (s: TemplateStep): StepView => ({
  enabled: s.enabled ?? true, account: s.account || undefined, model: s.model || undefined, review: s.review ?? false,
});

type StepData = { step: TemplateStep; view: StepView; issues: string[]; selected: boolean; agent?: Agent; onSelect: () => void };
type EndData = { icon: ReactNode; title: string; sub?: string };

function StepNode({ data }: NodeProps<Node<StepData>>) {
  const { step, view, issues, selected, agent } = data;
  return (
    <button
      type="button"
      className={`flow-step${selected ? " selected" : ""}${view.enabled ? "" : " disabled"}${issues.length ? " invalid" : ""}`}
      onClick={data.onSelect}
      aria-pressed={selected}
      title={issues.join("\n") || undefined}
    >
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <span className="flow-step-name">{issues.length > 0 && <AlertTriangle size={12} aria-hidden />}{step.name || step.id}</span>
      <span className="flow-step-agent mono"><Bot size={11} aria-hidden />{step.agent}</span>
      <span className="flow-step-acct mono"><KeyRound size={11} aria-hidden />{view.account ? `${view.account}${view.model ? ` · ${view.model}` : ""}` : "default routing"}</span>
      {agent?.produces && <span className="flow-step-kind">→ {label(agent.produces)}</span>}
      {!view.enabled && <span className="flow-step-off">skipped</span>}
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </button>
  );
}

function GateNode() {
  return (
    <div className="flow-gate" title="Review: pauses until you pass the result on">
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <Diamond size={26} aria-hidden />
      <span>review</span>
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  );
}

function EndNode({ data }: NodeProps<Node<EndData>>) {
  return (
    <div className="flow-end">
      <Handle type="target" position={Position.Left} isConnectable={false} />
      {data.icon}
      <span className="flow-end-title">{data.title}</span>
      {data.sub && <span className="flow-end-sub">{data.sub}</span>}
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  );
}

type InsertData = { onInsert?: () => void };

function InsertEdge({ id, sourceX, sourceY, targetX, targetY, data }: EdgeProps<Edge<InsertData>>) {
  const [path, lx, ly] = getStraightPath({ sourceX, sourceY, targetX, targetY });
  return (
    <>
      <BaseEdge id={id} path={path} />
      {data?.onInsert && (
        <EdgeLabelRenderer>
          <button
            type="button"
            className="flow-insert nodrag nopan"
            style={{ transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)` }}
            onClick={data.onInsert}
            aria-label="Insert a step here"
            title="Insert a step here"
          >
            <Plus size={12} aria-hidden />
          </button>
        </EdgeLabelRenderer>
      )}
    </>
  );
}

const nodeTypes = { step: StepNode, gate: GateNode, end: EndNode };
const edgeTypes = { insert: InsertEdge };

export interface FlowProps {
  def: TemplateDef;
  views?: Record<string, StepView>; // per step id; defaults to the template's values
  issues: Issue[];
  agents: Agent[];
  selected?: string;
  onSelect: (id: string | undefined) => void;
  onInsert?: (index: number) => void; // template mode
  sourceLabel: string;
}

function useNarrow() {
  const [narrow, setNarrow] = useState(() => typeof window !== "undefined" && window.innerWidth < 720);
  useEffect(() => {
    const on = () => setNarrow(window.innerWidth < 720);
    window.addEventListener("resize", on);
    return () => window.removeEventListener("resize", on);
  }, []);
  return narrow;
}

/** The workflow as a left-to-right flowchart: source → steps (with review gates) → report → destination. */
export function WorkflowFlow({ def, views, issues, agents, selected, onSelect, onInsert, sourceLabel }: FlowProps) {
  const { pref } = useTheme();
  const narrow = useNarrow();
  const agentByName = useMemo(() => new Map(agents.map((a) => [a.name, a])), [agents]);
  const stepIssues = (id: string) => issues.filter((i) => i.stepId === id).map((i) => i.message);
  const viewFor = (s: TemplateStep) => views?.[s.id] ?? viewOf(s);
  const dest = COLUMN_LABEL[def.destination?.column ?? "done"] ?? "Done";
  const finalReport = def.finalReport ?? true;

  const { nodes, edges } = useMemo(() => {
    const nodes: Node[] = [];
    const edges: Edge[] = [];
    let x = 0;
    const y = 40;
    let prev = "source";
    nodes.push({ id: "source", type: "end", position: { x, y: y + 18 }, data: { icon: <Inbox size={16} aria-hidden />, title: "Tasks", sub: sourceLabel }, selectable: false });
    x += 170;
    const link = (to: string, insertAt?: number) => {
      edges.push({
        id: `${prev}->${to}`, source: prev, target: to, type: "insert",
        data: { onInsert: onInsert && insertAt !== undefined ? () => onInsert(insertAt) : undefined },
      });
      prev = to;
    };
    def.steps.forEach((s, i) => {
      const id = `step:${s.id}:${i}`;
      nodes.push({
        id, type: "step", position: { x, y }, selectable: false,
        data: { step: s, view: viewFor(s), issues: stepIssues(s.id), selected: selected === s.id, agent: agentByName.get(s.agent), onSelect: () => onSelect(selected === s.id ? undefined : s.id) },
      });
      link(id, i);
      x += 230;
      if (viewFor(s).review && viewFor(s).enabled) {
        const gid = `gate:${s.id}:${i}`;
        nodes.push({ id: gid, type: "gate", position: { x: x - 20, y: y + 22 }, data: {}, selectable: false });
        edges.push({ id: `${prev}->${gid}`, source: prev, target: gid, type: "insert" });
        prev = gid;
        x += 90;
      }
    });
    if (finalReport) {
      nodes.push({ id: "report", type: "end", position: { x, y: y + 18 }, data: { icon: <FileText size={16} aria-hidden />, title: "Final report" }, selectable: false });
      link("report", def.steps.length);
      x += 170;
    }
    nodes.push({ id: "dest", type: "end", position: { x, y: y + 18 }, data: { icon: <Flag size={16} aria-hidden />, title: dest, sub: "board column" }, selectable: false });
    link("dest", finalReport ? undefined : def.steps.length);
    return { nodes, edges };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [def, views, issues, agents, selected, sourceLabel, onInsert]);

  if (narrow) {
    return (
      <ol className="flow-list" aria-label="Workflow">
        <li className="flow-list-end"><Inbox size={14} aria-hidden /> Tasks · {sourceLabel}</li>
        {def.steps.map((s, i) => {
          const v = viewFor(s);
          const probs = stepIssues(s.id);
          return (
            <li key={`${s.id}-${i}`}>
              <button type="button" className={`flow-step${selected === s.id ? " selected" : ""}${v.enabled ? "" : " disabled"}${probs.length ? " invalid" : ""}`} onClick={() => onSelect(selected === s.id ? undefined : s.id)}>
                <span className="flow-step-name">{probs.length > 0 && <AlertTriangle size={12} aria-hidden />}{s.name || s.id}</span>
                <span className="flow-step-agent mono">{s.agent}</span>
                <span className="flow-step-acct mono">{v.account ?? "default routing"}</span>
              </button>
              {v.review && v.enabled && <span className="flow-list-gate"><Diamond size={13} aria-hidden /> review</span>}
              {onInsert && <button type="button" className="btn btn-ghost btn-sm" onClick={() => onInsert(i + 1)}><Plus size={12} aria-hidden /> step</button>}
            </li>
          );
        })}
        {finalReport && <li className="flow-list-end"><FileText size={14} aria-hidden /> Final report</li>}
        <li className="flow-list-end"><Flag size={14} aria-hidden /> {dest}</li>
      </ol>
    );
  }

  return (
    <div className="flow" role="group" aria-label="Workflow flowchart">
      <ReactFlow
        nodes={nodes} edges={edges} nodeTypes={nodeTypes} edgeTypes={edgeTypes}
        colorMode={pref === "system" ? "system" : pref}
        nodesDraggable={false} nodesConnectable={false} elementsSelectable={false}
        zoomOnScroll={false} panOnScroll panOnScrollMode={PanOnScrollMode.Horizontal} preventScrolling={false}
        minZoom={0.5} maxZoom={1.4}
        // Readable first and read from the source: start left-aligned at 90%;
        // wide flows pan sideways (scroll or drag).
        defaultViewport={{ x: 16, y: 32, zoom: 0.9 }}
      />
    </div>
  );
}

/** Edits one step of a template (all fields) or of a task (enable/account/model/review only). */
export function StepInspector({
  def, index, agents, mode, view, onChange, onViewChange, onMove, onDelete,
}: {
  def: TemplateDef;
  index: number;
  agents: Agent[];
  mode: "template" | "task";
  view?: StepView;
  onChange?: (s: TemplateStep) => void;
  onViewChange?: (v: StepView) => void;
  onMove?: (dir: -1 | 1) => void;
  onDelete?: () => void;
}) {
  const step = def.steps[index]!;
  const v = view ?? viewOf(step);
  const earlier = def.steps.slice(0, index);
  const agent = agents.find((a) => a.name === step.agent);
  const setV = (patch: Partial<StepView>) => {
    if (mode === "task") onViewChange?.({ ...v, ...patch });
    else onChange?.({ ...step, ...("enabled" in patch ? { enabled: patch.enabled } : {}), ...("account" in patch ? { account: patch.account || undefined } : {}),
      ...("model" in patch ? { model: patch.model || undefined } : {}), ...("review" in patch ? { review: patch.review } : {}) });
  };

  return (
    <aside className="inspector" aria-label={`Step ${step.name || step.id}`}>
      <header>
        <h3>{step.name || step.id}</h3>
        {mode === "template" && (
          <div className="inspector-tools">
            <button type="button" className="btn btn-ghost btn-icon" aria-label="Move earlier" disabled={index === 0} onClick={() => onMove?.(-1)}><ArrowLeft size={14} aria-hidden /></button>
            <button type="button" className="btn btn-ghost btn-icon" aria-label="Move later" disabled={index === def.steps.length - 1} onClick={() => onMove?.(1)}><ArrowRight size={14} aria-hidden /></button>
            <button type="button" className="btn btn-ghost btn-icon" aria-label="Delete step" onClick={onDelete}><Trash2 size={14} aria-hidden /></button>
          </div>
        )}
      </header>
      {mode === "template" && (
        <>
          <Field label="Name">
            <input value={step.name ?? ""} onChange={(e) => onChange?.({ ...step, name: e.target.value })} maxLength={100} />
          </Field>
          <Field label="Step id" hint="Lowercase; referenced by reviews">
            <input className="mono" value={step.id} onChange={(e) => onChange?.({ ...step, id: e.target.value.toLowerCase() })} maxLength={41} />
          </Field>
          <Field label="Agent" hint={agent ? `${agent.description}` : undefined}>
            <select value={step.agent} onChange={(e) => onChange?.({ ...step, agent: e.target.value })}>
              {agents.filter((a) => a.enabled || a.name === step.agent).map((a) => (
                <option key={a.name} value={a.name} disabled={!a.enabled}>{a.name}{a.enabled ? "" : " (later phase)"}</option>
              ))}
            </select>
          </Field>
          {agent && (
            <p className="small faint">
              produces <code className="chip">{agent.produces ?? "—"}</code>
              {agent.consumes && Object.keys(agent.consumes).length > 0 && <> · reads {Object.values(agent.consumes).map((k) => <code key={k} className="chip">{k}</code>)}</>}
            </p>
          )}
        </>
      )}
      <label className="checkbox">
        <input type="checkbox" checked={v.enabled} onChange={(e) => setV({ enabled: e.target.checked })} />
        <span>Run this step</span>
      </label>
      <Field label="Account" hint="Who runs the step; default = the agent's model alias with fallbacks">
        <AccountSelect value={v.account ?? ""} onChange={(a) => setV({ account: a, ...(a ? {} : { model: "" }) })} defaultLabel="default routing" />
      </Field>
      {v.account && (
        <Field label="Model" hint="Empty = the account's default model">
          <input value={v.model ?? ""} onChange={(e) => setV({ model: e.target.value })} />
        </Field>
      )}
      <label className="checkbox">
        <input type="checkbox" checked={v.review} onChange={(e) => setV({ review: e.target.checked })} />
        <span>Review the result before passing it on</span>
      </label>
      {mode === "template" && v.review && earlier.length > 0 && (
        <>
          <fieldset className="inspector-set">
            <legend className="small">The review also pins</legend>
            {earlier.map((s) => (
              <label key={s.id} className="checkbox small">
                <input
                  type="checkbox"
                  checked={step.reviewIncludes?.includes(s.id) ?? false}
                  onChange={(e) => onChange?.({
                    ...step,
                    reviewIncludes: e.target.checked ? [...(step.reviewIncludes ?? []), s.id] : (step.reviewIncludes ?? []).filter((x) => x !== s.id),
                  })}
                />
                <span>{s.name || s.id}</span>
              </label>
            ))}
          </fieldset>
          <Field label="On “request changes”, re-run from">
            <select value={step.reviseFrom ?? ""} onChange={(e) => onChange?.({ ...step, reviseFrom: e.target.value || undefined })}>
              <option value="">this step</option>
              {earlier.map((s) => <option key={s.id} value={s.id}>{s.name || s.id}</option>)}
            </select>
          </Field>
        </>
      )}
    </aside>
  );
}
