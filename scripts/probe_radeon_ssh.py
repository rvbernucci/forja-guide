#!/usr/bin/env python3
"""Probe a Radeon Cloud SSH endpoint and classify connection readiness."""

from __future__ import annotations

import argparse
import json
import socket
import sys
from dataclasses import asdict, dataclass


@dataclass(frozen=True)
class ProbeResult:
    host: str
    port: int
    status: str
    ssh_banner_seen: bool
    detail: str


def probe(host: str, port: int, timeout_seconds: float) -> ProbeResult:
    try:
        with socket.create_connection((host, port), timeout=timeout_seconds) as sock:
            sock.settimeout(timeout_seconds)
            try:
                banner = sock.recv(128)
            except socket.timeout:
                return ProbeResult(
                    host=host,
                    port=port,
                    status="connected_no_banner",
                    ssh_banner_seen=False,
                    detail="tcp_connected_but_no_banner_before_timeout",
                )
    except TimeoutError:
        return ProbeResult(
            host=host,
            port=port,
            status="timeout",
            ssh_banner_seen=False,
            detail="tcp_connect_timeout",
        )
    except ConnectionRefusedError:
        return ProbeResult(
            host=host,
            port=port,
            status="refused",
            ssh_banner_seen=False,
            detail="tcp_connection_refused",
        )
    except OSError as exc:
        return ProbeResult(
            host=host,
            port=port,
            status="unreachable",
            ssh_banner_seen=False,
            detail=exc.__class__.__name__,
        )

    return classify_banner(host, port, banner)


def classify_banner(host: str, port: int, banner: bytes) -> ProbeResult:
    if banner.startswith(b"SSH-"):
        return ProbeResult(
            host=host,
            port=port,
            status="ready",
            ssh_banner_seen=True,
            detail=banner.decode("utf-8", errors="replace").strip(),
        )
    return ProbeResult(
        host=host,
        port=port,
        status="unexpected_banner",
        ssh_banner_seen=False,
        detail=banner[:64].decode("utf-8", errors="replace").strip(),
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host")
    parser.add_argument("port", type=int)
    parser.add_argument("--timeout-seconds", type=float, default=8.0)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    result = probe(args.host, args.port, args.timeout_seconds)
    sys.stdout.write(json.dumps(asdict(result), indent=2, sort_keys=True) + "\n")
    return 0 if result.status == "ready" else 2


if __name__ == "__main__":
    raise SystemExit(main())
