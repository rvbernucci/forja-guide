#!/usr/bin/env python3
"""Render a public-safe checklist for Sprint 10 private source snapshots."""

from __future__ import annotations

import argparse
import importlib.util
import sys
from collections.abc import Mapping, Sequence
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_SNAPSHOT_ROOT = "/secure/forja"
DEFAULT_MODEL_CANDIDATES = "/secure/forja/radeon-model-candidates.json"


def load_private_input_contract() -> Mapping[str, str]:
    path = ROOT / "scripts" / "check_radeon_sprint10_private_inputs.py"
    spec = importlib.util.spec_from_file_location(
        "check_radeon_sprint10_private_inputs_for_snapshot_checklist",
        path,
    )
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    required = getattr(module, "REQUIRED_SNAPSHOTS")
    if not isinstance(required, dict):
        raise RuntimeError("REQUIRED_SNAPSHOTS must be a dict")
    return required


def render_checklist(
    *,
    snapshot_root: str,
    model_candidates: str,
    required_snapshots: Mapping[str, str],
) -> str:
    rows = "\n".join(
        f"| `{family}` | `{snapshot_root}/{logical_path}` | required |"
        for family, logical_path in sorted(required_snapshots.items())
    )
    required_args = " \\\n".join(
        f"  --required-snapshot {family}={logical_path}"
        for family, logical_path in sorted(required_snapshots.items())
    )
    return f"""# Sprint 10 Private Snapshot Checklist

Status: public-safe operator checklist. This file names required private
snapshot paths but contains no source bodies, hashes, credentials, model
outputs, vectors, or private receipts. It does not collect evidence, close
Sprint 10, or authorize Sprint 11.

## Required Private Inputs

| Source family | Expected private path | Requirement |
| --- | --- | --- |
{rows}
| `model_candidates` | `{model_candidates}` | required |

## Preflight Command

Run this inside the Radeon instance before spending GPU time:

```bash
python3 scripts/check_radeon_sprint10_private_inputs.py \\
  --snapshot-root {snapshot_root} \\
  --model-candidates {model_candidates} \\
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \\
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \\
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL" \\
  --output /workspace/forja-radeon-private-input-preflight.json
```

## Source Manifest Command

The evidence runner can build this manifest automatically, but this explicit
command is useful when diagnosing snapshot coverage:

```bash
python3 scripts/build_alpha_snapshot_manifest.py \\
  --snapshot-root {snapshot_root} \\
{required_args} \\
  --output {snapshot_root}/alpha-source-manifest.json
```

## Success Shape

- The private preflight returns `ready_to_run: true`.
- The source manifest has no `missing_required_families`.
- The restore verifier later returns `verified: true`.
- Only the sanitized `radeon-public-summary.json` leaves the Radeon instance.
"""


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot-root", default=DEFAULT_SNAPSHOT_ROOT)
    parser.add_argument("--model-candidates", default=DEFAULT_MODEL_CANDIDATES)
    parser.add_argument("--output", type=Path)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        body = render_checklist(
            snapshot_root=args.snapshot_root,
            model_candidates=args.model_candidates,
            required_snapshots=load_private_input_contract(),
        )
    except RuntimeError as exc:
        print(f"snapshot checklist rejected: {exc}", file=sys.stderr)
        return 2
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        print(body, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
