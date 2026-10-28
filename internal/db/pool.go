// Package db owns the pgxpool lifecycle for the application.
//
// One pool is created at startup and injected into every consumer. Callers
// never construct their own pool, so limits and shutdown are configured in one
// place.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jaywestty/walletcore/internal/config"
)

// Limits is the pool tuning applied on top of the connection string.
type Limits struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// Pool wraps a *pgxpool.Pool. It exists so consumers depend on this type and
// can be substituted in tests without importing pgx directly.
type Pool struct {
	*pgxpool.Pool
}

// Open creates a pool, applies limits, and verifies connectivity with a ping.
// It returns an error if the pool cannot be created or the database does not
// answer within the connection context, so startup fails fast.
func Open(ctx context.Context, databaseURL string, limits Limits) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	cfg.MaxConns = limits.MaxConns
	cfg.MinConns = limits.MinConns
	cfg.MaxConnLifetime = limits.MaxConnLifetime
	cfg.MaxConnIdleTime = limits.MaxConnIdleTime
	cfg.HealthCheckPeriod = limits.HealthCheckPeriod

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Pool{Pool: pool}, nil
}

// LimitsFromConfig adapts application config into pool limits.
func LimitsFromConfig(c *config.Config) Limits {
	return Limits{
		MaxConns:          c.DBMaxConns,
		MinConns:          c.DBMinConns,
		MaxConnLifetime:   c.DBMaxConnLifetime,
		MaxConnIdleTime:   c.DBMaxConnIdleTime,
		HealthCheckPeriod: c.DBHealthCheckPeriod,
	}
}
