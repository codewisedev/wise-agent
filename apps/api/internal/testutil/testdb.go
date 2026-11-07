// Package testutil provides helpers for integration tests.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wise/apps/api/internal/db"
)

// NewDB creates an isolated, migrated database for the test and drops it on
// cleanup. Requires WISE_TEST_DATABASE_URL (a role allowed to CREATE DATABASE);
// the test is skipped otherwise.
func NewDB(t *testing.T) *db.DB {
	t.Helper()
	adminURL := os.Getenv("WISE_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("WISE_TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin db: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "wise_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	d, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(d.Close)
	if err := d.MigrateUp(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}
