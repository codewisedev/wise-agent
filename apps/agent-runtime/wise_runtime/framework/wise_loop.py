"""WiseLoop: the built-in agent loop.

Each turn sends the transcript to the gateway, which returns either tool calls
or a final answer (the envelope protocol). Tool results are appended to the
transcript. The final answer is validated against the agent's output schema;
invalid answers are sent back with the errors, up to
error_handling.invalid_output_retries times.
"""

from __future__ import annotations

import json
from typing import Any

import jsonschema

from .base import RunContext, RunFailed

MAX_TOOL_RESULT_CHARS = 30_000


def _render_input(ctx: RunContext) -> str:
    parts = [
        "# Task input",
        "```json",
        json.dumps(ctx.run.get("input", {}), indent=2, ensure_ascii=False),
        "```",
    ]
    if ctx.project:
        parts.append(
            f"\nA project is attached: **{ctx.project['name']}**. Call `get_project_context` to see its "
            "repositories before using the repository tools."
        )
    elif ctx.tools.definitions:
        parts.append("\nNo project is attached to this run; repository tools will return errors.")
    parts.append("\nProduce the final output when you have enough information.")
    return "\n".join(parts)


def _tool_message(name: str, output: Any, error: str | None) -> str:
    text = json.dumps({"error": error} if error else output, ensure_ascii=False)
    if len(text) > MAX_TOOL_RESULT_CHARS:
        text = text[:MAX_TOOL_RESULT_CHARS] + f"… [truncated {len(text) - MAX_TOOL_RESULT_CHARS} chars]"
    return text


def _schema_errors(output: Any, schema: dict[str, Any]) -> list[str]:
    validator = jsonschema.Draft202012Validator(schema)
    errors = []
    for e in sorted(validator.iter_errors(output), key=lambda e: list(e.path)):
        where = "/".join(str(p) for p in e.path) or "(root)"
        errors.append(f"{where}: {e.message}")
    return errors[:20]


class WiseLoop:
    name = "wise-loop"

    def run(self, ctx: RunContext) -> dict[str, Any]:
        limits = ctx.limits
        max_turns = int(limits.get("max_turns", 10))
        max_tool_calls = int(limits.get("max_tool_calls", 20))
        invalid_budget = int(ctx.definition.get("error_handling", {}).get("invalid_output_retries", 2))
        output_schema = ctx.definition.get("output_schema", {"type": "object"})

        messages: list[dict[str, str]] = [{"role": "user", "content": _render_input(ctx)}]
        tool_calls_used = 0
        invalid = 0

        for turn_no in range(1, max_turns + 1):
            ctx.check()
            turn = ctx.model_turn(messages)
            ctx.check()
            action = turn.get("action")
            if turn.get("thought"):
                ctx.events.emit("agent.message", {"turn": turn_no, "thought": turn["thought"]})

            if action == "invalid":
                invalid += 1
                reason = turn.get("parseError", "response did not follow the protocol")
                ctx.events.emit("output.invalid", {"turn": turn_no, "reason": reason, "attempt": invalid})
                ctx.events.flush()
                if invalid > invalid_budget:
                    raise RunFailed(f"model kept returning invalid responses: {reason}")
                messages.append({"role": "assistant", "content": turn.get("rawText") or ""})
                messages.append(
                    {
                        "role": "user",
                        "content": f"Your last response was invalid: {reason}. "
                        "Respond with exactly one JSON object that follows the response protocol.",
                    }
                )
                continue

            if action == "call_tools":
                calls = turn.get("toolCalls") or []
                messages.append(
                    {
                        "role": "assistant",
                        "content": json.dumps(
                            {"thought": turn.get("thought", ""), "action": "call_tools", "tool_calls": calls},
                            ensure_ascii=False,
                        ),
                    }
                )
                for call in calls:
                    ctx.check()
                    name, args = call["tool"], call.get("arguments") or {}
                    ctx.events.emit("tool.call", {"turn": turn_no, "tool": name, "input": args})
                    if tool_calls_used >= max_tool_calls:
                        output, error, ms = (
                            None,
                            (f"tool call limit reached ({max_tool_calls}); give your final answer now"),
                            0,
                        )
                    else:
                        tool_calls_used += 1
                        output, error, ms = ctx.tools.execute(name, args)
                    ctx.events.emit(
                        "tool.result",
                        {
                            "turn": turn_no,
                            "tool": name,
                            "input": args,
                            "output": output,
                            "status": "error" if error else "ok",
                            "error": error,
                            "durationMs": ms,
                        },
                    )
                    messages.append(
                        {"role": "tool", "name": name, "content": _tool_message(name, output, error)}
                    )
                ctx.events.flush()
                continue

            if action == "final":
                final = turn.get("final")
                errors = (
                    _schema_errors(final, output_schema)
                    if isinstance(final, dict)
                    else ["final must be an object"]
                )
                if not errors:
                    ctx.events.flush()
                    return final
                invalid += 1
                ctx.events.emit(
                    "output.invalid",
                    {"turn": turn_no, "reason": "schema", "errors": errors, "attempt": invalid},
                )
                ctx.events.flush()
                if invalid > invalid_budget:
                    raise RunFailed("final output does not match output_schema: " + "; ".join(errors[:5]))
                messages.append(
                    {
                        "role": "assistant",
                        "content": json.dumps({"action": "final", "final": final}, ensure_ascii=False),
                    }
                )
                messages.append(
                    {
                        "role": "user",
                        "content": "Your final output does not match the required output schema:\n- "
                        + "\n- ".join(errors)
                        + "\nFix these problems and send the corrected final output.",
                    }
                )
                continue

            raise RunFailed(f"unexpected action from gateway: {action!r}")

        raise RunFailed(f"no final answer after {max_turns} turns (execution_limits.max_turns)")
