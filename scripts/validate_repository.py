#!/usr/bin/env python3
"""Validate public repository structure without third-party dependencies."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path
from urllib.parse import unquote


ROOT = Path(__file__).resolve().parents[1]

REQUIRED_FILES = (
    "README.md",
    "ROADMAP.md",
    "LICENSE",
    "SECURITY.md",
    "CONTRIBUTING.md",
    "GOVERNANCE.md",
    "AGENTS.md",
    "docs/README.md",
    "docs/02-architecture/SYSTEM_ARCHITECTURE.md",
    "docs/02-architecture/DATA_ARCHITECTURE.md",
    "docs/04-roadmap/MASTER_DEVELOPMENT_PLAN.md",
)

EVIDENCE_FILES = (
    "plan.json",
    "test-report.json",
    "validation-report.json",
    "security-report.json",
    "rollback-report.json",
    "metrics-summary.json",
    "close-receipt.json",
)

SKIPPED_DIRECTORIES = {
    ".git",
    ".cache",
    ".tmp",
    "node_modules",
    "vendor",
}

FORBIDDEN_PATTERNS = {
    "private user path": re.compile(r"/Users/[A-Za-z0-9._-]+/"),
    "GitHub token": re.compile(r"\bgh[opsu]_[A-Za-z0-9]{20,}\b"),
    "Hugging Face token": re.compile(r"\bhf_[A-Za-z0-9]{20,}\b"),
    "private key": re.compile(
        "-----BEGIN " + r"(?:RSA |EC |OPENSSH )?" + "PRIVATE KEY-----"
    ),
    "database credential URL": re.compile(
        r"\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?)://[^/\s:@]+:[^@\s]+@",
        re.IGNORECASE,
    ),
}

MARKDOWN_LINK = re.compile(r"!?\[[^\]]*\]\(([^)]+)\)")


def files_with_suffix(suffix: str) -> list[Path]:
    return [
        path
        for path in ROOT.rglob(f"*{suffix}")
        if not any(part in SKIPPED_DIRECTORIES for part in path.parts)
    ]


def validate_required_files(errors: list[str]) -> None:
    for relative in REQUIRED_FILES:
        if not (ROOT / relative).is_file():
            errors.append(f"missing required file: {relative}")


def validate_json(errors: list[str]) -> None:
    for path in files_with_suffix(".json"):
        try:
            json.loads(path.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
            errors.append(f"invalid JSON: {path.relative_to(ROOT)}: {exc}")


def validate_evidence_sets(errors: list[str]) -> None:
    evidence_root = ROOT / "docs" / "evidence"
    if not evidence_root.exists():
        return

    for sprint_dir in sorted(evidence_root.glob("sprint-*")):
        if not sprint_dir.is_dir():
            continue

        expected_sprint_id = sprint_dir.name.removeprefix("sprint-")
        for filename in EVIDENCE_FILES:
            path = sprint_dir / filename
            if not path.is_file():
                errors.append(
                    f"incomplete Sprint evidence set: {sprint_dir.relative_to(ROOT)} "
                    f"is missing {filename}"
                )
                continue

            try:
                payload = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, UnicodeDecodeError, json.JSONDecodeError):
                continue

            if payload.get("evidence_version") != "1.0":
                errors.append(
                    f"invalid evidence_version in {path.relative_to(ROOT)}"
                )
            if payload.get("sprint_id") != expected_sprint_id:
                errors.append(
                    f"sprint_id mismatch in {path.relative_to(ROOT)}: "
                    f"expected {expected_sprint_id}"
                )

        close_path = sprint_dir / "close-receipt.json"
        if close_path.is_file():
            try:
                close_receipt = json.loads(close_path.read_text(encoding="utf-8"))
            except (OSError, UnicodeDecodeError, json.JSONDecodeError):
                continue
            if close_receipt.get("status") != "closed":
                errors.append(
                    f"Sprint close receipt is not closed: "
                    f"{close_path.relative_to(ROOT)}"
                )


def validate_sensitive_content(errors: list[str]) -> None:
    text_suffixes = {".md", ".json", ".yaml", ".yml", ".py", ".go", ".ts"}
    for path in ROOT.rglob("*"):
        if (
            not path.is_file()
            or path.suffix not in text_suffixes
            or any(part in SKIPPED_DIRECTORIES for part in path.parts)
        ):
            continue
        text = path.read_text(encoding="utf-8", errors="replace")
        for label, pattern in FORBIDDEN_PATTERNS.items():
            if pattern.search(text):
                errors.append(
                    f"{label} found in public file: {path.relative_to(ROOT)}"
                )


def strip_fragment(target: str) -> str:
    return target.split("#", maxsplit=1)[0].split("?", maxsplit=1)[0]


def validate_markdown_links(errors: list[str]) -> None:
    for path in files_with_suffix(".md"):
        text = path.read_text(encoding="utf-8")
        for raw_target in MARKDOWN_LINK.findall(text):
            target = raw_target.strip().strip("<>")
            if (
                not target
                or target.startswith(("http://", "https://", "mailto:", "#"))
            ):
                continue
            normalized = unquote(strip_fragment(target))
            resolved = (path.parent / normalized).resolve()
            try:
                resolved.relative_to(ROOT)
            except ValueError:
                errors.append(
                    f"link escapes repository: {path.relative_to(ROOT)} -> {target}"
                )
                continue
            if not resolved.exists():
                errors.append(
                    f"broken internal link: {path.relative_to(ROOT)} -> {target}"
                )


def main() -> int:
    errors: list[str] = []
    validate_required_files(errors)
    validate_json(errors)
    validate_evidence_sets(errors)
    validate_sensitive_content(errors)
    validate_markdown_links(errors)

    if errors:
        print("Repository validation failed:")
        for error in sorted(set(errors)):
            print(f"- {error}")
        return 1

    markdown_count = len(files_with_suffix(".md"))
    schema_count = len(files_with_suffix(".schema.json"))
    print(
        "Repository validation passed: "
        f"{markdown_count} Markdown files, {schema_count} JSON schemas."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
