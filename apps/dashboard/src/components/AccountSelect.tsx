import { useAccounts } from "../pages/AccountsPanel";

const MODEL_KINDS = new Set(["claude_code", "codex_cli", "openai_compatible"]);

/** Picks a model account (or a models.yaml provider); "" = the agent's default routing. */
export function AccountSelect({
  value,
  onChange,
  defaultLabel = "agent default (alias + fallbacks)",
  id,
}: {
  value: string;
  onChange: (v: string) => void;
  defaultLabel?: string;
  id?: string;
}) {
  const accounts = useAccounts();
  const models = accounts.data?.items.filter((a) => MODEL_KINDS.has(a.kind) && a.enabled) ?? [];
  return (
    <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">{defaultLabel}</option>
      {models.length > 0 && (
        <optgroup label="Accounts">
          {models.map((a) => (
            <option key={a.id} value={a.name}>
              {a.name}{a.defaultModel ? ` · ${a.defaultModel}` : ""}
            </option>
          ))}
        </optgroup>
      )}
      {(accounts.data?.builtinProviders.length ?? 0) > 0 && (
        <optgroup label="models.yaml">
          {accounts.data!.builtinProviders.map((p) => <option key={p} value={p}>{p}</option>)}
        </optgroup>
      )}
    </select>
  );
}
