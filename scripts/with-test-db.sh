#!/bin/sh
# Run a command against a throwaway PostgreSQL 18, removed on exit:
#   1. EDU_TEST_DATABASE_URL already set → use it as is;
#   2. otherwise Docker → a postgres:18.6 container on a free loopback port. The Mac runs PostgreSQL 18 this way
#      instead of a Homebrew install, which would upgrade shared libraries (user choice 2026-11-03).
set -eu
if [ -n "${EDU_TEST_DATABASE_URL:-}" ]; then
  exec "$@"
fi
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
if command -v docker >/dev/null 2>&1; then
  name="edu-test-pg-$port"
  cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  # The database lives in the container's own layer and goes away with it; trust auth is fine on a loopback-only port.
  docker run -d --rm --name "$name" -e POSTGRES_USER=edu_test -e POSTGRES_DB=edu_test -e POSTGRES_HOST_AUTH_METHOD=trust \
    -p "127.0.0.1:$port:5432" "${EDU_TEST_PG_IMAGE:-postgres:18.6}" >/dev/null
  tries=0
  until docker exec "$name" pg_isready -U edu_test -d edu_test -h 127.0.0.1 >/dev/null 2>&1; do
    tries=$((tries + 1))
    if [ "$tries" -gt 60 ]; then
      echo "PostgreSQL container did not become ready" >&2
      exit 1
    fi
    sleep 1
  done
else
  echo "PostgreSQL 18 is required: set EDU_TEST_DATABASE_URL or install Docker." >&2
  exit 1
fi
export EDU_TEST_DATABASE_URL="postgres://edu_test@127.0.0.1:$port/edu_test?sslmode=disable"
"$@"
