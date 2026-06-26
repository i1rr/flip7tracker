package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationsFS embeds the .sql migration files. The //go:embed pattern is
// resolved relative to THIS file's directory, so the files MUST sit under
// internal/db/migrations/ (not the module root).
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// sqlxMaxVersion is the highest migration numeric prefix that the original Rust
// bot's sqlx runner applied (001_initial..004_elo_rating). When a DB already
// carries a populated `_sqlx_migrations` table we seed our own tracking table as
// having applied 001..004 so only 005 (and any later additions) run — preventing
// 004's `ALTER TABLE players ADD COLUMN rating` from re-running and crashing with
// "duplicate column name" on the production DB.
const sqlxMaxVersion = 4

type migration struct {
	version string // the full filename, e.g. "001_initial.sql"
	num     int    // the leading numeric prefix, e.g. 1
	sql     string
}

// Migrate applies any pending embedded migrations. It is idempotent and safe to
// run at every startup.
//
// The whole batch runs on a single dedicated *sql.Conn that disables
// foreign-key enforcement for its lifetime (PRAGMA foreign_keys is a no-op
// inside a transaction, so it must be set outside any BEGIN). Each migration
// file runs in its own transaction; after the batch a PRAGMA foreign_key_check
// validates referential integrity (the 005 table rebuild drops/recreates
// `games`, which is referenced by several tables). Foreign keys are restored
// before the conn is released back to the pool.
func Migrate(ctx context.Context, sqlDB *sql.DB) error {
	all, err := loadMigrations()
	if err != nil {
		return err
	}

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	// First-run reconciliation with the sqlx-migrated production DB: if our
	// tracking table is empty but sqlx already ran its migrations, seed
	// 001..004 as applied so only 005 runs.
	if len(applied) == 0 {
		seeded, err := seedFromSqlx(ctx, conn, all)
		if err != nil {
			return err
		}
		for v := range seeded {
			applied[v] = struct{}{}
		}
	}

	var pending []migration
	for _, m := range all {
		if _, done := applied[m.version]; !done {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// Disable FK enforcement for the duration of the rebuild. This MUST happen
	// outside any transaction to take effect.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}

	if err := applyPending(ctx, conn, pending); err != nil {
		// Best-effort restore of FK enforcement before returning.
		_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
		return err
	}

	if err := foreignKeyCheck(ctx, conn); err != nil {
		_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
		return err
	}

	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("restore foreign_keys: %w", err)
	}
	return nil
}

func applyPending(ctx context.Context, conn *sql.Conn, pending []migration) error {
	for _, m := range pending {
		// Before the destructive 005 rebuild, self-check that the post-004
		// shape exists (the rebuild assumes players.rating is present). On a
		// fresh DB 001..004 will have run earlier in this same batch.
		if m.num >= 5 {
			ok, err := columnExists(ctx, conn, "players", "rating")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("migration %s: pre-condition failed: players.rating column missing (post-004 shape expected)", m.version)
			}
		}

		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			m.version, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", m.version, err)
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		content, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		out = append(out, migration{
			version: e.Name(),
			num:     numericPrefix(e.Name()),
			sql:     string(content),
		})
	}
	// Apply in lexical (== numeric, zero-padded) order.
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	if len(out) == 0 {
		return nil, fmt.Errorf("no embedded migrations found")
	}
	return out, nil
}

func numericPrefix(name string) int {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	n, err := strconv.Atoi(name[:i])
	if err != nil {
		return 0
	}
	return n
}

func appliedVersions(ctx context.Context, conn *sql.Conn) (map[string]struct{}, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[string]struct{})
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = struct{}{}
	}
	return applied, rows.Err()
}

// seedFromSqlx seeds schema_migrations with the migrations sqlx already applied
// (001..sqlxMaxVersion) when a populated `_sqlx_migrations` table is present.
// It returns the set of versions seeded (empty if no reconciliation happened).
func seedFromSqlx(ctx context.Context, conn *sql.Conn, all []migration) (map[string]struct{}, error) {
	seeded := make(map[string]struct{})

	var exists int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='_sqlx_migrations'",
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("probe _sqlx_migrations: %w", err)
	}
	if exists == 0 {
		return seeded, nil
	}

	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM _sqlx_migrations").Scan(&count); err != nil {
		return nil, fmt.Errorf("count _sqlx_migrations: %w", err)
	}
	if count == 0 {
		return seeded, nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, m := range all {
		if m.num >= 1 && m.num <= sqlxMaxVersion {
			if _, err := conn.ExecContext(ctx,
				"INSERT OR IGNORE INTO schema_migrations (version, applied_at) VALUES (?, ?)",
				m.version, now,
			); err != nil {
				return nil, fmt.Errorf("seed %s from sqlx: %w", m.version, err)
			}
			seeded[m.version] = struct{}{}
		}
	}
	return seeded, nil
}

func columnExists(ctx context.Context, conn *sql.Conn, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("table_info(%s): %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func foreignKeyCheck(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		// At least one violation. Report the first offending table.
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		_ = rows.Scan(ptrs...)
		return fmt.Errorf("foreign_key_check reported violations (first: %v)", vals)
	}
	return rows.Err()
}
