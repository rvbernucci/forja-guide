#!/usr/bin/env python3
"""Render a public-safe Sprint 10 fallback sheet for the Radeon web terminal."""

from __future__ import annotations

import argparse
from collections.abc import Sequence
from pathlib import Path


DEFAULT_BRANCH = "feat/sprint-10-radeon-runtime-v2"
DEFAULT_REPO = "https://github.com/rvbernucci/forja-guide"
DEFAULT_REPO_DIR = "/workspace/forja-guide"
DEFAULT_BUNDLE_DIR = "/workspace/forja-alpha-sprint10-operator-bundle"
DEFAULT_EVIDENCE_DIR = "/workspace/forja-alpha-sprint10-evidence"
DEFAULT_SNAPSHOT_ROOT = "/secure/forja"


def render_sheet(
    *,
    repo_url: str,
    branch: str,
    repo_dir: str,
    bundle_dir: str,
    evidence_dir: str,
    snapshot_root: str,
) -> str:
    return f"""# Sprint 10 Radeon Web-Terminal Evidence Sheet

Status: public-safe fallback path. Use this sheet when the Radeon web terminal
works but SSH from the workstation is not ready. It contains no credentials,
private receipts, raw model outputs, source bodies, tokens, or model weights.
Running these commands does not close Sprint 10; closure still requires public
summary ingestion, immutable review, and a v2 close receipt.

## 1. Prepare The Repository

Run inside the Radeon Jupyter/OpenCode terminal:

```bash
mkdir -p {repo_dir}
cd {repo_dir}
if [ ! -d .git ]; then
  git clone {repo_url} .
fi
git fetch origin
git checkout {branch}
git pull --ff-only origin {branch}
python3 scripts/validate_repository.py
```

## 2. Generate The Private Operator Bundle

```bash
python3 scripts/prepare_radeon_sprint10_operator_bundle.py \\
  --bundle-dir {bundle_dir}

python3 scripts/verify_radeon_operator_bundle.py \\
  --bundle-dir {bundle_dir} \\
  --allow-placeholders
```

The generated templates are private operator inputs. Keep filled files outside
Git under `{snapshot_root}`.

## 3. Fill Private Inputs Outside Git

```bash
mkdir -p {snapshot_root}
cp {bundle_dir}/radeon-model-candidates.template.json \\
  {snapshot_root}/radeon-model-candidates.json
```

Then edit `{snapshot_root}/radeon-model-candidates.json` and replace every
placeholder with real loopback model endpoints, model IDs, and quantization
notes. Also place the required source snapshots under `{snapshot_root}`.

## 4. Start Local Endpoints On Loopback

The default Sprint 10 bundle expects local-only OpenAI-compatible endpoints:

```text
instruction candidate A: http://127.0.0.1:8000/v1
instruction candidate B: http://127.0.0.1:8001/v1
embedding endpoint:      http://127.0.0.1:8081/v1
```

Set the evidence environment in the same terminal:

```bash
export FORJA_ALPHA_MODEL_BASE_URL=http://127.0.0.1:8000/v1
export FORJA_ALPHA_EMBEDDING_BASE_URL=http://127.0.0.1:8081/v1
export FORJA_ALPHA_EMBEDDING_MODEL=<local-embedding-model-id>
export FORJA_ALPHA_ACCELERATOR='AMD Radeon GPU'
export FORJA_ALPHA_SOFTWARE_STACK='ROCm + vLLM'
```

## 5. Run Fail-Fast Preflights

```bash
python3 scripts/verify_radeon_operator_bundle.py \\
  --bundle-dir {bundle_dir}

python3 scripts/check_radeon_sprint10_private_inputs.py \\
  --snapshot-root {snapshot_root} \\
  --model-candidates {snapshot_root}/radeon-model-candidates.json \\
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \\
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \\
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL" \\
  --output /workspace/forja-radeon-private-input-preflight.json
```

Do not run evidence until both preflights pass. Failed preflights are useful:
fix the reported private input or endpoint before spending GPU time.

## 6. Run Evidence From The Web Terminal

```bash
{bundle_dir}/run-sprint10-evidence.sh
```

The evidence runner writes private artifacts under `{evidence_dir}` and a
public-safe summary at:

```text
{evidence_dir}/radeon-public-summary.json
```

Diagnose the artifact set before moving anything out of the instance:

```bash
python3 scripts/diagnose_radeon_sprint10_artifacts.py \\
  --evidence-dir {evidence_dir} \\
  --output /workspace/forja-alpha-sprint10-artifact-diagnosis.json
```

Proceed only when the diagnosis reports
`"stage": "ready_to_ingest_public_summary"`. If it reports a blocked stage,
run the reported `"next_command"` after fixing the prerequisite.

## 7. Export Only The Public Summary

If SSH/SCP is unavailable, download or copy only this file through the notebook
UI:

```text
{evidence_dir}/radeon-public-summary.json
```

Do not export private receipts, logs, model outputs, vectors, source bodies,
model caches, credentials, or candidate files.

## 8. Ingest From The Workstation

After transferring only the public summary to the workstation:

```bash
python3 scripts/verify_radeon_sprint10_public_summary.py \\
  --summary /path/to/radeon-public-summary.json \\
  --output /tmp/forja-alpha-sprint10-public-summary-verify.json

python3 scripts/ingest_radeon_sprint10_public_summary.py \\
  --summary /path/to/radeon-public-summary.json \\
  --dry-run \\
  --output /tmp/forja-alpha-sprint10-public-ingest-dry-run.json

python3 scripts/ingest_radeon_sprint10_public_summary.py \\
  --summary /path/to/radeon-public-summary.json \\
  --output /tmp/forja-alpha-sprint10-public-ingest.json

python3 scripts/verify_sprint10_review_readiness.py \\
  --evidence-dir docs/evidence/sprint-10
```

The target state is `ready_for_independent_review: true` and
`next_sprint_authorized: false`. Sprint 11 remains blocked until independent
review promotes the candidate to a close receipt.
"""


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-url", default=DEFAULT_REPO)
    parser.add_argument("--branch", default=DEFAULT_BRANCH)
    parser.add_argument("--repo-dir", default=DEFAULT_REPO_DIR)
    parser.add_argument("--bundle-dir", default=DEFAULT_BUNDLE_DIR)
    parser.add_argument("--evidence-dir", default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--snapshot-root", default=DEFAULT_SNAPSHOT_ROOT)
    parser.add_argument("--output", type=Path)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    body = render_sheet(
        repo_url=args.repo_url,
        branch=args.branch,
        repo_dir=args.repo_dir,
        bundle_dir=args.bundle_dir,
        evidence_dir=args.evidence_dir,
        snapshot_root=args.snapshot_root,
    )
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        print(body, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
