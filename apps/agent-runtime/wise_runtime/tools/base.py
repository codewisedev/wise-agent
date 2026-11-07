from __future__ import annotations


class ToolError(Exception):
    """A tool failed in a way the model should see and can react to."""
