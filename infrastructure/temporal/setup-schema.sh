#!/bin/sh
# Installs or upgrades Temporal's SQL schemas (idempotent; runs on every start).
set -eu
: "${SQL_PASSWORD:?set WISE_TEMPORAL_DB_PASSWORD in .env}"
export SQL_PLUGIN=postgres12 SQL_HOST=postgres SQL_PORT=5432 SQL_USER=temporal
SCHEMA=/etc/temporal/schema/postgresql/v12

for pair in temporal:temporal temporal_visibility:visibility; do
  db=${pair%%:*}
  dir=${pair##*:}
  # Version 0.0 only creates the schema_version bookkeeping (no-op when present);
  # update-schema then applies every pending versioned migration.
  temporal-sql-tool --db "$db" setup-schema -v 0.0
  temporal-sql-tool --db "$db" update-schema -d "$SCHEMA/$dir/versioned"
done
echo "temporal schemas up to date"
