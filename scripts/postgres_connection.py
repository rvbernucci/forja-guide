#!/usr/bin/env python3
"""Separate PostgreSQL URI credentials from the process argument value."""

from __future__ import annotations

import os
import sys
import urllib.parse


def main() -> None:
    raw = os.environ["FORJA_DATABASE_URL"]
    parsed = urllib.parse.urlsplit(raw)
    if parsed.scheme not in {"postgres", "postgresql"}:
        raise SystemExit("FORJA_DATABASE_URL must be a PostgreSQL URI")
    if parsed.fragment:
        raise SystemExit("FORJA_DATABASE_URL must not contain a fragment")

    query = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
    if any(key.lower() in {"password", "sslpassword"} for key, _ in query):
        raise SystemExit(
            "password query parameters are forbidden; "
            "use URI userinfo or libpq environment"
        )

    authority = parsed.netloc
    password = ""
    if "@" in authority:
        raw_userinfo, host_list = authority.rsplit("@", 1)
        raw_username, separator, raw_password = raw_userinfo.partition(":")
        username = urllib.parse.unquote(raw_username)
        userinfo = urllib.parse.quote(username, safe="") + "@"
        authority = userinfo + host_list
        if separator:
            password = urllib.parse.unquote(raw_password)

    safe = f"{parsed.scheme}://{authority}{parsed.path}"
    if parsed.query:
        safe += f"?{parsed.query}"
    sys.stdout.buffer.write(safe.encode() + b"\0" + password.encode() + b"\0")


if __name__ == "__main__":
    main()
