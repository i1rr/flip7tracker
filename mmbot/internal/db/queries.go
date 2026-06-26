package db

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/rivan/flip7bot/mmbot/internal/rating"
)

// queries.go is the faithful Go port of the original Rust db/queries.rs. Every
// function takes a context.Context and a *sql.DB. Channel/post ids that were
// Telegram integers in the Rust bot are now Mattermost strings (channel_id,
// post_id) after migration 005. Multi-statement mutations run inside an explicit
// transaction with rollback-on-error; any variable-length clause is built from
// bound `?` placeholders, never string-interpolated ids.

// scanPlayer scans a single players row (id, name, is_deleted, created_at,
// rating) from the given Scanner.
func scanPlayer(s interface{ Scan(...any) error }) (Player, error) {
	var p Player
	err := s.Scan(&p.ID, &p.Name, &p.IsDeleted, &p.CreatedAt, &p.Rating)
	return p, err
}

// scanPlayers reads all rows from a players query into a slice.
func scanPlayers(rows *sql.Rows) ([]Player, error) {
	defer rows.Close()
	var out []Player
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// scanGame scans a single games row (id, channel_id, post_id, started_at,
// finished_at, winner_player_id) from the given Scanner.
func scanGame(s interface{ Scan(...any) error }) (Game, error) {
	var g Game
	err := s.Scan(&g.ID, &g.ChannelID, &g.PostID, &g.StartedAt, &g.FinishedAt, &g.WinnerPlayerID)
	return g, err
}

// CreateOrGetPlayer inserts a player with the given name if none exists, undoes
// a soft-delete on an existing same-name player, and returns the row. The
// insert + un-delete + read run in one transaction for atomicity.
func CreateOrGetPlayer(ctx context.Context, db *sql.DB, name string) (Player, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Player{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO players (name) VALUES (?)", name); err != nil {
		return Player{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE players SET is_deleted = 0 WHERE name = ? AND is_deleted = 1", name); err != nil {
		return Player{}, err
	}
	p, err := scanPlayer(tx.QueryRowContext(ctx,
		"SELECT id, name, is_deleted, created_at, rating FROM players WHERE name = ?", name))
	if err != nil {
		return Player{}, err
	}
	if err := tx.Commit(); err != nil {
		return Player{}, err
	}
	return p, nil
}

// GetAllActivePlayers returns all non-soft-deleted players ordered by name.
func GetAllActivePlayers(ctx context.Context, db *sql.DB) ([]Player, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, name, is_deleted, created_at, rating FROM players WHERE is_deleted = 0 ORDER BY name")
	if err != nil {
		return nil, err
	}
	return scanPlayers(rows)
}

// CreateGame inserts a new (unfinished) game for channelID and returns it.
func CreateGame(ctx context.Context, db *sql.DB, channelID string) (Game, error) {
	res, err := db.ExecContext(ctx, "INSERT INTO games (channel_id) VALUES (?)", channelID)
	if err != nil {
		return Game{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Game{}, err
	}
	return scanGame(db.QueryRowContext(ctx,
		"SELECT id, channel_id, post_id, started_at, finished_at, winner_player_id FROM games WHERE id = ?", id))
}

// GetUnfinishedGames returns the channel's unfinished games, newest first.
func GetUnfinishedGames(ctx context.Context, db *sql.DB, channelID string) ([]Game, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, channel_id, post_id, started_at, finished_at, winner_player_id FROM games WHERE channel_id = ? AND finished_at IS NULL ORDER BY started_at DESC",
		channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Game
	for rows.Next() {
		g, err := scanGame(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// FinishGameWithWinners marks a game finished, records its winners, and applies
// Elo rating updates — all in one transaction. It is idempotent: the finalizing
// UPDATE ... WHERE id=? AND finished_at IS NULL is the guard. If it affects no
// rows the game was already finished, so no game_winners / rating_history rows
// are written and no players.rating is changed; the call returns false.
// Otherwise it writes everything and returns true.
//
// winnerIDs may contain one player (sole winner) or several (joint winners who
// tied at the highest score). eloDeltas must include every participant.
func FinishGameWithWinners(ctx context.Context, db *sql.DB, gameID int64, winnerIDs []int64, eloDeltas []rating.EloDelta) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var primary sql.NullInt64
	if len(winnerIDs) > 0 {
		primary = sql.NullInt64{Int64: winnerIDs[0], Valid: true}
	}

	res, err := tx.ExecContext(ctx,
		"UPDATE games SET finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), winner_player_id = ? WHERE id = ? AND finished_at IS NULL",
		primary, gameID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		// Already finished (or no such game): idempotent no-op.
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}

	for _, pid := range winnerIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO game_winners (game_id, player_id) VALUES (?, ?)", gameID, pid); err != nil {
			return false, err
		}
	}
	for _, d := range eloDeltas {
		if _, err := tx.ExecContext(ctx,
			"UPDATE players SET rating = ? WHERE id = ?", d.RatingAfter, d.PlayerID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO rating_history (game_id, player_id, rating_before, rating_after, delta) VALUES (?, ?, ?, ?, ?)",
			gameID, d.PlayerID, d.RatingBefore, d.RatingAfter, d.Delta); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// DiscardGame removes an unfinished game completely (score entries, player
// links, winner links, and the game row). It is guarded and replay-safe: it
// deletes only if the game row still exists and is unfinished, and returns true
// iff this call actually deleted it. A double-tap on an already-discarded or
// finished game returns false and triggers no side effects. Children are deleted
// before the game row to respect foreign-key constraints.
func DiscardGame(ctx context.Context, db *sql.DB, gameID int64) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var finishedAt sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT finished_at FROM games WHERE id = ?", gameID).Scan(&finishedAt)
	if err == sql.ErrNoRows {
		// Already gone: no-op.
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if finishedAt.Valid {
		// Finished games are never discarded.
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}

	for _, stmt := range []string{
		"DELETE FROM score_entries WHERE game_id = ?",
		"DELETE FROM game_players WHERE game_id = ?",
		"DELETE FROM game_winners WHERE game_id = ?",
		"DELETE FROM games WHERE id = ?",
	} {
		if _, err := tx.ExecContext(ctx, stmt, gameID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// UpdateGamePostID sets the Mattermost post id for a game.
func UpdateGamePostID(ctx context.Context, db *sql.DB, gameID int64, postID string) error {
	_, err := db.ExecContext(ctx, "UPDATE games SET post_id = ? WHERE id = ?", postID, gameID)
	return err
}

// BackfillLegacyChannel sets channelID on any legacy rows whose channel_id is
// still ” (migrated from Telegram). Idempotent: re-running after the backfill
// matches nothing. Covers both games and chat_last_players.
func BackfillLegacyChannel(ctx context.Context, db *sql.DB, channelID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx, "UPDATE games SET channel_id = ? WHERE channel_id = ''", channelID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE chat_last_players SET channel_id = ? WHERE channel_id = ''", channelID); err != nil {
		return err
	}
	return tx.Commit()
}

// AddPlayerToGame links a player to a game (idempotent).
func AddPlayerToGame(ctx context.Context, db *sql.DB, gameID, playerID int64) error {
	_, err := db.ExecContext(ctx,
		"INSERT OR IGNORE INTO game_players (game_id, player_id) VALUES (?, ?)", gameID, playerID)
	return err
}

// AddScoreEntry inserts a score entry and returns the persisted row.
func AddScoreEntry(ctx context.Context, db *sql.DB, gameID, playerID, points int64) (ScoreEntry, error) {
	res, err := db.ExecContext(ctx,
		"INSERT INTO score_entries (game_id, player_id, points) VALUES (?, ?, ?)", gameID, playerID, points)
	if err != nil {
		return ScoreEntry{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ScoreEntry{}, err
	}
	var e ScoreEntry
	err = db.QueryRowContext(ctx,
		"SELECT id, game_id, player_id, points, created_at FROM score_entries WHERE id = ?", id).
		Scan(&e.ID, &e.GameID, &e.PlayerID, &e.Points, &e.CreatedAt)
	if err != nil {
		return ScoreEntry{}, err
	}
	return e, nil
}

// GetLastScoreEntry returns the most recent score entry for a game (highest id),
// or nil if the game has none.
func GetLastScoreEntry(ctx context.Context, db *sql.DB, gameID int64) (*ScoreEntry, error) {
	var e ScoreEntry
	err := db.QueryRowContext(ctx,
		"SELECT id, game_id, player_id, points, created_at FROM score_entries WHERE game_id = ? ORDER BY id DESC LIMIT 1",
		gameID).Scan(&e.ID, &e.GameID, &e.PlayerID, &e.Points, &e.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// DeleteScoreEntryByID deletes the score entry with the given id belonging to
// gameID and reports whether a row was actually deleted. Targeting an explicit
// id (instead of "delete max-id") makes edit-last idempotent and replay-safe: a
// second call with the same id deletes nothing and returns false.
func DeleteScoreEntryByID(ctx context.Context, db *sql.DB, gameID, entryID int64) (bool, error) {
	res, err := db.ExecContext(ctx,
		"DELETE FROM score_entries WHERE id = ? AND game_id = ?", entryID, gameID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetGameScores returns each player's total score for a game.
func GetGameScores(ctx context.Context, db *sql.DB, gameID int64) (map[int64]int64, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT player_id, COALESCE(SUM(points), 0) AS total FROM score_entries WHERE game_id = ? GROUP BY player_id",
		gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]int64)
	for rows.Next() {
		var pid, total int64
		if err := rows.Scan(&pid, &total); err != nil {
			return nil, err
		}
		out[pid] = total
	}
	return out, rows.Err()
}

// GetGamePlayers returns the players linked to a game, ordered by name.
func GetGamePlayers(ctx context.Context, db *sql.DB, gameID int64) ([]Player, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT p.id, p.name, p.is_deleted, p.created_at, p.rating "+
			"FROM players p "+
			"JOIN game_players gp ON p.id = gp.player_id "+
			"WHERE gp.game_id = ? "+
			"ORDER BY p.name",
		gameID)
	if err != nil {
		return nil, err
	}
	return scanPlayers(rows)
}

// IsPlayerInUnfinishedGame reports whether the player is a participant in any
// unfinished game. Used to guard a hard-delete against players still in play.
func IsPlayerInUnfinishedGame(ctx context.Context, db *sql.DB, playerID int64) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM game_players gp JOIN games g ON g.id = gp.game_id WHERE gp.player_id = ? AND g.finished_at IS NULL)",
		playerID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists != 0, nil
}

// FinishedGameResult is the persisted outcome of a finished game, read back to
// re-render its win screen without recomputing Elo. WinnerIDs are the
// game_winners rows; Deltas are the per-player rating_history rows for the game.
type FinishedGameResult struct {
	WinnerIDs []int64
	Deltas    []rating.EloDelta
}

// GetFinishedGameResult reads back a finished game's persisted winners and
// rating deltas so a replayed/stale end-confirm rebuilds the win screen from
// stored values rather than recomputing Elo over already-updated ratings.
func GetFinishedGameResult(ctx context.Context, db *sql.DB, gameID int64) (FinishedGameResult, error) {
	var res FinishedGameResult

	wrows, err := db.QueryContext(ctx,
		"SELECT player_id FROM game_winners WHERE game_id = ? ORDER BY player_id", gameID)
	if err != nil {
		return FinishedGameResult{}, err
	}
	func() {
		defer wrows.Close()
		for wrows.Next() {
			var pid int64
			if err = wrows.Scan(&pid); err != nil {
				return
			}
			res.WinnerIDs = append(res.WinnerIDs, pid)
		}
	}()
	if err != nil {
		return FinishedGameResult{}, err
	}
	if err := wrows.Err(); err != nil {
		return FinishedGameResult{}, err
	}

	drows, err := db.QueryContext(ctx,
		"SELECT player_id, rating_before, rating_after, delta FROM rating_history WHERE game_id = ? ORDER BY id", gameID)
	if err != nil {
		return FinishedGameResult{}, err
	}
	defer drows.Close()
	for drows.Next() {
		var d rating.EloDelta
		if err := drows.Scan(&d.PlayerID, &d.RatingBefore, &d.RatingAfter, &d.Delta); err != nil {
			return FinishedGameResult{}, err
		}
		res.Deltas = append(res.Deltas, d)
	}
	if err := drows.Err(); err != nil {
		return FinishedGameResult{}, err
	}
	return res, nil
}

// statsAggregate is the raw per-player aggregate row shared by GetPlayerStats and
// GetAllStats.
type statsAggregate struct {
	id           int64
	name         string
	rating       float64
	games        int64
	wins         int64
	totalPoints  int64
	highestScore int64
}

// statsSelect is the common SELECT projecting one aggregate row per player; the
// caller appends its own WHERE / GROUP BY.
const statsSelect = `SELECT
            p.id,
            p.name,
            p.rating,
            COUNT(DISTINCT gp.game_id) AS games,
            COUNT(DISTINCT g_win.id) AS wins,
            COALESCE(SUM(se.points), 0) AS total_points,
            COALESCE(MAX(sub.game_total), 0) AS highest_score
         FROM players p
         LEFT JOIN game_players gp ON gp.player_id = p.id
         LEFT JOIN games g ON g.id = gp.game_id AND g.finished_at IS NOT NULL
         LEFT JOIN game_winners gw ON gw.game_id = g.id AND gw.player_id = p.id
         LEFT JOIN games g_win ON g_win.id = gw.game_id
         LEFT JOIN score_entries se ON se.game_id = g.id AND se.player_id = p.id
         LEFT JOIN (
             SELECT game_id, player_id, SUM(points) AS game_total
             FROM score_entries
             GROUP BY game_id, player_id
         ) sub ON sub.game_id = g.id AND sub.player_id = p.id`

// statsFromAggregate derives a PlayerStats from a raw aggregate row, matching the
// Rust semantics: losses = games - wins; win_rate = wins/games; avg_score =
// total_points/games (0 when games == 0).
func statsFromAggregate(a statsAggregate) PlayerStats {
	losses := a.games - a.wins
	var winRate, avgScore float64
	if a.games > 0 {
		winRate = float64(a.wins) / float64(a.games)
		avgScore = float64(a.totalPoints) / float64(a.games)
	}
	return PlayerStats{
		PlayerID:     a.id,
		PlayerName:   a.name,
		Games:        a.games,
		Wins:         a.wins,
		Losses:       losses,
		WinRate:      winRate,
		AvgScore:     avgScore,
		HighestScore: a.highestScore,
		TotalPoints:  a.totalPoints,
		Rating:       a.rating,
	}
}

// GetPlayerStats returns the computed statistics for a single player. Only
// finished games count toward the aggregates.
func GetPlayerStats(ctx context.Context, db *sql.DB, playerID int64) (PlayerStats, error) {
	var a statsAggregate
	err := db.QueryRowContext(ctx,
		statsSelect+" WHERE p.id = ? GROUP BY p.id, p.name, p.rating", playerID).
		Scan(&a.id, &a.name, &a.rating, &a.games, &a.wins, &a.totalPoints, &a.highestScore)
	if err != nil {
		return PlayerStats{}, err
	}
	return statsFromAggregate(a), nil
}

// RenamePlayer changes a player's name.
func RenamePlayer(ctx context.Context, db *sql.DB, playerID int64, newName string) error {
	_, err := db.ExecContext(ctx, "UPDATE players SET name = ? WHERE id = ?", newName, playerID)
	return err
}

// NameExists reports whether any player (including soft-deleted) other than
// excludePlayerID already has the given name. It pre-empts the UNIQUE index on
// players.name. Pass excludePlayerID = 0 to check against every player (no
// exclusion), since player ids start at 1.
func NameExists(ctx context.Context, db *sql.DB, name string, excludePlayerID int64) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM players WHERE name = ? AND id != ?)", name, excludePlayerID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists != 0, nil
}

// HardDeletePlayer permanently removes a player and all of its references, in a
// single transaction. Game winner pointers are NULLed rather than deleted.
func HardDeletePlayer(ctx context.Context, db *sql.DB, playerID int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	for _, stmt := range []string{
		"DELETE FROM score_entries WHERE player_id = ?",
		"DELETE FROM game_players WHERE player_id = ?",
		"DELETE FROM game_winners WHERE player_id = ?",
		"DELETE FROM rating_history WHERE player_id = ?",
		"DELETE FROM chat_last_players WHERE player_id = ?",
		"UPDATE games SET winner_player_id = NULL WHERE winner_player_id = ?",
		"DELETE FROM players WHERE id = ?",
	} {
		if _, err := tx.ExecContext(ctx, stmt, playerID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveLastPlayers replaces the channel's remembered last-players set with
// playerIDs, in a single transaction. The variable-length insert is built from
// bound `?` placeholders.
func SaveLastPlayers(ctx context.Context, db *sql.DB, channelID string, playerIDs []int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx, "DELETE FROM chat_last_players WHERE channel_id = ?", channelID); err != nil {
		return err
	}
	for _, pid := range playerIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO chat_last_players (channel_id, player_id) VALUES (?, ?)", channelID, pid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetLastPlayers returns the player ids remembered for a channel.
func GetLastPlayers(ctx context.Context, db *sql.DB, channelID string) ([]int64, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT player_id FROM chat_last_players WHERE channel_id = ?", channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetAllStats returns statistics for every active (non-soft-deleted) player,
// ranked for the Hall of Fame: rating descending, with case-insensitive name
// ascending as a stable tiebreaker.
func GetAllStats(ctx context.Context, db *sql.DB) ([]PlayerStats, error) {
	rows, err := db.QueryContext(ctx,
		statsSelect+" WHERE p.is_deleted = 0 GROUP BY p.id, p.name, p.rating")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []PlayerStats
	for rows.Next() {
		var a statsAggregate
		if err := rows.Scan(&a.id, &a.name, &a.rating, &a.games, &a.wins, &a.totalPoints, &a.highestScore); err != nil {
			return nil, err
		}
		stats = append(stats, statsFromAggregate(a))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].Rating != stats[j].Rating {
			return stats[i].Rating > stats[j].Rating
		}
		return strings.ToLower(stats[i].PlayerName) < strings.ToLower(stats[j].PlayerName)
	})
	return stats, nil
}
