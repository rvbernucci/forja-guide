#!/usr/bin/env python3
"""Validate public repository structure without third-party dependencies."""

from __future__ import annotations

import hashlib
import json
import os
import re
import subprocess
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

CLOSURE_CANDIDATE_FILE = "closure-candidate.json"

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
COMMIT_SHA = re.compile(r"^[a-f0-9]{40}$")
SHA256 = re.compile(r"^[a-f0-9]{64}$")

SPRINT_ROADMAP_RANGES = (
    (0, 4, "docs/04-roadmap/SPRINTS_00_04_FOUNDATION.md"),
    (5, 9, "docs/04-roadmap/SPRINTS_05_09_INTELLIGENCE.md"),
    (10, 14, "docs/04-roadmap/SPRINTS_10_14_PRODUCTION.md"),
)


def sprint_roadmap_path(sprint_id: str) -> str | None:
    """Return the detailed roadmap that owns a canonical numeric Sprint ID."""
    if not sprint_id.isdigit():
        return None
    sprint_number = int(sprint_id)
    for first, last, roadmap in SPRINT_ROADMAP_RANGES:
        if first <= sprint_number <= last:
            return roadmap
    return None


def attestation_matches_trusted_main(
    candidate_commit: str,
    attestation_commit: str,
) -> bool:
    """Bind protected CI to its immutable PR base or published main head."""
    if os.environ.get("FORJA_ENFORCE_TRUSTED_MAIN") != "1":
        return True
    trusted_main = os.environ.get("FORJA_TRUSTED_MAIN_SHA", "")
    if COMMIT_SHA.fullmatch(trusted_main) is None:
        return False
    if candidate_commit == trusted_main:
        return True
    result = subprocess.run(
        [
            "git",
            "-C",
            str(ROOT),
            "merge-base",
            "--is-ancestor",
            attestation_commit,
            trusted_main,
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    return result.returncode == 0


def files_with_suffix(suffix: str) -> list[Path]:
    """Return repository files with a suffix, excluding generated directories."""
    return [
        path
        for path in ROOT.rglob(f"*{suffix}")
        if not any(part in SKIPPED_DIRECTORIES for part in path.parts)
    ]


def validate_required_files(errors: list[str]) -> None:
    """Require the public governance and architecture entry points."""
    for relative in REQUIRED_FILES:
        if not (ROOT / relative).is_file():
            errors.append(f"missing required file: {relative}")


def validate_json(errors: list[str]) -> None:
    """Ensure every tracked JSON document can be parsed."""
    for path in files_with_suffix(".json"):
        try:
            json.loads(path.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
            errors.append(f"invalid JSON: {path.relative_to(ROOT)}: {exc}")


def validate_evidence_sets(errors: list[str]) -> None:
    """Validate the mandatory, versioned evidence package for every Sprint."""
    evidence_root = ROOT / "docs" / "evidence"
    if not evidence_root.exists():
        return

    for sprint_dir in sorted(evidence_root.glob("sprint-*")):
        if not sprint_dir.is_dir():
            continue

        expected_sprint_id = sprint_dir.name.removeprefix("sprint-")
        requires_v2 = (
            expected_sprint_id.isdigit() and int(expected_sprint_id) >= 3
        )
        close_path = sprint_dir / "close-receipt.json"
        candidate_path = sprint_dir / CLOSURE_CANDIDATE_FILE
        closure_paths = [
            path for path in (close_path, candidate_path) if path.is_file()
        ]
        if not closure_paths:
            errors.append(
                f"incomplete Sprint evidence set: {sprint_dir.relative_to(ROOT)} "
                "is missing a closure receipt or candidate"
            )
        elif len(closure_paths) > 1:
            errors.append(
                f"ambiguous Sprint closure state: {sprint_dir.relative_to(ROOT)} "
                "contains both a receipt and a candidate"
            )

        document_paths = [
            sprint_dir / filename for filename in EVIDENCE_FILES[:-1]
        ]
        document_paths.extend(closure_paths)
        for path in document_paths:
            if not path.is_file():
                errors.append(
                    f"incomplete Sprint evidence set: {sprint_dir.relative_to(ROOT)} "
                    f"is missing {path.name}"
                )
                continue

            try:
                payload = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, UnicodeDecodeError, json.JSONDecodeError):
                continue

            if not isinstance(payload, dict):
                errors.append(
                    f"evidence document must be a JSON object: "
                    f"{path.relative_to(ROOT)}"
                )
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
            basis_commit = payload.get("basis_commit")
            if basis_commit is not None:
                if not isinstance(basis_commit, str) or not COMMIT_SHA.fullmatch(
                    basis_commit
                ):
                    errors.append(
                        f"invalid basis_commit in {path.relative_to(ROOT)}"
                    )
                elif (ROOT / ".git").exists():
                    result = subprocess.run(
                        [
                            "git",
                            "-C",
                            str(ROOT),
                            "cat-file",
                            "-e",
                            f"{basis_commit}^{{commit}}",
                        ],
                        check=False,
                        capture_output=True,
                        text=True,
                    )
                    if result.returncode != 0:
                        errors.append(
                            f"unresolvable basis_commit in "
                            f"{path.relative_to(ROOT)}: {basis_commit}"
                        )
            validate_artifact_references(payload, path, errors)

        if close_path.is_file():
            try:
                close_receipt = json.loads(close_path.read_text(encoding="utf-8"))
            except (OSError, UnicodeDecodeError, json.JSONDecodeError):
                continue
            if not isinstance(close_receipt, dict):
                errors.append(
                    f"evidence document must be a JSON object: "
                    f"{close_path.relative_to(ROOT)}"
                )
                continue
            if close_receipt.get("status") != "closed":
                errors.append(
                    f"Sprint close receipt is not closed: "
                    f"{close_path.relative_to(ROOT)}"
                )
            protocol_version = close_receipt.get("closure_protocol_version")
            if requires_v2 and protocol_version != "2.0":
                errors.append(
                    f"Sprint close receipt requires closure protocol 2.0: "
                    f"{close_path.relative_to(ROOT)}"
                )
            elif protocol_version not in (None, "2.0"):
                errors.append(
                    f"unsupported Sprint closure protocol: "
                    f"{close_path.relative_to(ROOT)}"
                )
            elif protocol_version == "2.0":
                validate_v2_close_receipt(
                    close_receipt,
                    close_path,
                    errors,
                )
        if candidate_path.is_file():
            try:
                candidate = json.loads(candidate_path.read_text(encoding="utf-8"))
            except (OSError, UnicodeDecodeError, json.JSONDecodeError):
                continue
            if not isinstance(candidate, dict):
                continue
            if (
                candidate.get("status") != "candidate"
                or candidate.get("authoritative") is not False
                or candidate.get("next_sprint_authorized") is not None
                or candidate.get("closure_protocol_version") != "2.0"
            ):
                errors.append(
                    f"Sprint closure candidate is not fail-closed: "
                    f"{candidate_path.relative_to(ROOT)}"
                )


def validate_v2_close_receipt(
    receipt: dict[str, object],
    path: Path,
    errors: list[str],
) -> None:
    """Require immutable review binding before a v2 receipt authorizes work."""
    label = path.relative_to(ROOT)
    candidate_commit = receipt.get("reviewed_candidate_commit")
    review = receipt.get("immutable_review")
    next_sprint = receipt.get("next_sprint_authorized")
    closed_at = receipt.get("closed_at")
    valid = (
        receipt.get("authoritative") is True
        and isinstance(candidate_commit, str)
        and COMMIT_SHA.fullmatch(candidate_commit) is not None
        and isinstance(review, dict)
        and review.get("result") == "passed"
        and review.get("reviewed_commit") == candidate_commit
        and isinstance(review.get("artifact_path"), str)
        and isinstance(review.get("artifact_sha256"), str)
        and isinstance(next_sprint, str)
        and bool(next_sprint.strip())
        and isinstance(closed_at, str)
        and bool(closed_at.strip())
    )
    if not valid:
        errors.append(f"Sprint v2 close receipt is not review-bound: {label}")
        return
    if (ROOT / ".git").exists():
        candidate_result = subprocess.run(
            [
                "git",
                "-C",
                str(ROOT),
                "cat-file",
                "-e",
                f"{candidate_commit}^{{commit}}",
            ],
            check=False,
            capture_output=True,
            text=True,
        )
        if candidate_result.returncode != 0:
            errors.append(
                f"unresolvable reviewed_candidate_commit in {label}: "
                f"{candidate_commit}"
            )
            return
        candidate_path = path.parent / CLOSURE_CANDIDATE_FILE
        candidate_relative = candidate_path.relative_to(ROOT).as_posix()
        candidate_document = subprocess.run(
            [
                "git",
                "-C",
                str(ROOT),
                "show",
                f"{candidate_commit}:{candidate_relative}",
            ],
            check=False,
            capture_output=True,
            text=True,
        )
        try:
            candidate = json.loads(candidate_document.stdout)
        except json.JSONDecodeError:
            candidate = None
        if (
            candidate_document.returncode != 0
            or not isinstance(candidate, dict)
            or candidate.get("status") != "candidate"
            or candidate.get("authoritative") is not False
            or candidate.get("next_sprint_authorized") is not None
            or candidate.get("closure_protocol_version") != "2.0"
        ):
            errors.append(
                f"reviewed commit lacks a fail-closed closure candidate: {label}"
            )
            return

        receipt_relative = path.relative_to(ROOT).as_posix()
        introduction = subprocess.run(
            [
                "git",
                "-C",
                str(ROOT),
                "log",
                "--diff-filter=A",
                "--no-renames",
                "--format=%H",
                "--",
                receipt_relative,
            ],
            check=False,
            capture_output=True,
            text=True,
        )
        commits = introduction.stdout.splitlines()
        if introduction.returncode != 0 or not commits:
            errors.append(f"cannot resolve v2 attestation commit: {label}")
            return
        attestation_commit = commits[0]
        if not attestation_matches_trusted_main(
            candidate_commit,
            attestation_commit,
        ):
            errors.append(
                f"v2 attestation is not based on trusted main: {label}"
            )
            return
        parent = subprocess.run(
            [
                "git",
                "-C",
                str(ROOT),
                "rev-parse",
                f"{attestation_commit}^",
            ],
            check=False,
            capture_output=True,
            text=True,
        )
        if parent.returncode != 0 or parent.stdout.strip() != candidate_commit:
            errors.append(
                f"v2 attestation is not a direct child of its candidate: {label}"
            )
            return

        review_path = review["artifact_path"]
        review_prefix = (path.parent / "reviews").relative_to(ROOT).as_posix() + "/"
        if not review_path.startswith(review_prefix):
            errors.append(f"v2 review artifact is outside Sprint evidence: {label}")
            return
        sprint_id = path.parent.name.removeprefix("sprint-")
        detailed_roadmap = sprint_roadmap_path(sprint_id)
        if detailed_roadmap is None:
            errors.append(f"v2 Sprint has no declared roadmap range: {label}")
            return
        changed = subprocess.run(
            [
                "git",
                "-C",
                str(ROOT),
                "diff",
                "--name-only",
                "--no-renames",
                candidate_commit,
                attestation_commit,
            ],
            check=False,
            capture_output=True,
            text=True,
        )
        expected_paths = {
            candidate_relative,
            receipt_relative,
            review_path,
            "docs/04-roadmap/MASTER_DEVELOPMENT_PLAN.md",
            detailed_roadmap,
        }
        if changed.returncode != 0 or set(changed.stdout.splitlines()) != expected_paths:
            errors.append(f"v2 attestation contains non-promotion changes: {label}")
            return

        committed_receipt = subprocess.run(
            [
                "git",
                "-C",
                str(ROOT),
                "show",
                f"{attestation_commit}:{receipt_relative}",
            ],
            check=False,
            capture_output=True,
        )
        if (
            committed_receipt.returncode != 0
            or committed_receipt.stdout != path.read_bytes()
        ):
            errors.append(f"v2 close receipt changed after attestation: {label}")


def validate_artifact_references(
    value: object,
    evidence_path: Path,
    errors: list[str],
) -> None:
    """Verify every artifact_path and artifact_sha256 pair recursively."""
    if isinstance(value, list):
        for item in value:
            validate_artifact_references(item, evidence_path, errors)
        return
    if not isinstance(value, dict):
        return

    artifact_path = value.get("artifact_path")
    artifact_sha256 = value.get("artifact_sha256")
    if artifact_path is not None or artifact_sha256 is not None:
        label = evidence_path.relative_to(ROOT)
        if not isinstance(artifact_path, str) or not artifact_path:
            errors.append(f"invalid artifact_path in {label}")
        elif not isinstance(artifact_sha256, str) or not SHA256.fullmatch(
            artifact_sha256
        ):
            errors.append(f"invalid artifact_sha256 in {label}")
        else:
            candidate = (ROOT / artifact_path).resolve()
            root = ROOT.resolve()
            if Path(artifact_path).is_absolute() or not candidate.is_relative_to(root):
                errors.append(f"artifact_path escapes repository in {label}")
            elif not candidate.is_file():
                errors.append(f"missing evidence artifact in {label}: {artifact_path}")
            else:
                digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
                if digest != artifact_sha256:
                    errors.append(
                        f"evidence artifact digest mismatch in {label}: "
                        f"{artifact_path}"
                    )

    for item in value.values():
        validate_artifact_references(item, evidence_path, errors)


def validate_sensitive_content(errors: list[str]) -> None:
    """Reject common credentials and machine-specific paths in public files."""
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
    """Remove URL query and fragment components from an internal link."""
    return target.split("#", maxsplit=1)[0].split("?", maxsplit=1)[0]


def validate_markdown_links(errors: list[str]) -> None:
    """Check that relative Markdown links resolve inside the repository."""
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
    """Run all repository quality gates and return a process exit code."""
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
