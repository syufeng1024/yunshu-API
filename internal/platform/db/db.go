// Package db opens the MySQL pools used by the three processes.
//
// gatewayd gets a deliberately small, restricted channel to the main database.
// T01 establishes only the pool settings and the per-transaction timeout; no
// business query is issued here.
package db

import (
	"context"
	"database/sql"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	// GatewayConnMaxLifetime recycles the gateway's restricted channel.
	GatewayConnMaxLifetime = 5 * time.Minute
	// GatewayTxnTimeout is the hard budget for one gateway main-database
	// transaction. It is four times the P10 50ms budget.
	GatewayTxnTimeout = 200 * time.Millisecond

	// MainPoolMaxOpen is the control-plane pool size.
	MainPoolMaxOpen = 20
	// MainPoolMaxIdle is the control-plane idle pool size.
	MainPoolMaxIdle = 10
	// JobsPoolMaxOpen is the jobs process pool size.
	JobsPoolMaxOpen = 10
	// PoolConnMaxLifetime bounds long-lived control-plane connections.
	PoolConnMaxLifetime = 5 * time.Minute
	// PoolConnMaxIdleTime bounds idle control-plane connections.
	PoolConnMaxIdleTime = time.Minute

	defaultGatewayDBMaxConns = 8
)

// Open creates a pool. It does not dial: the first use (or Ping) connects.
func Open(dsn string, maxOpen, maxIdle int, connMaxLifetime time.Duration) (*sql.DB, error) {
	d, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(maxOpen)
	d.SetMaxIdleConns(maxIdle)
	d.SetConnMaxLifetime(connMaxLifetime)
	d.SetConnMaxIdleTime(PoolConnMaxIdleTime)
	return d, nil
}

// OpenGateway opens the only main-database channel gatewayd may hold.
func OpenGateway(dsn string, maxConns int) (*sql.DB, error) {
	if maxConns <= 0 {
		maxConns = defaultGatewayDBMaxConns
	}
	return Open(dsn, maxConns, maxConns, GatewayConnMaxLifetime)
}

// OpenMain opens a general read/write pool for controld and jobsd.
func OpenMain(dsn string, maxOpen, maxIdle int) (*sql.DB, error) {
	return Open(dsn, maxOpen, maxIdle, PoolConnMaxLifetime)
}

// Ping checks connectivity within a bounded budget.
func Ping(ctx context.Context, d *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return d.PingContext(ctx)
}
