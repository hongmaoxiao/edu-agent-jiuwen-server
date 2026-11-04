// Package migrations applies the embedded SQL files in name order, once each; applied files must never change.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

func Ready(ctx context.Context, db *pgxpool.Pool) error {
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		body, err := files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		var stored string
		if err := db.QueryRow(ctx, "SELECT sha256 FROM schema_migrations WHERE name=$1", entry.Name()).Scan(&stored); err != nil {
			return err
		}
		if stored != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("migration version mismatch")
		}
	}
	return nil
}

func Apply(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(4020001)"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, sha256 text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		body, err := files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])
		var previous string
		err = tx.QueryRow(ctx, "SELECT sha256 FROM schema_migrations WHERE name=$1", entry.Name()).Scan(&previous)
		if err == nil {
			if previous != hash {
				return fmt.Errorf("applied migration changed: %s", entry.Name())
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(name,sha256) VALUES($1,$2)", entry.Name(), hash); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
