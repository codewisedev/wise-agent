import { Fragment, type ReactNode } from "react";
import { label } from "./ui";

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);

/** Fields shown as an item's heading, in order of preference. */
const TITLE_KEYS = ["title", "name", "id"];
/** Reading order for known agent output fields; unknown fields follow alphabetically. */
const KEY_ORDER = [
  "title", "summary", "problem", "target_users", "goals", "non_goals", "key_features", "user_stories",
  "functional_requirements", "non_functional_requirements", "scope_change", "components", "decisions", "steps",
  "milestones", "contracts_changed", "data_changes", "test_strategy", "assumptions", "risks", "concerns",
  "out_of_scope", "open_questions",
];
const rank = (k: string) => {
  const i = KEY_ORDER.indexOf(k);
  return i < 0 ? KEY_ORDER.length : i;
};
const ordered = (o: Obj) => Object.entries(o).sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b));

/** Short enum-like fields shown as chips next to the heading. */
const CHIP_KEYS = ["priority", "risk", "category", "change", "scope_change"];

function Scalar({ value }: { value: unknown }) {
  if (value === null || value === undefined || value === "") return <span className="faint">—</span>;
  if (typeof value === "string") return <>{value}</>;
  return <code>{JSON.stringify(value)}</code>;
}

function Item({ item }: { item: Obj }) {
  const titleKey = TITLE_KEYS.find((k) => typeof item[k] === "string");
  const id = typeof item.id === "string" && titleKey !== "id" ? item.id : undefined;
  const chips = CHIP_KEYS.filter((k) => typeof item[k] === "string");
  const rest = ordered(item).map(([k]) => k).filter((k) => k !== titleKey && k !== "id" && !chips.includes(k));
  return (
    <li className="art-item">
      <div className="art-item-head">
        {id && <code className="chip">{id}</code>}
        {titleKey && <strong>{String(item[titleKey])}</strong>}
        {chips.map((k) => (
          <span key={k} className={`chip chip-${String(item[k])}`}>
            {String(item[k])}
          </span>
        ))}
      </div>
      {rest.length > 0 && (
        <dl className="art-fields">
          {rest.map((k) => (
            <Fragment key={k}>
              <dt>{label(k)}</dt>
              <dd>
                <Value value={item[k]} nested />
              </dd>
            </Fragment>
          ))}
        </dl>
      )}
    </li>
  );
}

function Value({ value, nested }: { value: unknown; nested?: boolean }): ReactNode {
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="faint">none</span>;
    if (value.every((v) => !isObj(v))) {
      const short = value.every((v) => String(v).length <= 40);
      return nested && short ? (
        <span className="art-inline">{value.map((v, i) => <code key={i} className="chip">{String(v)}</code>)}</span>
      ) : (
        <ul className="bullets">{value.map((v, i) => <li key={i}><Scalar value={v} /></li>)}</ul>
      );
    }
    return <ul className="art-items">{value.map((v, i) => (isObj(v) ? <Item key={i} item={v} /> : <li key={i}><Scalar value={v} /></li>))}</ul>;
  }
  if (isObj(value)) {
    return (
      <dl className="art-fields">
        {ordered(value).map(([k, v]) => (
          <Fragment key={k}>
            <dt>{label(k)}</dt>
            <dd><Value value={v} nested /></dd>
          </Fragment>
        ))}
      </dl>
    );
  }
  return <Scalar value={value} />;
}

/** Renders a structured agent output as readable sections (keys become headings). */
export function ArtifactView({ content }: { content: Obj }) {
  if (typeof content.markdown === "string") return <Markdown text={content.markdown} />;
  return (
    <div className="artifact">
      {ordered(content).map(([k, v]) => (
        <section key={k} className="art-section">
          <h3>{label(k)}</h3>
          <Value value={v} />
        </section>
      ))}
    </div>
  );
}

function inline(text: string): ReactNode {
  // **bold** only; everything else is plain text (never HTML).
  return text.split(/(\*\*[^*]+\*\*)/g).map((part, i) =>
    part.startsWith("**") && part.endsWith("**") ? <strong key={i}>{part.slice(2, -2)}</strong> : part,
  );
}

/** Minimal markdown for generated reports: headings, bullets, quotes, bold, paragraphs. */
export function Markdown({ text }: { text: string }) {
  const blocks: ReactNode[] = [];
  let bullets: string[] = [];
  const flush = () => {
    if (bullets.length) {
      const items = bullets;
      blocks.push(<ul key={blocks.length} className="bullets">{items.map((b, i) => <li key={i}>{inline(b)}</li>)}</ul>);
      bullets = [];
    }
  };
  for (const line of text.split("\n")) {
    if (line.startsWith("- ")) {
      bullets.push(line.slice(2));
      continue;
    }
    flush();
    if (line.startsWith("# ")) blocks.push(<h2 key={blocks.length}>{inline(line.slice(2))}</h2>);
    else if (line.startsWith("## ")) blocks.push(<h3 key={blocks.length}>{inline(line.slice(3))}</h3>);
    else if (line.startsWith("> ")) blocks.push(<blockquote key={blocks.length}>{inline(line.slice(2))}</blockquote>);
    else if (line.trim()) blocks.push(<p key={blocks.length}>{inline(line)}</p>);
  }
  flush();
  return <div className="artifact md">{blocks}</div>;
}
