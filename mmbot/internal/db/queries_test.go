package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/rivan/flip7bot/mmbot/internal/rating"
)

// newTestDB returns a freshly-migrated database backed by a temp file (modernc's
// :memory: gives each pooled connection its own DB, so a file is used instead).
func newTestDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	ctx := context.Background()
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return ctx, sqlDB
}

func TestCreateOrGetPlayer(t *testing.T) {
	ctx, db := newTestDB(t)

	a1, err := CreateOrGetPlayer(ctx, db, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if a1.Name != "Alice" || a1.Rating != 1000.0 || a1.IsDeleted {
		t.Fatalf("unexpected player: %+v", a1)
	}

	// Same name returns the same row.
	a2, err := CreateOrGetPlayer(ctx, db, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if a2.ID != a1.ID {
		t.Fatalf("expected same id %d, got %d", a1.ID, a2.ID)
	}

	// Soft-delete then re-create un-deletes the same row.
	if _, err := db.ExecContext(ctx, "UPDATE players SET is_deleted = 1 WHERE id = ?", a1.ID); err != nil {
		t.Fatal(err)
	}
	a3, err := CreateOrGetPlayer(ctx, db, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if a3.ID != a1.ID || a3.IsDeleted {
		t.Fatalf("expected un-deleted same row, got %+v", a3)
	}
}

func TestGetAllActivePlayers(t *testing.T) {
	ctx, db := newTestDB(t)
	for _, n := range []string{"Bob", "Alice", "Carol"} {
		if _, err := CreateOrGetPlayer(ctx, db, n); err != nil {
			t.Fatal(err)
		}
	}
	// Soft-delete Carol; she must be excluded.
	if _, err := db.ExecContext(ctx, "UPDATE players SET is_deleted = 1 WHERE name = 'Carol'"); err != nil {
		t.Fatal(err)
	}
	players, err := GetAllActivePlayers(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 2 || players[0].Name != "Alice" || players[1].Name != "Bob" {
		t.Fatalf("expected [Alice Bob] ordered, got %+v", players)
	}
}

func TestFinishGameWithWinnersIdempotent(t *testing.T) {
	ctx, db := newTestDB(t)
	alice, _ := CreateOrGetPlayer(ctx, db, "Alice")
	bob, _ := CreateOrGetPlayer(ctx, db, "Bob")

	game, err := CreateGame(ctx, db, "chan1")
	if err != nil {
		t.Fatal(err)
	}
	if game.ChannelID != "chan1" || game.PostID.Valid {
		t.Fatalf("unexpected game: %+v", game)
	}
	for _, p := range []int64{alice.ID, bob.ID} {
		if err := AddPlayerToGame(ctx, db, game.ID, p); err != nil {
			t.Fatal(err)
		}
	}

	deltas := []rating.EloDelta{
		{PlayerID: alice.ID, RatingBefore: 1000, RatingAfter: 1012, Delta: 12},
		{PlayerID: bob.ID, RatingBefore: 1000, RatingAfter: 988, Delta: -12},
	}

	ok, err := FinishGameWithWinners(ctx, db, game.ID, []int64{alice.ID}, deltas)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("first finish should return true")
	}
	assertRatingEq(t, ctx, db, alice.ID, 1012)
	assertRatingEq(t, ctx, db, bob.ID, 988)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM game_winners WHERE game_id = ?", 1, game.ID)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM rating_history WHERE game_id = ?", 2, game.ID)

	// Second call is a no-op: returns false, mutates nothing.
	ok, err = FinishGameWithWinners(ctx, db, game.ID, []int64{alice.ID},
		[]rating.EloDelta{
			{PlayerID: alice.ID, RatingBefore: 1012, RatingAfter: 1099, Delta: 87},
			{PlayerID: bob.ID, RatingBefore: 988, RatingAfter: 900, Delta: -88},
		})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second finish should return false (idempotent)")
	}
	assertRatingEq(t, ctx, db, alice.ID, 1012)
	assertRatingEq(t, ctx, db, bob.ID, 988)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM rating_history WHERE game_id = ?", 2, game.ID)
}

func TestFinishGameWithMultipleWinners(t *testing.T) {
	ctx, db := newTestDB(t)
	a, _ := CreateOrGetPlayer(ctx, db, "A")
	b, _ := CreateOrGetPlayer(ctx, db, "B")
	game, _ := CreateGame(ctx, db, "c")

	ok, err := FinishGameWithWinners(ctx, db, game.ID, []int64{a.ID, b.ID}, nil)
	if err != nil || !ok {
		t.Fatalf("finish: ok=%v err=%v", ok, err)
	}
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM game_winners WHERE game_id = ?", 2, game.ID)
	// Primary winner is the first id.
	var primary int64
	if err := db.QueryRowContext(ctx, "SELECT winner_player_id FROM games WHERE id = ?", game.ID).Scan(&primary); err != nil {
		t.Fatal(err)
	}
	if primary != a.ID {
		t.Fatalf("primary winner = %d, want %d", primary, a.ID)
	}
}

func TestDiscardGameIdempotent(t *testing.T) {
	ctx, db := newTestDB(t)
	a, _ := CreateOrGetPlayer(ctx, db, "A")
	game, _ := CreateGame(ctx, db, "c")
	if err := AddPlayerToGame(ctx, db, game.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := AddScoreEntry(ctx, db, game.ID, a.ID, 50); err != nil {
		t.Fatal(err)
	}

	ok, err := DiscardGame(ctx, db, game.ID)
	if err != nil || !ok {
		t.Fatalf("first discard ok=%v err=%v", ok, err)
	}
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM games WHERE id = ?", 0, game.ID)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM score_entries WHERE game_id = ?", 0, game.ID)

	// Replay: nothing left, returns false.
	ok, err = DiscardGame(ctx, db, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second discard should return false")
	}
}

func TestDiscardGameRefusesFinished(t *testing.T) {
	ctx, db := newTestDB(t)
	game, _ := CreateGame(ctx, db, "c")
	if _, err := FinishGameWithWinners(ctx, db, game.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	ok, err := DiscardGame(ctx, db, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("finished game must not be discarded")
	}
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM games WHERE id = ?", 1, game.ID)
}

func TestScoreEntriesAndDeleteByID(t *testing.T) {
	ctx, db := newTestDB(t)
	a, _ := CreateOrGetPlayer(ctx, db, "A")
	game, _ := CreateGame(ctx, db, "c")

	if last, err := GetLastScoreEntry(ctx, db, game.ID); err != nil || last != nil {
		t.Fatalf("expected no last entry, got %+v err=%v", last, err)
	}

	e1, err := AddScoreEntry(ctx, db, game.ID, a.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := AddScoreEntry(ctx, db, game.ID, a.ID, 20)
	if err != nil {
		t.Fatal(err)
	}

	last, err := GetLastScoreEntry(ctx, db, game.ID)
	if err != nil || last == nil || last.ID != e2.ID {
		t.Fatalf("expected last = %d, got %+v err=%v", e2.ID, last, err)
	}

	scores, err := GetGameScores(ctx, db, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if scores[a.ID] != 30 {
		t.Fatalf("expected total 30, got %d", scores[a.ID])
	}

	// Delete by id is idempotent.
	ok, err := DeleteScoreEntryByID(ctx, db, game.ID, e1.ID)
	if err != nil || !ok {
		t.Fatalf("first delete ok=%v err=%v", ok, err)
	}
	ok, err = DeleteScoreEntryByID(ctx, db, game.ID, e1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second delete should return false")
	}
	// Wrong game id never deletes.
	ok, err = DeleteScoreEntryByID(ctx, db, game.ID+999, e2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("delete with mismatched game id must return false")
	}
}

func TestGetGamePlayersAndUnfinished(t *testing.T) {
	ctx, db := newTestDB(t)
	a, _ := CreateOrGetPlayer(ctx, db, "Alice")
	b, _ := CreateOrGetPlayer(ctx, db, "Bob")
	game, _ := CreateGame(ctx, db, "c")
	if err := AddPlayerToGame(ctx, db, game.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := AddPlayerToGame(ctx, db, game.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	players, err := GetGamePlayers(ctx, db, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 2 || players[0].Name != "Alice" || players[1].Name != "Bob" {
		t.Fatalf("expected [Alice Bob], got %+v", players)
	}

	in, err := IsPlayerInUnfinishedGame(ctx, db, a.ID)
	if err != nil || !in {
		t.Fatalf("expected Alice in unfinished game, in=%v err=%v", in, err)
	}
	unfinished, err := GetUnfinishedGames(ctx, db, "c")
	if err != nil || len(unfinished) != 1 {
		t.Fatalf("expected 1 unfinished, got %d err=%v", len(unfinished), err)
	}

	// Finish it: no longer unfinished.
	if _, err := FinishGameWithWinners(ctx, db, game.ID, []int64{a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	in, err = IsPlayerInUnfinishedGame(ctx, db, a.ID)
	if err != nil || in {
		t.Fatalf("expected Alice not in unfinished game, in=%v err=%v", in, err)
	}
}

func TestUpdateGamePostIDAndBackfill(t *testing.T) {
	ctx, db := newTestDB(t)
	game, _ := CreateGame(ctx, db, "")
	if err := UpdateGamePostID(ctx, db, game.ID, "post123"); err != nil {
		t.Fatal(err)
	}
	got, err := scanGame(db.QueryRowContext(ctx,
		"SELECT id, channel_id, post_id, started_at, finished_at, winner_player_id FROM games WHERE id = ?", game.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !got.PostID.Valid || got.PostID.String != "post123" {
		t.Fatalf("post id not set: %+v", got.PostID)
	}

	// Backfill legacy '' channel rows.
	a, _ := CreateOrGetPlayer(ctx, db, "A")
	if err := SaveLastPlayers(ctx, db, "", []int64{a.ID}); err != nil {
		t.Fatal(err)
	}
	if err := BackfillLegacyChannel(ctx, db, "realchan"); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM games WHERE channel_id = ''", 0)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM chat_last_players WHERE channel_id = ''", 0)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM games WHERE channel_id = 'realchan'", 1)
}

func TestLastPlayersRoundTrip(t *testing.T) {
	ctx, db := newTestDB(t)
	a, _ := CreateOrGetPlayer(ctx, db, "A")
	b, _ := CreateOrGetPlayer(ctx, db, "B")
	if err := SaveLastPlayers(ctx, db, "c", []int64{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := GetLastPlayers(ctx, db, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 last players, got %v", got)
	}
	// Replacing overwrites.
	if err := SaveLastPlayers(ctx, db, "c", []int64{a.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ = GetLastPlayers(ctx, db, "c")
	if len(got) != 1 || got[0] != a.ID {
		t.Fatalf("expected [%d], got %v", a.ID, got)
	}
}

func TestRenameAndNameExists(t *testing.T) {
	ctx, db := newTestDB(t)
	a, _ := CreateOrGetPlayer(ctx, db, "Alice")
	b, _ := CreateOrGetPlayer(ctx, db, "Bob")

	exists, err := NameExists(ctx, db, "Bob", a.ID)
	if err != nil || !exists {
		t.Fatalf("Bob should exist (excluding Alice), exists=%v err=%v", exists, err)
	}
	exists, _ = NameExists(ctx, db, "Bob", b.ID)
	if exists {
		t.Fatal("excluding Bob himself, name should not be considered taken")
	}
	exists, _ = NameExists(ctx, db, "Zoe", 0)
	if exists {
		t.Fatal("Zoe should not exist")
	}

	if err := RenamePlayer(ctx, db, a.ID, "Alicia"); err != nil {
		t.Fatal(err)
	}
	got, _ := scanPlayer(db.QueryRowContext(ctx,
		"SELECT id, name, is_deleted, created_at, rating FROM players WHERE id = ?", a.ID))
	if got.Name != "Alicia" {
		t.Fatalf("rename failed, got %q", got.Name)
	}
}

func TestHardDeletePlayer(t *testing.T) {
	ctx, db := newTestDB(t)
	a, _ := CreateOrGetPlayer(ctx, db, "A")
	b, _ := CreateOrGetPlayer(ctx, db, "B")
	game, _ := CreateGame(ctx, db, "c")
	for _, p := range []int64{a.ID, b.ID} {
		_ = AddPlayerToGame(ctx, db, game.ID, p)
		_, _ = AddScoreEntry(ctx, db, game.ID, p, 100)
	}
	if _, err := FinishGameWithWinners(ctx, db, game.ID, []int64{a.ID},
		[]rating.EloDelta{{PlayerID: a.ID, RatingBefore: 1000, RatingAfter: 1012, Delta: 12}}); err != nil {
		t.Fatal(err)
	}

	if err := HardDeletePlayer(ctx, db, a.ID); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM players WHERE id = ?", 0, a.ID)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM score_entries WHERE player_id = ?", 0, a.ID)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM game_players WHERE player_id = ?", 0, a.ID)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM game_winners WHERE player_id = ?", 0, a.ID)
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM rating_history WHERE player_id = ?", 0, a.ID)
	// Winner pointer NULLed, not the whole game.
	assertScalar(t, ctx, db, "SELECT COUNT(*) FROM games WHERE id = ? AND winner_player_id IS NULL", 1, game.ID)
}

func TestGetPlayerStatsAndGetFinishedGameResult(t *testing.T) {
	ctx, db := newTestDB(t)
	a, _ := CreateOrGetPlayer(ctx, db, "Alice")
	b, _ := CreateOrGetPlayer(ctx, db, "Bob")
	game, _ := CreateGame(ctx, db, "c")
	_ = AddPlayerToGame(ctx, db, game.ID, a.ID)
	_ = AddPlayerToGame(ctx, db, game.ID, b.ID)
	_, _ = AddScoreEntry(ctx, db, game.ID, a.ID, 210)
	_, _ = AddScoreEntry(ctx, db, game.ID, b.ID, 150)
	deltas := []rating.EloDelta{
		{PlayerID: a.ID, RatingBefore: 1000, RatingAfter: 1012, Delta: 12},
		{PlayerID: b.ID, RatingBefore: 1000, RatingAfter: 988, Delta: -12},
	}
	if _, err := FinishGameWithWinners(ctx, db, game.ID, []int64{a.ID}, deltas); err != nil {
		t.Fatal(err)
	}

	st, err := GetPlayerStats(ctx, db, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Games != 1 || st.Wins != 1 || st.Losses != 0 || st.HighestScore != 210 || st.TotalPoints != 210 {
		t.Fatalf("unexpected stats: %+v", st)
	}
	if st.WinRate != 1.0 || st.AvgScore != 210.0 || st.Rating != 1012.0 {
		t.Fatalf("unexpected derived stats: %+v", st)
	}

	bStats, _ := GetPlayerStats(ctx, db, b.ID)
	if bStats.Wins != 0 || bStats.Losses != 1 || bStats.WinRate != 0.0 {
		t.Fatalf("unexpected loser stats: %+v", bStats)
	}

	// Finished-game read-back reflects persisted winners + deltas.
	res, err := GetFinishedGameResult(ctx, db, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.WinnerIDs) != 1 || res.WinnerIDs[0] != a.ID {
		t.Fatalf("unexpected winners: %+v", res.WinnerIDs)
	}
	if len(res.Deltas) != 2 {
		t.Fatalf("expected 2 deltas, got %d", len(res.Deltas))
	}
}

func TestGetAllStatsSorting(t *testing.T) {
	ctx, db := newTestDB(t)
	alice, _ := CreateOrGetPlayer(ctx, db, "Alice")
	bob, _ := CreateOrGetPlayer(ctx, db, "Bob")
	_, _ = CreateOrGetPlayer(ctx, db, "Carol") // stays at default 1000

	game, _ := CreateGame(ctx, db, "c")
	_ = AddPlayerToGame(ctx, db, game.ID, alice.ID)
	_ = AddPlayerToGame(ctx, db, game.ID, bob.ID)
	_, _ = AddScoreEntry(ctx, db, game.ID, alice.ID, 150)
	_, _ = AddScoreEntry(ctx, db, game.ID, bob.ID, 220)
	deltas := []rating.EloDelta{
		{PlayerID: bob.ID, RatingBefore: 1000, RatingAfter: 1012, Delta: 12},
		{PlayerID: alice.ID, RatingBefore: 1000, RatingAfter: 988, Delta: -12},
	}
	if _, err := FinishGameWithWinners(ctx, db, game.ID, []int64{bob.ID}, deltas); err != nil {
		t.Fatal(err)
	}

	stats, err := GetAllStats(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	// Order: Bob (1012), Carol (1000, alphabetically before Alice), Alice (988).
	if len(stats) != 3 {
		t.Fatalf("expected 3 stats, got %d", len(stats))
	}
	if stats[0].PlayerName != "Bob" || stats[1].PlayerName != "Carol" || stats[2].PlayerName != "Alice" {
		t.Fatalf("unexpected order: %s %s %s", stats[0].PlayerName, stats[1].PlayerName, stats[2].PlayerName)
	}
}

// --- helpers ---

func assertRatingEq(t *testing.T, ctx context.Context, db *sql.DB, playerID int64, want float64) {
	t.Helper()
	var got float64
	if err := db.QueryRowContext(ctx, "SELECT rating FROM players WHERE id = ?", playerID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("rating for %d = %v, want %v", playerID, got, want)
	}
}

func assertScalar(t *testing.T, ctx context.Context, db *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %q = %d, want %d", query, got, want)
	}
}
