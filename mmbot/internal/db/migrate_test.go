package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// seedSqlxProductionShape builds a DB at path that mirrors the live, sqlx-migrated
// production database: the full post-004 schema (legacy Telegram-integer `games`
// /`chat_last_players`, plus `players.rating`) AND a populated `_sqlx_migrations`
// table recording that migrations 001..004 already ran. It seeds players with
// known non-default ratings, finished games, score entries, winners, rating
// history, and one unfinished game.
func seedSqlxProductionShape(t *testing.T, path string) {
	t.Helper()

	// Raw connection (no embedded migrations) so we can build the OLD shape.
	raw, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()

	stmts := []string{
		`CREATE TABLE players (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			is_deleted INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			rating REAL NOT NULL DEFAULT 1000.0
		)`,
		`CREATE TABLE games (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			message_id INTEGER,
			started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			finished_at TEXT,
			winner_player_id INTEGER REFERENCES players(id)
		)`,
		`CREATE TABLE game_players (
			game_id INTEGER NOT NULL REFERENCES games(id),
			player_id INTEGER NOT NULL REFERENCES players(id),
			PRIMARY KEY (game_id, player_id)
		)`,
		`CREATE TABLE score_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			game_id INTEGER NOT NULL REFERENCES games(id),
			player_id INTEGER NOT NULL REFERENCES players(id),
			points INTEGER NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		)`,
		`CREATE INDEX idx_games_chat_id ON games(chat_id)`,
		`CREATE INDEX idx_score_entries_game_id ON score_entries(game_id)`,
		`CREATE INDEX idx_game_players_game_id ON game_players(game_id)`,
		`CREATE TABLE chat_last_players (
			chat_id INTEGER NOT NULL,
			player_id INTEGER NOT NULL REFERENCES players(id),
			PRIMARY KEY (chat_id, player_id)
		)`,
		`CREATE TABLE game_winners (
			game_id INTEGER NOT NULL REFERENCES games(id),
			player_id INTEGER NOT NULL REFERENCES players(id),
			PRIMARY KEY (game_id, player_id)
		)`,
		`CREATE INDEX idx_game_winners_game_id ON game_winners(game_id)`,
		`CREATE INDEX idx_game_winners_player_id ON game_winners(player_id)`,
		`CREATE TABLE rating_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			game_id INTEGER NOT NULL REFERENCES games(id),
			player_id INTEGER NOT NULL REFERENCES players(id),
			rating_before REAL NOT NULL,
			rating_after REAL NOT NULL,
			delta REAL NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		)`,
		`CREATE INDEX idx_rating_history_game_id ON rating_history(game_id)`,
		`CREATE INDEX idx_rating_history_player_id ON rating_history(player_id)`,
		// sqlx's own migration-tracking table, populated with 001..004.
		`CREATE TABLE _sqlx_migrations (
			version BIGINT PRIMARY KEY,
			description TEXT NOT NULL,
			installed_on TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			success BOOLEAN NOT NULL,
			checksum BLOB NOT NULL,
			execution_time BIGINT NOT NULL
		)`,
		`INSERT INTO _sqlx_migrations (version, description, success, checksum, execution_time) VALUES
			(1, 'initial', 1, x'00', 1),
			(2, 'last_players', 1, x'00', 1),
			(3, 'game_winners', 1, x'00', 1),
			(4, 'elo_rating', 1, x'00', 1)`,

		// --- Data ---
		`INSERT INTO players (id, name, is_deleted, rating) VALUES
			(1, 'Alice', 0, 1234.5),
			(2, 'Bob',   0, 1011.0),
			(3, 'Carol', 0, 987.25),
			(4, 'Dave',  0, 1000.0)`,
		// Two distinct Telegram chats -> two finished games + one unfinished.
		`INSERT INTO games (id, chat_id, message_id, finished_at, winner_player_id) VALUES
			(1, 111, 5001, '2026-01-01T00:00:00.000Z', 1),
			(2, 222, 5002, '2026-02-01T00:00:00.000Z', 2),
			(3, 111, 5003, NULL, NULL)`,
		`INSERT INTO game_players (game_id, player_id) VALUES
			(1,1),(1,2),(2,2),(2,3),(3,1),(3,4)`,
		`INSERT INTO score_entries (game_id, player_id, points) VALUES
			(1,1,210),(1,2,150),(2,2,205),(2,3,180),(3,1,40),(3,4,30)`,
		`INSERT INTO game_winners (game_id, player_id) VALUES (1,1),(2,2)`,
		`INSERT INTO rating_history (game_id, player_id, rating_before, rating_after, delta) VALUES
			(1,1,1222.5,1234.5,12.0),
			(1,2,1023.0,1011.0,-12.0),
			(2,2,1023.0,1011.0,-12.0),
			(2,3,975.25,987.25,12.0)`,
		// chat_last_players: player 2 appears under BOTH chats -> will collide on
		// the rebuilt (channel_id='', player_id) PK and must be deduped.
		`INSERT INTO chat_last_players (chat_id, player_id) VALUES
			(111,1),(111,2),(222,2),(222,3)`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("seed exec failed: %v\nstmt: %s", err, s)
		}
	}
}

func TestMigrateParityAgainstSqlxShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flip7.db")
	seedSqlxProductionShape(t, path)

	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// 001..004 must NOT have re-run: only 005 should be newly recorded beyond the
	// seeded versions. (A re-run of 004 would have failed with "duplicate column
	// name rating" and Migrate would already have errored above.)
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM schema_migrations", 5)
	if !versionApplied(t, sqlDB, "005_mattermost_ids.sql") {
		t.Fatalf("005 was not recorded as applied")
	}

	// Row counts for the valuable history are unchanged.
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM players", 4)
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM score_entries", 6)
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM game_winners", 2)
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM rating_history", 4)
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM games", 3)

	// Known ratings survive untouched.
	assertRating(t, sqlDB, 1, 1234.5)
	assertRating(t, sqlDB, 2, 1011.0)
	assertRating(t, sqlDB, 3, 987.25)

	// Every migrated game row: post_id NULL, channel_id ''.
	var nonNullPost, nonEmptyChannel int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM games WHERE post_id IS NOT NULL").Scan(&nonNullPost); err != nil {
		t.Fatal(err)
	}
	if nonNullPost != 0 {
		t.Fatalf("expected all post_id NULL, found %d non-null", nonNullPost)
	}
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM games WHERE channel_id <> ''").Scan(&nonEmptyChannel); err != nil {
		t.Fatal(err)
	}
	if nonEmptyChannel != 0 {
		t.Fatalf("expected all channel_id '', found %d non-empty", nonEmptyChannel)
	}

	// The unfinished game (id 3) survives and is resumable (still finished_at NULL).
	var unfinished int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM games WHERE finished_at IS NULL").Scan(&unfinished); err != nil {
		t.Fatal(err)
	}
	if unfinished != 1 {
		t.Fatalf("expected 1 unfinished game, got %d", unfinished)
	}

	// chat_last_players deduped: player 2's two legacy rows collapse to one.
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM chat_last_players", 3)

	// New column affinities are TEXT.
	assertColumnType(t, sqlDB, "games", "channel_id", "TEXT")
	assertColumnType(t, sqlDB, "games", "post_id", "TEXT")
	assertColumnType(t, sqlDB, "chat_last_players", "channel_id", "TEXT")

	// AUTOINCREMENT preserved: a fresh insert gets an id above the prior max.
	var maxID int64
	if err := sqlDB.QueryRow("SELECT MAX(id) FROM games").Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	res, err := sqlDB.Exec("INSERT INTO games (channel_id, post_id) VALUES ('C1', NULL)")
	if err != nil {
		t.Fatalf("insert new game: %v", err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if newID <= maxID {
		t.Fatalf("AUTOINCREMENT not preserved: new id %d not above prior max %d", newID, maxID)
	}

	// Idempotency: a second Migrate is a clean no-op.
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM schema_migrations", 5)
}

// TestMigrateFreshDB runs all five migrations on an empty DB (no _sqlx_migrations)
// and confirms the full schema builds.
func TestMigrateFreshDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")

	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatalf("Migrate fresh: %v", err)
	}
	assertCount(t, sqlDB, "SELECT COUNT(*) FROM schema_migrations", 5)

	// Post-005 shape: games has TEXT channel_id/post_id.
	assertColumnType(t, sqlDB, "games", "channel_id", "TEXT")
	assertColumnType(t, sqlDB, "games", "post_id", "TEXT")

	// players.rating exists from 004.
	assertColumnType(t, sqlDB, "players", "rating", "REAL")

	// Inserting works end-to-end with FK on.
	if _, err := sqlDB.Exec("INSERT INTO players (name) VALUES ('Z')"); err != nil {
		t.Fatalf("insert player: %v", err)
	}
	if _, err := sqlDB.Exec("INSERT INTO games (channel_id, post_id) VALUES ('C', NULL)"); err != nil {
		t.Fatalf("insert game: %v", err)
	}
}

func assertCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %q = %d, want %d", query, got, want)
	}
}

func assertRating(t *testing.T, db *sql.DB, playerID int64, want float64) {
	t.Helper()
	var got float64
	if err := db.QueryRow("SELECT rating FROM players WHERE id = ?", playerID).Scan(&got); err != nil {
		t.Fatalf("rating for %d: %v", playerID, err)
	}
	if got != want {
		t.Fatalf("rating for player %d = %v, want %v", playerID, got, want)
	}
}

func versionApplied(t *testing.T, db *sql.DB, version string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&n); err != nil {
		t.Fatalf("check version %s: %v", version, err)
	}
	return n == 1
}

func assertColumnType(t *testing.T, db *sql.DB, table, column, wantType string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
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
			t.Fatal(err)
		}
		if name == column {
			if !strings.EqualFold(ctype, wantType) {
				t.Fatalf("%s.%s type = %q, want %q", table, column, ctype, wantType)
			}
			return
		}
	}
	t.Fatalf("%s.%s not found", table, column)
}
