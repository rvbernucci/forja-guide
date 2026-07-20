#!/usr/bin/env python3
"""Prepare private operator templates for the next Sprint 10 Radeon run."""

from __future__ import annotations

import argparse
import json
import stat
import sys
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

DEFAULT_OUTPUT_DIR = Path("/workspace/forja-alpha-sprint10-operator-bundle")
LOOPBACK_HOSTS = {"127.0.0.1", "localhost", "::1"}


def is_loopback_url(value: str) -> bool:
    parsed = urlparse(value)
    return parsed.scheme in {"http", "https"} and parsed.hostname in LOOPBACK_HOSTS


def ensure_loopback(name: str, value: str) -> None:
    if not is_loopback_url(value):
        raise ValueError(f"{name} must be an HTTP loopback URL: {value}")


def write_private(path: Path, body: str, executable: bool = False) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body, encoding="utf-8")
    path.chmod(0o700 if executable else 0o600)


def candidate_template(
    *,
    model_base_url: str,
    second_model_base_url: str,
) -> dict[str, Any]:
    return {
        "schema_version": "1.0",
        "candidate_set_id": "radeon-alpha-v1",
        "notes": "Private template. Replace model placeholders with local Radeon-served IDs before benchmarking.",
        "candidates": [
            {
                "candidate_id": "candidate-a",
                "base_url": model_base_url,
                "model": "<local-instruction-model-a>",
                "server": "vllm",
                "quantization": "<precision-or-quantization>",
                "expected_context_tokens": 8192,
            },
            {
                "candidate_id": "candidate-b",
                "base_url": second_model_base_url,
                "model": "<local-instruction-model-b>",
                "server": "vllm",
                "quantization": "<precision-or-quantization>",
                "expected_context_tokens": 8192,
            },
        ],
    }


def env_template(
    *,
    model_base_url: str,
    embedding_base_url: str,
    embedding_model: str,
) -> str:
    return f"""#!/usr/bin/env bash
set -euo pipefail

export FORJA_ALPHA_MODEL_BASE_URL={model_base_url!r}
export FORJA_ALPHA_EMBEDDING_BASE_URL={embedding_base_url!r}
export FORJA_ALPHA_EMBEDDING_MODEL={embedding_model!r}
export FORJA_ALPHA_ACCELERATOR='AMD Radeon GPU'
export FORJA_ALPHA_SOFTWARE_STACK='ROCm + vLLM'
"""


def commands_template() -> str:
    return """#!/usr/bin/env bash
set -euo pipefail

cd "${FORJA_REPO:-/workspace/forja-guide}"
. /workspace/forja-alpha-sprint10-operator-bundle/sprint10-env.template.sh

python3 scripts/capture_radeon_runtime_receipt.py \
  --output /workspace/forja-radeon-runtime-receipt.json

python3 scripts/verify_radeon_runtime_readiness.py \
  --receipt /workspace/forja-radeon-runtime-receipt.json \
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL" \
  --require-endpoints \
  --output /workspace/forja-radeon-runtime-readiness.json

python3 scripts/run_radeon_sprint10_evidence.py \
  --evidence-dir /workspace/forja-alpha-sprint10-evidence \
  --build-source-manifest \
  --source-manifest /secure/forja/alpha-source-manifest.json \
  --snapshot-root /secure/forja \
  --required-snapshot sec_identity=sec/company_tickers.json \
  --required-snapshot sec_submissions=sec/submissions/CIK0001045810.json \
  --required-snapshot sec_company_facts=sec/companyfacts/CIK0001045810.json \
  --required-snapshot treasury=treasury/real-yield-10y.csv \
  --required-snapshot fred=fred/FEDFUNDS.csv \
  --required-snapshot market=market/NVDA-adjusted.csv \
  --model-candidates /secure/forja/radeon-model-candidates.json \
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL"

python3 scripts/summarize_radeon_sprint10_evidence.py \
  --recovery /workspace/forja-alpha-sprint10-evidence/forja-alpha-competition-profile-recovery.json \
  --output /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json
"""


def readme_template(output_dir: Path) -> str:
    return f"""# Sprint 10 Radeon Operator Bundle

This private bundle accelerates the next Radeon Cloud boot. It contains no
credentials and no model weights, but it may contain local model IDs after you
edit the templates. Keep it outside Git.

## Files

- `sprint10-env.template.sh`: loopback endpoint environment.
- `radeon-model-candidates.template.json`: private two-candidate config.
- `run-sprint10-evidence.sh`: ordered evidence commands.

## Use

1. Copy `radeon-model-candidates.template.json` to
   `/secure/forja/radeon-model-candidates.json`.
2. Replace placeholder local model IDs and quantization notes.
3. Start the instruction and embedding endpoints on loopback.
4. Run `{output_dir.as_posix()}/run-sprint10-evidence.sh`.

The generated evidence still has to pass public-summary application,
independent review readiness, and v2 close-receipt promotion before Sprint 11
is authorized.
"""


def prepare_bundle(
    *,
    output_dir: Path,
    model_base_url: str,
    second_model_base_url: str,
    embedding_base_url: str,
    embedding_model: str,
) -> dict[str, Any]:
    for name, value in (
        ("model_base_url", model_base_url),
        ("second_model_base_url", second_model_base_url),
        ("embedding_base_url", embedding_base_url),
    ):
        ensure_loopback(name, value)

    output_dir.mkdir(parents=True, exist_ok=True)
    output_dir.chmod(0o700)
    write_private(
        output_dir / "radeon-model-candidates.template.json",
        json.dumps(
            candidate_template(
                model_base_url=model_base_url,
                second_model_base_url=second_model_base_url,
            ),
            indent=2,
            sort_keys=True,
        )
        + "\n",
    )
    write_private(
        output_dir / "sprint10-env.template.sh",
        env_template(
            model_base_url=model_base_url,
            embedding_base_url=embedding_base_url,
            embedding_model=embedding_model,
        ),
        executable=True,
    )
    write_private(output_dir / "run-sprint10-evidence.sh", commands_template(), executable=True)
    write_private(output_dir / "README.md", readme_template(output_dir))

    files = sorted(path.name for path in output_dir.iterdir() if path.is_file())
    modes = {
        path.name: stat.S_IMODE(path.stat().st_mode)
        for path in output_dir.iterdir()
        if path.is_file()
    }
    return {
        "bundle_dir": output_dir.as_posix(),
        "files": files,
        "modes": {name: oct(mode) for name, mode in modes.items()},
        "status": "prepared",
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, default=DEFAULT_OUTPUT_DIR)
    parser.add_argument("--model-base-url", default="http://127.0.0.1:8000/v1")
    parser.add_argument("--second-model-base-url", default="http://127.0.0.1:8001/v1")
    parser.add_argument("--embedding-base-url", default="http://127.0.0.1:8081/v1")
    parser.add_argument("--embedding-model", default="<local-embedding-model-id>")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report = prepare_bundle(
            output_dir=args.output_dir,
            model_base_url=args.model_base_url,
            second_model_base_url=args.second_model_base_url,
            embedding_base_url=args.embedding_base_url,
            embedding_model=args.embedding_model,
        )
    except (OSError, ValueError) as exc:
        print(f"operator bundle rejected: {exc}", file=sys.stderr)
        return 2
    sys.stdout.write(json.dumps(report, indent=2, sort_keys=True) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
