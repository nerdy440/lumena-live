// Package db provides the shared Postgres connection pool and a minimal
// migration runner for every module's *_pgrepo package (auth/pgrepo,
// ledger/pgrepo, profile/pgrepo, ...). This is the real-database half of
// the local-vs-production split: feed/cmd/api uses these Postgres repos
// when DATABASE_URL is set, and falls back to the existing in-memory
// Mem* repos (plus localstate.go's JSON-snapshot persistence) when it
// isn't — so a personal/local instance never needs Postgres, but a real
// deployment always should.
package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pooled connection to databaseURL and verifies it's
// reachable (Ping) before returning — fail fast at startup rather than on
// the first real request.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// RunMigrations applies every .sql file in dir whose filename hasn't
// already been recorded in the schema_migrations table, in filename
// order (hence the "0001_", "0002_" prefixes on every migration file —
// plain lexical sort is the whole ordering mechanism, deliberately no
// timestamp/hash scheme for a project this size). Each file runs in its
// own transaction; a failure leaves already-applied files applied and
// stops before the failing one, safe to fix and re-run.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		filename TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("db: read migrations dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		var already bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`, name).Scan(&already); err != nil {
			return fmt.Errorf("db: check migration %s: %w", name, err)
		}
		if already {
			continue
		}
		sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("db: read migration %s: %w", name, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("db: begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("db: apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("db: record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("db: commit migration %s: %w", name, err)
		}
	}
	return nil
}
