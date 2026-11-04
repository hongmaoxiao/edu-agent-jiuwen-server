// Package testdb gives each test its own schema in an explicitly configured PostgreSQL test database.
package testdb

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"edu-agent-jiuwen-server/internal/platform"
	"edu-agent-jiuwen-server/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("EDU_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("use make check or scripts/with-test-db.sh for PostgreSQL integration tests")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "edu_test") {
		t.Fatal("test database name must start with edu_test")
	}
	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := platform.ID("test")
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = name
	db, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+name+" CASCADE"); err != nil {
			t.Errorf("test schema cleanup: %v", err)
		}
		admin.Close()
	})
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}
