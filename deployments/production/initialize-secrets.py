#!/usr/bin/env python3
"""Create initial production secrets on the target host; never print their values."""

import json
import os
from pathlib import Path
import secrets


def main():
    if os.geteuid() != 0:
        raise SystemExit("Run as root on gpu-train")
    root = Path("/etc/edu-agent-server")
    root.mkdir(mode=0o700, exist_ok=True)
    paths = [root / name for name in ("server.env", "postgres.env", "accounts.json")]
    if any(path.exists() for path in paths):
        raise SystemExit("Initial secrets already exist; preserve them and inspect the deployment state")
    app_password = secrets.token_urlsafe(36)
    postgres_password = secrets.token_urlsafe(36)
    server_env = (
        "EDU_ENV=production\n"
        "EDU_LISTEN=127.0.0.1:4020\n"
        "EDU_METRICS_LISTEN=127.0.0.1:4029\n"
        "EDU_WEB_ORIGINS=https://agent.hrtiger.cn\n"
        "EDU_TRUST_PROXY=loopback\n"
        "EDU_ALLOW_LOOPBACK_ORIGINS=true\n"
        f"DATABASE_URL=postgres://edu_agent:{app_password}@127.0.0.1:25432/edu_agent?sslmode=disable\n"
    )
    postgres_env = (
        "POSTGRES_USER=postgres\nPOSTGRES_DB=postgres\n"
        f"POSTGRES_PASSWORD={postgres_password}\nEDU_APP_PASSWORD={app_password}\n"
    )
    accounts = json.dumps(
        {"ops": secrets.token_urlsafe(24), "demo": secrets.token_urlsafe(24)},
        indent=2,
    ) + "\n"
    for path, content in zip(paths, [server_env, postgres_env, accounts]):
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as handle:
            handle.write(content)
        print(f"Created {path} (root:root 0600)")


if __name__ == "__main__":
    main()
