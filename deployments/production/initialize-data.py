#!/usr/bin/env python3
"""Apply migrations twice, import content and provision initial accounts on gpu-train."""

import hashlib
import json
import os
from pathlib import Path
import pwd
import subprocess


def database_output(*args):
    return subprocess.check_output(
        ["docker", "exec", "edu-agent-api-postgres-1", *args], text=True
    )


def main():
    if os.geteuid() != 0:
        raise SystemExit("Run as root on gpu-train")
    release = Path("/var/www/edu-agent-jiuwen-server/releases/2ade9d02f5fdb55f83b2922f79aa515186bd1a96")
    environment = {"PATH": "/usr/local/bin:/usr/bin:/bin"}
    for line in Path("/etc/edu-agent-server/server.env").read_text().splitlines():
        if line and not line.startswith("#"):
            key, value = line.split("=", 1)
            environment[key] = value
    service_user = pwd.getpwnam("edu-agent")

    def run(binary, *args, input_text=None):
        subprocess.run(
            [str(release / "bin" / binary), *args],
            env=environment,
            cwd=release,
            user=service_user.pw_uid,
            group=service_user.pw_gid,
            extra_groups=(),
            input=input_text,
            text=True,
            check=True,
        )

    run("edu-agent-migrate")
    query = "SELECT name,sha256,applied_at FROM schema_migrations ORDER BY name"
    first = database_output("psql", "-U", "postgres", "-d", "edu_agent", "-Atc", query)
    schema_before = database_output("pg_dump", "-U", "postgres", "-d", "edu_agent", "--schema-only", "--no-owner", "--no-privileges", "--restrict-key=eduagentdeploycheck")
    run("edu-agent-migrate")
    second = database_output("psql", "-U", "postgres", "-d", "edu_agent", "-Atc", query)
    schema_after = database_output("pg_dump", "-U", "postgres", "-d", "edu_agent", "--schema-only", "--no-owner", "--no-privileges", "--restrict-key=eduagentdeploycheck")
    if first != second or schema_before != schema_after:
        raise SystemExit("The second migration changed migration records or the schema")
    print(f"Migration idempotency: PASS ({len(first.splitlines())} records unchanged)", flush=True)
    print("Schema SHA-256:", hashlib.sha256(schema_after.encode()).hexdigest(), flush=True)
    run("edu-agent-admin", "import-content", "seed/fde-content.json")
    passwords = json.loads(Path("/etc/edu-agent-server/accounts.json").read_text())
    for command, login, display_name in [
        ("create-operator", "ops", "运营"),
        ("create-demo", "demo", "客户演示"),
    ]:
        existing = database_output(
            "psql", "-U", "postgres", "-d", "edu_agent", "-Atc",
            f"SELECT count(*) FROM accounts WHERE login='{login}'",
        ).strip()
        if existing == "1":
            print(f"Preserved existing account: {login}", flush=True)
        else:
            run("edu-agent-admin", command, login, display_name, input_text=passwords[login] + "\n")
    print("Initial data provisioning: PASS", flush=True)


if __name__ == "__main__":
    main()
