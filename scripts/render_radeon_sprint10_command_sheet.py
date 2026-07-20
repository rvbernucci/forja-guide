#!/usr/bin/env python3
"""Render a safe operator command sheet for the Sprint 10 Radeon evidence run."""

from __future__ import annotations

import argparse
from pathlib import Path


DEFAULT_HOST = "<radeon-host>"
DEFAULT_PORT = "<radeon-port>"
DEFAULT_BRANCH = "feat/sprint-10-radeon-runtime-v2"
DEFAULT_REPO = "https://github.com/rvbernucci/forja-guide"


def render_sheet(
    *,
    host: str,
    port: str,
    repo_url: str,
    branch: str,
    repo_dir: str,
    evidence_dir: str,
    snapshot_root: str,
) -> str:
    return f"""# Sprint 10 Radeon Evidence Command Sheet

Status: private operator aid. This sheet contains no credentials, model
weights, tokens, source bodies, or private receipts. Sprint 10 is not closed by
running these commands; closure still requires public-summary ingestion,
independent immutable review, and a v2 close receipt.

## 1. Wait For SSH

Run from the workstation:

```bash
python3 scripts/preflight_radeon_ssh.py {host} {port} \\
  --timeout-seconds 180 \\
  --interval-seconds 10 \\
  --probe-timeout-seconds 8 \\
  --wait-output /tmp/forja-radeon-ssh-wait.json \\
  --recovery-output /tmp/forja-radeon-ssh-recovery.md \\
  --output /tmp/forja-radeon-ssh-preflight.json
```

Proceed only when the preflight report says `"ready": true`. If it returns
`"connected_no_banner"`, `"refused"`, `"timeout"`, `"unreachable"`, or
`"unexpected_banner"`, follow the report's `next_action` and `operator_hints`
from `/tmp/forja-radeon-ssh-wait.json` before attempting `ssh`, `scp`, or
evidence collection. For `"connected_no_banner"`, open
`/tmp/forja-radeon-ssh-recovery.md` and follow the web-terminal repair steps.

The lower-level wait and recovery commands remain available for manual
debugging:

```bash
python3 scripts/wait_radeon_ssh.py {host} {port} \\
  --timeout-seconds 180 \\
  --interval-seconds 10 \\
  --probe-timeout-seconds 8 \\
  --output /tmp/forja-radeon-ssh-wait.json

python3 scripts/render_radeon_ssh_recovery_sheet.py \\
  --wait-report /tmp/forja-radeon-ssh-wait.json \\
  --host {host} \\
  --port {port} \\
  --output /tmp/forja-radeon-ssh-recovery.md
```

## 2. Prepare Repository On Radeon

Run on the Radeon instance:

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

## 3. Generate And Fill Private Operator Bundle

Run on the Radeon instance:

```bash
python3 scripts/prepare_radeon_sprint10_operator_bundle.py
python3 scripts/verify_radeon_operator_bundle.py \\
  --bundle-dir /workspace/forja-alpha-sprint10-operator-bundle \\
  --allow-placeholders
```

Then edit the private files outside Git:

```bash
cp /workspace/forja-alpha-sprint10-operator-bundle/radeon-model-candidates.template.json \\
  {snapshot_root}/radeon-model-candidates.json
```

Replace every local model ID, embedding model ID, and quantization placeholder.

## 4. Start Local Endpoints

Start two local instruction endpoints and one local embedding endpoint on
loopback only. The default evidence bundle expects:

```text
instruction candidate A: http://127.0.0.1:8000/v1
instruction candidate B: http://127.0.0.1:8001/v1
embedding endpoint:      http://127.0.0.1:8081/v1
```

## 5. Fail-Fast Preflights

Run on the Radeon instance:

```bash
python3 scripts/verify_radeon_operator_bundle.py \\
  --bundle-dir /workspace/forja-alpha-sprint10-operator-bundle

python3 scripts/check_radeon_sprint10_private_inputs.py \\
  --snapshot-root {snapshot_root} \\
  --model-candidates {snapshot_root}/radeon-model-candidates.json \\
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \\
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \\
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL" \\
  --output /workspace/forja-radeon-private-input-preflight.json
```

Do not run the evidence sequence until both preflights pass.

## 6. Run Sprint 10 Evidence

Run on the Radeon instance:

```bash
{repo_dir}/../forja-alpha-sprint10-operator-bundle/run-sprint10-evidence.sh
```

If the bundle lives at the default path, this is equivalent to:

```bash
/workspace/forja-alpha-sprint10-operator-bundle/run-sprint10-evidence.sh
```

The evidence runner writes private artifacts under `{evidence_dir}` and creates
`{evidence_dir}/radeon-public-summary.json`.

Diagnose the artifact set before copying anything back:

```bash
python3 scripts/diagnose_radeon_sprint10_artifacts.py \\
  --evidence-dir {evidence_dir} \\
  --output /workspace/forja-alpha-sprint10-artifact-diagnosis.json
```

Proceed only when the diagnosis reports
`"stage": "ready_to_ingest_public_summary"`. If it reports any
`"blocked_at_*"` stage, inspect `"next_command"` and run that command on the
Radeon instance after fixing the referenced prerequisite.

## 7. Bring Back Only The Public Summary

Copy only this public-safe file back to the repository workstation:

```text
{evidence_dir}/radeon-public-summary.json
```

Do not commit raw runtime receipts, logs, model outputs, vectors, source bodies,
tokens, credentials, model weights, caches, or private candidate files.

## 8. Ingest Public Summary

Run from the repository workstation after copying the public summary:

```bash
python3 scripts/ingest_radeon_sprint10_public_summary.py \\
  --summary /path/to/radeon-public-summary.json \\
  --output /tmp/forja-alpha-sprint10-public-ingest.json

python3 scripts/verify_sprint10_review_readiness.py \\
  --evidence-dir docs/evidence/sprint-10
```

The expected state after this step is `ready_for_independent_review: true` and
`next_sprint_authorized: false`. Sprint 11 remains blocked until independent
review promotes the candidate to a close receipt.
"""


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default=DEFAULT_HOST)
    parser.add_argument("--port", default=DEFAULT_PORT)
    parser.add_argument("--repo-url", default=DEFAULT_REPO)
    parser.add_argument("--branch", default=DEFAULT_BRANCH)
    parser.add_argument("--repo-dir", default="/workspace/forja-guide")
    parser.add_argument("--evidence-dir", default="/workspace/forja-alpha-sprint10-evidence")
    parser.add_argument("--snapshot-root", default="/secure/forja")
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    body = render_sheet(
        host=args.host,
        port=args.port,
        repo_url=args.repo_url,
        branch=args.branch,
        repo_dir=args.repo_dir,
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
