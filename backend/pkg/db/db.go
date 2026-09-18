// Package db provides the database connection pool and migration tooling.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds database connection parameters.
type Config struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// DefaultConfig returns safe production defaults.
func DefaultConfig(dsn string) Config {
	return Config{
		DSN:             dsn,
		MaxConns:        25,
		MinConns:        2,
		MaxConnLifetime: 30 * time.Minute,
		MaxConnIdleTime: 5 * time.Minute,
	}
}

// Connect establishes the connection pool and verifies connectivity.
func Connect(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// LedgerConnect connects to the ledger's *separate* database with stricter settings.
// The ledger uses SERIALIZABLE isolation by default.
// It connects with a dedicated role that has INSERT-only on ledger_entries
// and no UPDATE/DELETE grants — enforced at the DB level.
func LedgerConnect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg := DefaultConfig(dsn)
	cfg.MaxConns = 10 // ledger writes are serialized; more connections ≠ more throughput here
	return Connect(ctx, cfg)
}
