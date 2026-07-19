#!/usr/bin/env python3
"""Emit deterministic Python AST facts without importing repository code."""

from __future__ import annotations

import ast
import json
import pathlib
import sys


request = json.load(sys.stdin)
root = pathlib.Path(request["root"]).resolve()
files = sorted(str(value).replace("\\", "/") for value in request["files"])
syntax_version = tuple(int(value) for value in request["toolchain_version"].split("."))
if len(syntax_version) != 2 or syntax_version[0] != 3:
    raise ValueError("Python syntax boundary must be a Python 3 major.minor pair")
symbols: list[dict[str, object]] = []
relations: list[dict[str, object]] = []
diagnostics: list[dict[str, object]] = []
symbol_keys: dict[tuple[str, str], str] = {}


def module_name(file_path: str) -> str:
    path = pathlib.PurePosixPath(file_path).with_suffix("")
    parts = list(path.parts)
    if parts and parts[-1] == "__init__":
        parts.pop()
    return ".".join(parts)


local_modules = {
    module_name(value): value
    for value in files
    if pathlib.PurePosixPath(value).suffix in {".py", ".pyi"}
}


def position(node: ast.AST) -> dict[str, object]:
    return {
        "start": {
            "line": node.lineno,
            "column": node.col_offset + 1,
            "offset": node._forja_start,
        },
        "end": {
            "line": getattr(node, "end_lineno", node.lineno),
            "column": getattr(node, "end_col_offset", node.col_offset) + 1,
            "offset": node._forja_end,
        },
    }


def annotate_offsets(tree: ast.AST, body: str) -> None:
    lines = body.splitlines(keepends=True)
    starts = []
    total = 0
    for line in lines:
        starts.append(total)
        total += len(line.encode("utf-8"))
    if not starts:
        starts = [0]
    for node in ast.walk(tree):
        if not hasattr(node, "lineno"):
            continue
        start_line = max(node.lineno - 1, 0)
        end_line = max(getattr(node, "end_lineno", node.lineno) - 1, 0)
        node._forja_start = starts[start_line] + node.col_offset
        end_column = getattr(node, "end_col_offset", node.col_offset)
        node._forja_end = starts[end_line] + end_column


def dotted_name(node: ast.AST) -> str | None:
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        prefix = dotted_name(node.value)
        return f"{prefix}.{node.attr}" if prefix else node.attr
    return None


def signature(node: ast.FunctionDef | ast.AsyncFunctionDef) -> str:
    prefix = "async " if isinstance(node, ast.AsyncFunctionDef) else ""
    returns = f" -> {ast.unparse(node.returns)}" if node.returns else ""
    return f"{prefix}def {node.name}({ast.unparse(node.args)}){returns}"


class Extractor(ast.NodeVisitor):
    def __init__(self, file_path: str) -> None:
        self.file_path = file_path
        self.module = module_name(file_path)
        path = pathlib.PurePosixPath(file_path)
        self.package = self.module if path.stem == "__init__" else self.module.rpartition(".")[0]
        self.scope: list[str] = []
        self.source_keys: list[str] = []

    def resolve_import(self, module: str, level: int = 0, imported_name: str = "") -> str | None:
        if level:
            package = self.package.split(".") if self.package else []
            trim = level - 1
            if trim > len(package):
                return None
            parts = package[: len(package) - trim]
            if module:
                parts.extend(module.split("."))
            resolved = ".".join(parts)
        else:
            resolved = module
        if imported_name and resolved:
            candidate = f"{resolved}.{imported_name}"
            if candidate in local_modules:
                return local_modules[candidate]
        return local_modules.get(resolved)

    def add_symbol(self, node: ast.AST, name: str, kind: str, value_signature: str = "") -> str:
        qualified = ".".join([self.module, *self.scope, name])
        key = f"python:{self.file_path}:{node._forja_start}:{kind}:{qualified}"
        decorators = ".".join(dotted_name(item) or "" for item in getattr(node, "decorator_list", []))
        symbols.append({
            "key": key, "path": self.file_path, "language": "python", "kind": kind,
            "name": name, "qualified_name": qualified, "signature": value_signature,
            "declaration": position(node), "exported": not name.startswith("_"),
            "test": self.file_path.startswith(("test/", "tests/")) or pathlib.PurePosixPath(self.file_path).name.startswith("test_") or name.startswith("test_"),
            "route": any(part in decorators.lower().split(".") for part in ("route", "get", "post", "put", "patch", "delete")),
            "schema": name.lower().endswith(("schema", "model", "dto")) or "dataclass" in decorators.lower(),
        })
        symbol_keys[(self.file_path, qualified)] = key
        return key

    def visit_ClassDef(self, node: ast.ClassDef) -> None:
        key = self.add_symbol(node, node.name, "class", f"class {node.name}")
        for base in node.bases:
            target = dotted_name(base)
            self.add_relation(base, "extends", external_name=target) if target else self.add_relation(base, "extends", unresolved_name=ast.unparse(base))
        self.scope.append(node.name)
        self.source_keys.append(key)
        self.generic_visit(node)
        self.source_keys.pop()
        self.scope.pop()

    def visit_FunctionDef(self, node: ast.FunctionDef) -> None:
        kind = "method" if self.scope else "function"
        key = self.add_symbol(node, node.name, kind, signature(node))
        self.scope.append(node.name)
        self.source_keys.append(key)
        self.generic_visit(node)
        self.source_keys.pop()
        self.scope.pop()

    visit_AsyncFunctionDef = visit_FunctionDef

    def visit_Import(self, node: ast.Import) -> None:
        for alias in node.names:
            target = self.resolve_import(alias.name)
            if target:
                self.add_relation(node, "imports", target_path=target)
            else:
                self.add_relation(node, "imports", external_name=alias.name)

    def visit_ImportFrom(self, node: ast.ImportFrom) -> None:
        module = "." * node.level + (node.module or "")
        targets: set[str] = set()
        externals: set[str] = set()
        for alias in node.names:
            target = self.resolve_import(node.module or "", node.level, alias.name)
            if target:
                targets.add(target)
            else:
                externals.add(f"{module}.{alias.name}" if module else alias.name)
        for target in sorted(targets):
            self.add_relation(node, "imports", target_path=target)
        for external in sorted(externals):
            self.add_relation(node, "imports", external_name=external)

    def visit_Call(self, node: ast.Call) -> None:
        target = dotted_name(node.func)
        self.add_relation(node.func, "calls", external_name=target) if target else self.add_relation(node.func, "calls", unresolved_name=ast.unparse(node.func))
        self.generic_visit(node)

    def visit_Name(self, node: ast.Name) -> None:
        if isinstance(node.ctx, ast.Load) and self.source_keys:
            self.add_relation(node, "references", external_name=node.id)

    def add_relation(
        self,
        node: ast.AST,
        kind: str,
        *,
        target_path: str | None = None,
        external_name: str | None = None,
        unresolved_name: str | None = None,
    ) -> None:
        relation: dict[str, object] = {
            "source_path": self.file_path, "kind": kind,
            "evidence_class": "candidate_static" if kind in {"calls", "references"} else "confirmed_static",
            "locator": position(node),
        }
        if self.source_keys:
            relation["source_key"] = self.source_keys[-1]
        if target_path:
            relation["target_path"] = target_path
        elif external_name:
            relation["external_name"] = external_name
        else:
            relation["unresolved_name"] = (unresolved_name or ast.unparse(node))[:2048]
        relations.append(relation)


for file_path in files:
    if pathlib.PurePosixPath(file_path).suffix not in {".py", ".pyi"}:
        continue
    target = (root / pathlib.PurePosixPath(file_path)).resolve()
    if root not in target.parents:
        raise ValueError("Python source escapes root")
    body = target.read_text(encoding="utf-8")
    try:
        tree = ast.parse(body, filename=file_path, type_comments=True, feature_version=syntax_version)
    except SyntaxError:
        diagnostics.append({"path": file_path, "severity": "error", "code": "python/syntax"})
        continue
    annotate_offsets(tree, body)
    Extractor(file_path).visit(tree)

json.dump(
    {
        "toolchain_version": f"{sys.version_info.major}.{sys.version_info.minor}",
        "symbols": symbols,
        "relations": relations,
        "diagnostics": diagnostics,
    },
    sys.stdout,
    separators=(",", ":"),
    sort_keys=True,
)
