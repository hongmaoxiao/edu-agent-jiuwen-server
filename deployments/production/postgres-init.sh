#!/usr/bin/env bash
set -euo pipefail

# Read the password inside psql so it is absent from process arguments and logs.
psql --set=ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
\getenv app_password EDU_APP_PASSWORD
CREATE ROLE edu_agent LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'app_password';
CREATE DATABASE edu_agent OWNER edu_agent;
SQL
