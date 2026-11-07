// Package db owns the PostgreSQL connection pool, migrations and transactions.
package db

//go:generate go tool sqlc generate -f sqlc.yaml
