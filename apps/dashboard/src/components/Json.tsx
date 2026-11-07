import { CopyText } from "./ui";

/** Pretty-printed JSON block with a copy button. */
export function Json({ value, label = "JSON", maxHeight = 420 }: { value: unknown; label?: string; maxHeight?: number }) {
  const text = JSON.stringify(value, null, 2);
  return (
    <div className="json-block">
      <div className="json-actions">
        <CopyText value={text} display="copy" label={label} />
      </div>
      <pre className="json" style={{ maxHeight }}>
        {text}
      </pre>
    </div>
  );
}
