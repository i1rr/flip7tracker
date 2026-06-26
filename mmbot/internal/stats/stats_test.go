package stats

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
	"github.com/rivan/flip7bot/mmbot/internal/rating"
)

const testKey = "0123456789abcdef0123456789abcdef" // 32 bytes
const testChannel = "chan1"

func newTestScreen(t *testing.T, opts ...Option) (*Screen, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(context.Background(), database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	signer, err := mm.NewSigner(testKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	b := nav.NewBuilder(signer, "http://bot.example:8068", nil)
	return New(database, b, nil, opts...), database
}

func seedPlayer(t *testing.T, database *sql.DB, name string) db.Player {
	t.Helper()
	pl, err := db.CreateOrGetPlayer(context.Background(), database, name)
	if err != nil {
		t.Fatalf("seed player %q: %v", name, err)
	}
	return pl
}

// finishGame plays a single finished game: every player gets one score entry and
// the highest scorer wins. Elo deltas are computed exactly as the live flow does.
func finishGame(t *testing.T, database *sql.DB, scores map[int64]int64) {
	t.Helper()
	ctx := context.Background()
	g, err := db.CreateGame(ctx, database, testChannel)
	if err != nil {
		t.Fatalf("create game: %v", err)
	}
	all, err := db.GetAllActivePlayers(ctx, database)
	if err != nil {
		t.Fatalf("get players: %v", err)
	}
	var entries []rating.EloEntry
	var winnerID int64
	var max int64 = -1
	for _, p := range all {
		sc, ok := scores[p.ID]
		if !ok {
			continue
		}
		if err := db.AddPlayerToGame(ctx, database, g.ID, p.ID); err != nil {
			t.Fatalf("add player: %v", err)
		}
		if _, err := db.AddScoreEntry(ctx, database, g.ID, p.ID, sc); err != nil {
			t.Fatalf("add score: %v", err)
		}
		entries = append(entries, rating.EloEntry{PlayerID: p.ID, Score: sc, Rating: p.Rating})
		if sc > max {
			max = sc
			winnerID = p.ID
		}
	}
	deltas := rating.ComputeUpdates(entries)
	if _, err := db.FinishGameWithWinners(ctx, database, g.ID, []int64{winnerID}, deltas); err != nil {
		t.Fatalf("finish game: %v", err)
	}
}

func buttonLabels(att []*model.SlackAttachment) []string {
	var out []string
	for _, a := range att {
		for _, act := range a.Actions {
			out = append(out, act.Name)
		}
	}
	return out
}

func hasLabel(att []*model.SlackAttachment, substr string) bool {
	for _, l := range buttonLabels(att) {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

func TestOwns(t *testing.T) {
	s, _ := newTestScreen(t)
	for _, a := range []string{mm.ActMenuStats, mm.ActStatsPlayer, mm.ActStatsPage, mm.ActStatsBack, mm.ActStatsBackToList} {
		if !s.Owns(a) {
			t.Errorf("should own %q", a)
		}
	}
	for _, a := range []string{mm.ActMenuPlayers, mm.ActMgmtRename, mm.ActScorePlayer, "", "garbage"} {
		if s.Owns(a) {
			t.Errorf("should not own %q", a)
		}
	}
}

func TestHallOfFameEmpty(t *testing.T) {
	s, database := newTestScreen(t)
	all, err := db.GetAllStats(context.Background(), database)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	msg, att := s.hallOfFameScreen(all, 0)
	if !strings.Contains(msg, "No statistics yet") {
		t.Errorf("empty body = %q", msg)
	}
	if !hasLabel(att, "Back") {
		t.Errorf("empty hall of fame must offer Back; got %v", buttonLabels(att))
	}
}

func TestHallOfFameRendersAndDrilldown(t *testing.T) {
	s, database := newTestScreen(t)
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")
	finishGame(t, database, map[int64]int64{a.ID: 210, b.ID: 150})

	all, err := db.GetAllStats(context.Background(), database)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	msg, att := s.hallOfFameScreen(all, 0)
	if !strings.Contains(msg, "Hall of Fame") {
		t.Errorf("hall of fame body missing header: %q", msg)
	}
	if !hasLabel(att, "Alice") || !hasLabel(att, "Bob") {
		t.Errorf("expected per-player drilldown buttons; got %v", buttonLabels(att))
	}
}

func TestHallOfFamePagination(t *testing.T) {
	s, database := newTestScreen(t)
	// 11 players each with one finished win => 2 pages of 10 + 1.
	for _, n := range []string{"p01", "p02", "p03", "p04", "p05", "p06", "p07", "p08", "p09", "p10", "p11"} {
		p := seedPlayer(t, database, n)
		finishGame(t, database, map[int64]int64{p.ID: 210})
	}
	all, err := db.GetAllStats(context.Background(), database)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(all) != 11 {
		t.Fatalf("expected 11 stat rows, got %d", len(all))
	}

	_, att0 := s.hallOfFameScreen(all, 0)
	if !hasLabel(att0, "Next") || hasLabel(att0, "Prev") {
		t.Errorf("page 0 nav wrong; got %v", buttonLabels(att0))
	}
	_, att1 := s.hallOfFameScreen(all, 1)
	if hasLabel(att1, "Next") || !hasLabel(att1, "Prev") {
		t.Errorf("page 1 nav wrong; got %v", buttonLabels(att1))
	}
	// Out-of-range page clamps rather than panicking.
	if _, attHi := s.hallOfFameScreen(all, 99); attHi == nil {
		t.Errorf("clamped page should still render")
	}
}

func TestPlayerDetail(t *testing.T) {
	s, database := newTestScreen(t)
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")
	finishGame(t, database, map[int64]int64{a.ID: 210, b.ID: 150})

	resp, err := s.Action(context.Background(), nil, mm.NavState{Action: mm.ActStatsPlayer, PlayerID: a.ID})
	if err != nil {
		t.Fatalf("player detail: %v", err)
	}
	if resp.Update == nil || !strings.Contains(resp.Update.Message, "Alice") {
		t.Errorf("detail should show the player name; got %+v", resp.Update)
	}
	if !strings.Contains(resp.Update.Message, "Elo rating") {
		t.Errorf("detail should show the stored Elo rating; got %q", resp.Update.Message)
	}
}

func TestPlayerDetailMissingFallsBackToList(t *testing.T) {
	s, _ := newTestScreen(t)
	// No such player => GetPlayerStats returns sql.ErrNoRows; the screen falls
	// back to the (empty) hall of fame rather than erroring.
	resp, err := s.Action(context.Background(), nil, mm.NavState{Action: mm.ActStatsPlayer, PlayerID: 999})
	if err != nil {
		t.Fatalf("player detail (missing): %v", err)
	}
	if resp.Update == nil {
		t.Errorf("expected a fallback render, got %+v", resp)
	}
}

func TestBackToMainMenu(t *testing.T) {
	called := false
	fallback := func() *model.PostActionIntegrationResponse {
		called = true
		return &model.PostActionIntegrationResponse{}
	}
	s, _ := newTestScreen(t, WithMainMenuRenderer(fallback))
	if _, err := s.Action(context.Background(), nil, mm.NavState{Action: mm.ActStatsBack}); err != nil {
		t.Fatalf("back: %v", err)
	}
	if !called {
		t.Errorf("stats:back should invoke the main-menu renderer")
	}
}
