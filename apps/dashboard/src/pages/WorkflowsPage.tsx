import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Plus, Save, Star, Trash2, Workflow } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { StepInspector, WorkflowFlow, type TemplateDef, type TemplateStep } from "../components/WorkflowFlow";
import { Badge, Empty, ErrorNote, Field, Panel, relativeTime } from "../components/ui";

type Template = Schemas["WorkflowTemplate"];
const ROLES: Schemas["ColumnRole"][] = ["backlog", "todo", "in_progress", "review", "done"];

const BLANK: TemplateDef = {
  steps: [{ id: "analysis", name: "Product analysis", agent: "product-agent", review: true }],
  destination: { column: "review" },
  finalReport: true,
};

function uniqueId(def: TemplateDef, base: string): string {
  let id = base;
  for (let n = 2; def.steps.some((s) => s.id === id); n++) id = `${base}-${n}`;
  return id;
}

/** Live validation: resolves the draft on the server (debounced). */
function usePreview(def: TemplateDef | null) {
  const [debounced, setDebounced] = useState(def);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(def), 350);
    return () => window.clearTimeout(t);
  }, [def]);
  return useQuery({
    queryKey: ["template-preview", JSON.stringify(debounced)],
    queryFn: () => unwrap(api.POST("/workflow-templates/preview", { body: debounced! })),
    enabled: !!debounced,
    placeholderData: (prev) => prev,
  });
}

function Editor({ template, agents, onSaved }: { template: Template | null; agents: Schemas["AgentSummary"][]; onSaved: (t: Template) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(template?.name ?? "New workflow");
  const [description, setDescription] = useState(template?.description ?? "");
  const [def, setDef] = useState<TemplateDef>(template?.definition ?? BLANK);
  const [selected, setSelected] = useState<string | undefined>(def.steps[0]?.id);
  const [dirty, setDirty] = useState(!template);
  const preview = usePreview(def);
  const issues = preview.data?.issues ?? [];

  const update = (next: TemplateDef) => {
    setDef(next);
    setDirty(true);
  };
  const insert = useCallback((index: number) => {
    const enabled = agents.find((a) => a.enabled && a.name === "product-agent") ?? agents.find((a) => a.enabled);
    const id = uniqueId(def, "step");
    const step: TemplateStep = { id, name: "New step", agent: enabled?.name ?? "product-agent" };
    const steps = [...def.steps];
    steps.splice(index, 0, step);
    setDef({ ...def, steps });
    setSelected(id);
    setDirty(true);
  }, [agents, def]);
  const idx = def.steps.findIndex((s) => s.id === selected);

  const save = useMutation({
    mutationFn: () => {
      const body = { name: name.trim(), description, definition: def };
      return unwrap(template
        ? api.PUT("/workflow-templates/{templateId}", { params: { path: { templateId: template.id } }, body })
        : api.POST("/workflow-templates", { body }));
    },
    onSuccess: (t) => {
      setDirty(false);
      void qc.invalidateQueries({ queryKey: ["workflow-templates"] });
      onSaved(t);
    },
  });
  const makeDefault = useMutation({
    mutationFn: () => unwrap(api.PUT("/workflow-templates/{templateId}", {
      params: { path: { templateId: template!.id } }, body: { name: template!.name, description: template!.description, definition: template!.definition, isDefault: true },
    })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["workflow-templates"] }),
  });
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/workflow-templates/{templateId}", { params: { path: { templateId: template!.id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["workflow-templates"] });
      onSaved(null as unknown as Template);
    },
  });
  const duplicate = useMutation({
    mutationFn: () => unwrap(api.POST("/workflow-templates", { body: { name: `${name} (copy)`, description, definition: def } })),
    onSuccess: (t) => {
      void qc.invalidateQueries({ queryKey: ["workflow-templates"] });
      onSaved(t);
    },
  });

  return (
    <Panel
      title={template ? "Edit workflow" : "New workflow"}
      actions={
        <div className="panel-actions">
          {template && !template.isDefault && (
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => makeDefault.mutate()} title="New tasks use the default workflow">
              <Star size={13} aria-hidden /> Make default
            </button>
          )}
          {template && (
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => duplicate.mutate()}><Copy size={13} aria-hidden /> Duplicate</button>
          )}
          {template && !template.isDefault && (
            <button type="button" className="btn btn-ghost btn-icon" aria-label="Delete workflow"
              onClick={() => window.confirm(`Delete workflow "${template.name}"? Tasks using it fall back to the default.`) && del.mutate()}>
              <Trash2 size={14} aria-hidden />
            </button>
          )}
          <button type="button" className="btn btn-primary btn-sm" onClick={() => save.mutate()} disabled={!dirty || save.isPending || issues.length > 0 || !name.trim()}
            title={issues.length ? "Fix the highlighted problems first" : undefined}>
            <Save size={13} aria-hidden /> Save
          </button>
        </div>
      }
    >
      <div className="panel-body">
        <div className="form-grid">
          <Field label="Name">
            <input value={name} onChange={(e) => { setName(e.target.value); setDirty(true); }} maxLength={100} />
          </Field>
          <Field label="Description" grow>
            <input value={description} onChange={(e) => { setDescription(e.target.value); setDirty(true); }} maxLength={2000} />
          </Field>
        </div>
        <div className="form-grid wf-options" style={{ marginTop: 10 }}>
          <label className="checkbox">
            <input type="checkbox" checked={def.source?.autoStart ?? false} onChange={(e) => update({ ...def, source: { ...def.source, autoStart: e.target.checked } })} />
            <span>Run automatically when a task is imported</span>
          </label>
          <label className="inline-field">
            <span>Finished tasks go to the</span>
            <select value={def.destination?.column ?? "done"} onChange={(e) => update({ ...def, destination: { column: e.target.value as Schemas["ColumnRole"] } })}>
              {ROLES.map((c) => <option key={c} value={c}>{c.replace("_", " ")}</option>)}
            </select>
            <span>list</span>
          </label>
          <label className="checkbox">
            <input type="checkbox" checked={def.finalReport ?? true} onChange={(e) => update({ ...def, finalReport: e.target.checked })} />
            <span>Write a final report</span>
          </label>
        </div>
      </div>
      <div className="flow-editor">
        <WorkflowFlow
          def={def} issues={issues} agents={agents} selected={selected} onSelect={setSelected} onInsert={insert}
          sourceLabel={def.source?.autoStart ? "board + imports (auto-run)" : "board"}
        />
        {idx >= 0 ? (
          <StepInspector
            def={def} index={idx} agents={agents} mode="template"
            onChange={(s) => {
              const steps = [...def.steps];
              const oldId = steps[idx]!.id;
              steps[idx] = s;
              // Keep references valid when a step id changes.
              const fixed = s.id === oldId ? steps : steps.map((x) => ({
                ...x,
                reviewIncludes: x.reviewIncludes?.map((r) => (r === oldId ? s.id : r)),
                reviseFrom: x.reviseFrom === oldId ? s.id : x.reviseFrom,
              }));
              update({ ...def, steps: fixed });
              setSelected(s.id);
            }}
            onMove={(dir) => {
              const steps = [...def.steps];
              const [s] = steps.splice(idx, 1);
              steps.splice(idx + dir, 0, s!);
              update({ ...def, steps });
            }}
            onDelete={() => {
              const removed = def.steps[idx]!.id;
              const steps = def.steps.filter((_, i) => i !== idx).map((x) => ({
                ...x,
                reviewIncludes: x.reviewIncludes?.filter((r) => r !== removed),
                reviseFrom: x.reviseFrom === removed ? undefined : x.reviseFrom,
              }));
              update({ ...def, steps });
              setSelected(undefined);
            }}
          />
        ) : (
          <aside className="inspector inspector-hint">
            <p className="small faint">Select a step to edit it, or use + on a connection to insert one.</p>
            {issues.filter((i) => !i.stepId).map((i) => <p key={i.message} className="wf-issue small">{i.message}</p>)}
          </aside>
        )}
      </div>
      {issues.length > 0 && (
        <ul className="flow-issues small">{issues.map((i) => <li key={`${i.stepId}-${i.message}`}>{i.stepId ? <strong>{i.stepId}: </strong> : null}{i.message}</li>)}</ul>
      )}
      <ErrorNote error={save.error ?? makeDefault.error ?? del.error ?? duplicate.error ?? preview.error} />
      {template && <TemplateHistory template={template} dirty={dirty} onRestored={onSaved} />}
    </Panel>
  );
}

/** Earlier definitions of a template (every save keeps the one it replaced), with Restore. */
function TemplateHistory({ template, dirty, onRestored }: { template: Template; dirty: boolean; onRestored: (t: Template) => void }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const versions = useQuery({
    queryKey: ["workflow-templates", template.id, "versions", template.version],
    queryFn: () => unwrap(api.GET("/workflow-templates/{templateId}/versions", { params: { path: { templateId: template.id } } })),
    enabled: open,
  });
  const restore = useMutation({
    mutationFn: (version: number) =>
      unwrap(api.POST("/workflow-templates/{templateId}/versions/{version}/restore", { params: { path: { templateId: template.id, version } } })),
    onSuccess: (t) => {
      void qc.invalidateQueries({ queryKey: ["workflow-templates"] });
      onRestored(t);
    },
  });
  return (
    <details className="template-history" onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary className="small">History (version {template.version})</summary>
      <ErrorNote error={versions.error ?? restore.error} />
      {versions.data && versions.data.items.length === 0 && <p className="small faint">No earlier versions yet.</p>}
      <ul className="small">
        {versions.data?.items.map((v) => (
          <li key={v.version}>
            version {v.version} · {v.name} · {v.definition && Array.isArray((v.definition as { steps?: unknown[] }).steps) ? `${(v.definition as { steps: unknown[] }).steps.length} steps` : ""} · replaced {relativeTime(v.createdAt)}{" "}
            <button type="button" className="btn btn-sm" disabled={restore.isPending || dirty}
              title={dirty ? "Save or discard your changes first" : "Save this definition as the new version"}
              onClick={() => restore.mutate(v.version)}>Restore</button>
          </li>
        ))}
      </ul>
    </details>
  );
}

export function WorkflowsPage() {
  const [params, setParams] = useSearchParams();
  const templates = useQuery({ queryKey: ["workflow-templates"], queryFn: () => unwrap(api.GET("/workflow-templates")) });
  const agents = useQuery({ queryKey: ["agents"], queryFn: () => unwrap(api.GET("/agents")) });
  const selectedId = params.get("id") ?? "";
  const creating = params.get("new") === "1";
  const items = templates.data?.items ?? [];
  const current = creating ? null : items.find((t) => t.id === selectedId) ?? items[0] ?? null;
  const select = (id?: string, isNew = false) => setParams(isNew ? { new: "1" } : id ? { id } : {}, { replace: true });

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Workflows</h1>
          <p className="desc">Which agents handle a task, in what order, with which account, and where it pauses for your review.</p>
        </div>
        <button className="btn btn-primary" onClick={() => select(undefined, true)}><Plus size={15} aria-hidden /> New workflow</button>
      </header>
      <ErrorNote error={templates.error ?? agents.error} />
      <div className="workflows">
        <nav className="workflow-list" aria-label="Workflow templates">
          {items.length === 0 && <Empty icon={<Workflow size={22} aria-hidden />}>No workflows.</Empty>}
          {items.map((t) => (
            <button key={t.id} type="button" className={`workflow-item${current?.id === t.id && !creating ? " active" : ""}`} onClick={() => select(t.id)}>
              <span className="workflow-item-name">{t.name}{t.isDefault && <Badge tone="ok" dot={false}>default</Badge>}</span>
              <span className="small faint">{t.definition.steps.length} steps · v{t.version}</span>
            </button>
          ))}
        </nav>
        {agents.data && (creating || current) && (
          <Editor key={creating ? "new" : `${current!.id}:${current!.version}`} template={creating ? null : current} agents={agents.data.items}
            onSaved={(t) => select(t?.id)} />
        )}
      </div>
    </>
  );
}
