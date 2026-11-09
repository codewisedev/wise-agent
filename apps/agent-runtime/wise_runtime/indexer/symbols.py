"""AST symbol and import extraction with tree-sitter.

Each language has small queries whose captures are named ``def.<kind>`` (the
whole definition), ``name`` and, for imports, ``import``. Patterns that a
grammar version does not support are skipped individually, so one bad pattern
never disables a language.
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass, field
from functools import cache

import tree_sitter as ts
import tree_sitter_language_pack as tslp

log = logging.getLogger(__name__)

# extension -> tree-sitter language
LANGUAGES: dict[str, str] = {
    ".go": "go",
    ".ts": "typescript",
    ".mts": "typescript",
    ".cts": "typescript",
    ".tsx": "tsx",
    ".js": "javascript",
    ".mjs": "javascript",
    ".cjs": "javascript",
    ".jsx": "javascript",
    ".py": "python",
    ".rs": "rust",
    ".java": "java",
    ".vue": "vue",
}

QUERIES: dict[str, list[str]] = {
    "go": [
        "(function_declaration name: (identifier) @name) @def.function",
        "(method_declaration name: (field_identifier) @name) @def.method",
        "(type_spec name: (type_identifier) @name) @def.type",
        "(source_file (const_declaration (const_spec name: (identifier) @name) @def.const))",
        "(import_spec path: (interpreted_string_literal) @import)",
    ],
    "typescript": [
        "(function_declaration name: (identifier) @name) @def.function",
        "(class_declaration name: (type_identifier) @name) @def.class",
        "(abstract_class_declaration name: (type_identifier) @name) @def.class",
        "(method_definition name: (property_identifier) @name) @def.method",
        "(interface_declaration name: (type_identifier) @name) @def.interface",
        "(type_alias_declaration name: (type_identifier) @name) @def.type",
        "(enum_declaration name: (identifier) @name) @def.enum",
        "(lexical_declaration (variable_declarator name: (identifier) @name"
        " value: [(arrow_function) (function_expression)])) @def.function",
        "(program (lexical_declaration (variable_declarator name: (identifier) @name)) @def.const)",
        "(program (export_statement"
        " (lexical_declaration (variable_declarator name: (identifier) @name)) @def.const))",
        "(import_statement source: (string) @import)",
    ],
    "javascript": [
        "(function_declaration name: (identifier) @name) @def.function",
        "(class_declaration name: (identifier) @name) @def.class",
        "(method_definition name: (property_identifier) @name) @def.method",
        "(lexical_declaration (variable_declarator name: (identifier) @name"
        " value: [(arrow_function) (function_expression)])) @def.function",
        "(program (lexical_declaration (variable_declarator name: (identifier) @name)) @def.const)",
        "(program (export_statement"
        " (lexical_declaration (variable_declarator name: (identifier) @name)) @def.const))",
        "(import_statement source: (string) @import)",
    ],
    "python": [
        "(function_definition name: (identifier) @name) @def.function",
        "(class_definition name: (identifier) @name) @def.class",
        "(import_from_statement module_name: (dotted_name) @import)",
        "(import_statement name: (dotted_name) @import)",
    ],
    "rust": [
        "(function_item name: (identifier) @name) @def.function",
        "(struct_item name: (type_identifier) @name) @def.struct",
        "(enum_item name: (type_identifier) @name) @def.enum",
        "(trait_item name: (type_identifier) @name) @def.interface",
        "(type_item name: (type_identifier) @name) @def.type",
        "(const_item name: (identifier) @name) @def.const",
        "(mod_item name: (identifier) @name) @def.module",
        "(use_declaration argument: (_) @import)",
    ],
    "java": [
        "(class_declaration name: (identifier) @name) @def.class",
        "(interface_declaration name: (identifier) @name) @def.interface",
        "(enum_declaration name: (identifier) @name) @def.enum",
        "(method_declaration name: (identifier) @name) @def.method",
        "(constructor_declaration name: (identifier) @name) @def.method",
        "(import_declaration (scoped_identifier) @import)",
    ],
}
QUERIES["tsx"] = QUERIES["typescript"]

# Node types that make a definition belong to a parent (class, impl, interface…).
PARENT_TYPES = {
    "class_declaration",
    "abstract_class_declaration",
    "class_definition",
    "class_body",
    "interface_declaration",
    "impl_item",
    "trait_item",
    "enum_declaration",
}
WRAPPERS = {
    "export_statement",
    "type_declaration",
    "const_declaration",
    "lexical_declaration",
    "variable_declaration",
    "decorated_definition",
}


@dataclass
class Symbol:
    name: str
    kind: str
    start_line: int  # 1-based, inclusive
    end_line: int
    signature: str = ""
    parent: str = ""
    exported: bool = False
    doc: str = ""

    def as_json(self) -> dict:
        return {
            "name": self.name,
            "kind": self.kind,
            "signature": self.signature,
            "parent": self.parent,
            "exported": self.exported,
            "startLine": self.start_line,
            "endLine": self.end_line,
            "doc": self.doc,
        }


@dataclass
class Parsed:
    symbols: list[Symbol] = field(default_factory=list)
    imports: list[str] = field(default_factory=list)


def language_for(path: str) -> str:
    dot = path.rfind(".")
    return LANGUAGES.get(path[dot:].lower(), "") if dot >= 0 else ""


@cache
def _compiled(lang: str) -> tuple[ts.Language, list[ts.Query]] | None:
    try:
        language = tslp.get_language(lang)  # type: ignore[arg-type]
    except Exception as e:  # grammar missing from the pack
        log.warning("no tree-sitter grammar for %s: %s", lang, e)
        return None
    queries = []
    for src in QUERIES.get(lang, []):
        try:
            queries.append(ts.Query(language, src))
        except Exception as e:  # pattern not supported by this grammar version
            log.warning("skipping %s query %r: %s", lang, src[:60], e)
    return language, queries


@cache
def _parser(lang: str) -> ts.Parser:
    return tslp.get_parser(lang)  # type: ignore[arg-type]


_VUE_SCRIPT = re.compile(rb"<script\b([^>]*)>(.*?)</script>", re.S | re.I)


def _text(node: ts.Node) -> str:
    return (node.text or b"").decode("utf-8", "replace")


def _signature(node: ts.Node) -> str:
    text = _text(node)
    first = text.split("\n", 1)[0]
    for stop in (" {", "{"):
        if stop in first:
            first = first[: first.index(stop)]
            break
    return first.strip()[:300]


def _doc(node: ts.Node, lang: str) -> str:
    # Python: docstring is the first statement of the body.
    if lang == "python":
        body = node.child_by_field_name("body")
        if body is not None and body.named_child_count:
            first = body.named_children[0]
            # Grammar versions differ: a bare string, or one wrapped in an expression statement.
            if first.type == "expression_statement" and first.named_child_count:
                first = first.named_children[0]
            if first.type == "string":
                return _text(first).strip("\"' \n")[:1000]
        return ""
    # Others: comments directly above (adjacent lines).
    lines: list[str] = []
    target = node
    prev = node.prev_named_sibling
    # The comment sits above the enclosing declaration (export statement, Go
    # type/const declaration, TS variable declaration, decorated definition).
    while prev is None and target.parent is not None and target.parent.type in WRAPPERS:
        target = target.parent
        prev = target.prev_named_sibling
    while (
        prev is not None
        and prev.type in ("comment", "line_comment", "block_comment")
        and prev.end_point[0] >= target.start_point[0] - 1
    ):
        lines.insert(0, _text(prev))
        target = prev
        prev = prev.prev_named_sibling
    doc = "\n".join(lines)
    doc = re.sub(r"\s*\*/\s*$", "", doc, flags=re.M)
    doc = re.sub(r"^\s*(///?|/\*\*?|\*|#)\s?", "", doc, flags=re.M)
    return doc.strip()[:1000]


def _parent_name(node: ts.Node) -> str:
    p = node.parent
    while p is not None:
        if p.type in PARENT_TYPES:
            if p.type == "class_body":
                p = p.parent
                continue
            if p.type == "impl_item":
                t = p.child_by_field_name("type")
                return _text(t) if t is not None else ""
            n = p.child_by_field_name("name")
            if n is not None:
                return _text(n)
        p = p.parent
    return ""


def _receiver(node: ts.Node) -> str:
    rec = node.child_by_field_name("receiver")
    if rec is None:
        return ""
    m = re.search(r"\*?\s*([A-Za-z_][A-Za-z0-9_]*)\s*(\[.*\])?\s*\)\s*$", _text(rec))
    return m.group(1) if m else ""


def _exported(node: ts.Node, name: str, lang: str) -> bool:
    if lang == "go":
        return name[:1].isupper()
    if lang == "python":
        return not name.startswith("_")
    if lang == "rust":
        return any(c.type == "visibility_modifier" for c in node.children)
    if lang == "java":
        mods = next((c for c in node.children if c.type == "modifiers"), None)
        return mods is not None and "public" in _text(mods)
    p = node.parent
    while p is not None and p.type not in ("program",):
        if p.type == "export_statement":
            return True
        p = p.parent
    return False


def _kind_for(lang: str, kind: str, node: ts.Node, name: str, parent: str) -> str:
    if lang == "go" and kind == "type":
        t = node.child_by_field_name("type")
        if t is not None and t.type == "struct_type":
            return "struct"
        if t is not None and t.type == "interface_type":
            return "interface"
    if kind == "function" and parent:
        return "method"
    if kind == "function" and lang in ("tsx", "javascript") and name[:1].isupper():
        return "component"
    return kind


def parse(path: str, source: bytes) -> Parsed:
    """Symbols and imports of a source file ('' language → nothing)."""
    lang = language_for(path)
    if not lang:
        return Parsed()
    offset = 0
    if lang == "vue":
        m = _VUE_SCRIPT.search(source)
        if not m:
            return Parsed()
        attrs, body = m.group(1), m.group(2)
        offset = source[: m.start(2)].count(b"\n")
        lang = "tsx" if b"tsx" in attrs else "typescript" if b"ts" in attrs else "javascript"
        source = body
    compiled = _compiled(lang)
    if compiled is None:
        return Parsed()
    _, queries = compiled
    tree = _parser(lang).parse(source)
    out = Parsed()
    seen: set[tuple[int, int, str]] = set()
    for q in queries:
        for _, caps in ts.QueryCursor(q).matches(tree.root_node):
            if "import" in caps:
                for n in caps["import"]:
                    imp = _text(n).strip("\"'` ")
                    if imp and imp not in out.imports:
                        out.imports.append(imp[:300])
                continue
            kind = next((k[4:] for k in caps if k.startswith("def.")), None)
            if kind is None or "name" not in caps:
                continue
            node = caps[f"def.{kind}"][0]
            name = _text(caps["name"][0])
            key = (node.start_byte, node.end_byte, name)
            if key in seen or not name:
                continue
            seen.add(key)
            parent = _receiver(node) if lang == "go" and kind == "method" else _parent_name(node)
            out.symbols.append(
                Symbol(
                    name=name[:200],
                    kind=_kind_for(lang, kind, node, name, parent),
                    start_line=node.start_point[0] + 1 + offset,
                    end_line=node.end_point[0] + 1 + offset,
                    signature=_signature(node),
                    parent=parent[:200],
                    exported=_exported(node, name, lang),
                    doc=_doc(node, lang),
                )
            )
    out.symbols.sort(key=lambda s: (s.start_line, -s.end_line))
    out.imports = out.imports[:500]
    return out
