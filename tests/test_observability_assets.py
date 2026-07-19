#!/usr/bin/env python3
"""Validate the pinned, low-cardinality local observability assets."""

from __future__ import annotations

import json
import re
import shutil
import subprocess
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
OBS = ROOT / "deploy" / "observability"


class ObservabilityAssetsTests(unittest.TestCase):
    def test_compose_uses_exact_images_and_loopback_ports(self) -> None:
        expected = {
            "otel/opentelemetry-collector-contrib:0.153.0",
            "grafana/tempo:2.10.5",
            "prom/prometheus:v3.12.0",
            "grafana/loki:3.7.2",
            "grafana/alloy:v1.16.1",
            "grafana/grafana:13.1.0",
        }
        for name in ("compose.yaml", "compose.linux.yaml"):
            compose = (OBS / name).read_text(encoding="utf-8")
            images = set(re.findall(r"^\s+image:\s+(\S+)\s*$", compose, re.MULTILINE))
            self.assertEqual(images, expected, name)
            self.assertNotIn(":latest", compose, name)
            published_ports = re.findall(r'^\s+- "([^\"]+:\d+:\d+)"\s*$', compose, re.MULTILINE)
            self.assertTrue(published_ports, name)
            self.assertTrue(
                all(value.startswith("127.0.0.1:") for value in published_ports), name
            )

    def test_runtime_profiles_scrape_loopback_without_exposing_daemon(self) -> None:
        for compose_name, prometheus_name in (
            ("compose.yaml", "prometheus.yaml"),
            ("compose.linux.yaml", "prometheus-linux.yaml"),
        ):
            compose = (OBS / compose_name).read_text(encoding="utf-8")
            prometheus = (OBS / prometheus_name).read_text(encoding="utf-8")
            self.assertGreaterEqual(compose.count("network_mode: host"), 2)
            self.assertIn("--web.listen-address=127.0.0.1:9090", compose)
            self.assertIn("GF_SERVER_HTTP_ADDR: 127.0.0.1", compose)
            self.assertIn(
                "--server.http.listen-addr=0.0.0.0:12345", compose
            )
            self.assertIn('targets: ["127.0.0.1:8080"]', prometheus)
            self.assertIn('targets: ["127.0.0.1:9464"]', prometheus)
            self.assertNotIn("host.docker.internal", compose + prometheus)

    def test_dashboard_is_parseable_and_uses_bounded_signals(self) -> None:
        dashboard = json.loads(
            (OBS / "grafana/dashboards/forja-overview.json").read_text(encoding="utf-8")
        )
        self.assertEqual(dashboard["uid"], "forja-runtime-overview")
        expressions = "\n".join(
            target.get("expr", "")
            for panel in dashboard["panels"]
            for target in panel.get("targets", [])
        )
        self.assertIn("forja_operational_condition_items", expressions)
        self.assertIn("forja_operations_total", expressions)
        for forbidden in ("tenant_id", "repository_id", "run_id", "task_id", "attempt_id"):
            self.assertNotIn(forbidden, expressions)

    def test_alerts_cover_declared_failure_modes(self) -> None:
        alerts = (OBS / "alerts.yaml").read_text(encoding="utf-8")
        for condition in (
            "stuck_runs",
            "expired_leases",
            "pending_outbox",
            "dead_outbox",
            "projection_lag",
            "pending_approvals",
            "worker_crash_loops",
        ):
            self.assertIn(f'condition="{condition}"', alerts)
        for forbidden in ("tenant_id", "repository_id", "run_id", "task_id", "attempt_id"):
            self.assertNotIn(forbidden, alerts)

    def test_yaml_assets_have_no_tabs_or_unexpanded_secrets(self) -> None:
        for path in OBS.rglob("*.yaml"):
            data = path.read_text(encoding="utf-8")
            self.assertNotIn("\t", data, path)
            self.assertNotRegex(data, r"(?i)(password|api_key|bearer_token):\s*\S+")

    def test_stack_smoke_uses_valid_synthetic_bearer_fixture(self) -> None:
        script = (ROOT / "scripts/observability_stack_smoke.sh").read_text(
            encoding="utf-8"
        )
        match = re.search(r'FORJA_HTTP_BEARER_TOKEN="([^"]+)"', script)
        self.assertIsNotNone(match)
        self.assertGreaterEqual(len(match.group(1).encode("utf-8")), 32)
        self.assertIn("observability stack diagnostics", script)
        self.assertIn("http://127.0.0.1:12345/-/ready", script)

    def test_unstructured_stderr_is_not_ingested_as_json_logs(self) -> None:
        script = (ROOT / "scripts/observability_stack_smoke.sh").read_text(
            encoding="utf-8"
        )
        operations = (
            ROOT / "docs/06-operations/OBSERVABILITY_OPERATIONS.md"
        ).read_text(encoding="utf-8")
        self.assertNotRegex(script, r"forjad\.jsonl[^\n]*2>&1")
        self.assertNotRegex(operations, r"forjad\.jsonl[^\n]*2>&1")
        self.assertIn('2>"$work/forjad.stderr"', script)
        self.assertIn("2> /tmp/forja/forjad.stderr", operations)
        self.assertIn("non-ingested", operations)

    def test_compose_parser_when_available(self) -> None:
        if shutil.which("docker") is None:
            self.skipTest("Docker CLI is not available")
        for name in ("compose.yaml", "compose.linux.yaml"):
            subprocess.run(
                ["docker", "compose", "-f", str(OBS / name), "config", "--quiet"],
                cwd=ROOT,
                check=True,
            )


if __name__ == "__main__":
    unittest.main()
