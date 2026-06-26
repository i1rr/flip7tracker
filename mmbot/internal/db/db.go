package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go, cgo-free SQLite driver registered as "sqlite"
)

// Open opens the SQLite database at databaseURL (a file path, e.g.
// "/data/flip7.db") using the pure-Go modernc.org/sqlite driver.
//
// The connection is configured via DSN pragmas to match the original Rust bot:
// WAL journaling, foreign-key enforcement, and a 5s busy-timeout so a concurrent
// dialog-submit vs action write waits on SQLite's single-writer lock instead of
// failing fast with SQLITE_BUSY. The pool is capped at 5 open / 1 idle
// connections (mirroring the Rust pool).
func Open(databaseURL string) (*sql.DB, error) {
	sep := "?"
	if strings.Contains(databaseURL, "?") {
		sep = "&"
	}
	dsn := databaseURL + sep + "_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", databaseURL, err)
	}
	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", databaseURL, err)
	}
	return sqlDB, nil
}
