#!/usr/bin/env python3
"""Render public-safe Radeon SSH recovery steps for the web terminal."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any


def load_wait_report(path: Path | None) -> dict[str, Any]:
    if path is None:
        return {}
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError("wait report must be a JSON object")
    return payload


def wait_status(payload: dict[str, Any]) -> str:
    result = payload.get("last_result")
    if isinstance(result, dict) and isinstance(result.get("status"), str):
        return result["status"]
    return "unknown"


def render_sheet(*, wait_report: dict[str, Any], host: str, port: str) -> str:
    status = wait_status(wait_report)
    return f"""# Radeon SSH Recovery Sheet

Status: private operator aid. This sheet contains no credentials, private keys,
tokens, source bodies, model weights, or evidence receipts. Run it only from
the Radeon web terminal when SSH is not ready.

Observed host: `{host}`
Observed port: `{port}`
Observed wait status: `{status}`

## 1. Diagnose SSH Inside The Radeon Instance

Run in the Radeon Jupyter/OpenCode terminal:

```bash
whoami
id
command -v sshd || true
ps -ef | grep '[s]shd' || true
ss -ltnp | grep ':22 ' || true
```

If `sshd` is running and listening on port 22, return to the workstation and
rerun:

```bash
python3 scripts/wait_radeon_ssh.py {host} {port} \\
  --timeout-seconds 180 \\
  --interval-seconds 10 \\
  --probe-timeout-seconds 8
```

## 2. Repair Missing OpenSSH Server

If `command -v sshd` prints nothing, install OpenSSH server from the web
terminal:

```bash
apt-get update
apt-get install -y openssh-server
```

## 3. Repair Missing Runtime Directory Or Host Keys

If `sshd` exists but cannot start, repair the common ephemeral-image issues:

```bash
mkdir -p /run/sshd
chmod 755 /run/sshd
ssh-keygen -A
```

## 4. Start SSHD

Try the service manager first. If this image does not use systemd, fall back to
the OpenSSH init script or direct daemon start:

```bash
systemctl restart ssh || service ssh restart || /usr/sbin/sshd
ps -ef | grep '[s]shd' || true
ss -ltnp | grep ':22 ' || true
```

## 5. Recheck From The Workstation

Run from the repository workstation:

```bash
python3 scripts/wait_radeon_ssh.py {host} {port} \\
  --timeout-seconds 180 \\
  --interval-seconds 10 \\
  --probe-timeout-seconds 8 \\
  --output /tmp/forja-radeon-ssh-wait.json
```

Proceed to the Sprint 10 evidence command sheet only when the report says
`"ready": true`. If the status remains `connected_no_banner`, keep the web
terminal open and inspect the `sshd` logs before spending GPU time on model
setup.
"""


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wait-report", type=Path)
    parser.add_argument("--host", default="<radeon-host>")
    parser.add_argument("--port", default="<radeon-port>")
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    wait_report = load_wait_report(args.wait_report)
    body = render_sheet(wait_report=wait_report, host=args.host, port=args.port)
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        print(body, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
