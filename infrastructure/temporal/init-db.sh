#!/bin/sh
# Creates Temporal's role and databases in the shared Postgres (idempotent).
# Runs once per `docker compose up` as the wise superuser; Temporal itself only
# ever connects as the unprivileged `temporal` role that owns its two databases.
set -eu
: "${TEMPORAL_DB_PASSWORD:?set WISE_TEMPORAL_DB_PASSWORD in .env}"

until pg_isready -h postgres -U wise -d wise -q; do sleep 1; done

psql -v ON_ERROR_STOP=1 -h postgres -U wise -d wise -v pw="$TEMPORAL_DB_PASSWORD" <<'SQL'
SELECT 'CREATE ROLE temporal LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'temporal')\gexec
ALTER ROLE temporal WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'pw';
SELECT 'CREATE DATABASE temporal OWNER temporal'
  WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'temporal')\gexec
SELECT 'CREATE DATABASE temporal_visibility OWNER temporal'
  WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'temporal_visibility')\gexec
REVOKE ALL ON DATABASE temporal FROM PUBLIC;
REVOKE ALL ON DATABASE temporal_visibility FROM PUBLIC;
SQL
echo "temporal role and databases ready"
