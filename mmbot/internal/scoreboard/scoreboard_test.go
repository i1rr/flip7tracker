package scoreboard

import (
	"testing"

	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/rating"
)

func TestPadName(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"short padded", "Alice", 15, "Alice.........."},
		{"exact width", "Abcdefghijklmno", 15, "Abcdefghijklmno"},
		{"truncated", "ThisNameIsWayTooLong", 15, "ThisNameIsWayTo"},
		{"empty", "", 5, "....."},
		{"unicode runes", "Renée", 15, "Renée.........."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := padName(tt.in, tt.width); got != tt.want {
				t.Errorf("padName(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}

func TestRenderScoreboard(t *testing.T) {
	players := []db.Player{
		{ID: 1, Name: "Alice"},
		{ID: 2, Name: "Bob"},
		{ID: 3, Name: "Carol"},
		{ID: 4, Name: "Dave"},
	}
	scores := map[int64]int64{1: 30, 2: 95, 3: 95, 4: 0}

	got := RenderScoreboard(scores, players, 7)

	// Bob and Carol tie at 95; stable sort preserves input order (Bob first).
	want := "```\n" +
		"🎮 Flip 7 — Game #7\n" +
		"\n" +
		"🥇 1. Bob............ 95 pts\n" +
		"🥈 2. Carol.......... 95 pts\n" +
		"🥉 3. Alice.......... 30 pts\n" +
		"   4. Dave........... 0 pts\n" +
		"\n" +
		"🏁 First to 200 wins!\n" +
		"```"

	if got != want {
		t.Errorf("RenderScoreboard mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderScoreboardMissingScoreDefaultsZero(t *testing.T) {
	players := []db.Player{{ID: 1, Name: "Solo"}}
	got := RenderScoreboard(map[int64]int64{}, players, 1)
	want := "```\n" +
		"🎮 Flip 7 — Game #1\n" +
		"\n" +
		"🥇 1. Solo........... 0 pts\n" +
		"\n" +
		"🏁 First to 200 wins!\n" +
		"```"
	if got != want {
		t.Errorf("RenderScoreboard mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildWinTextSingleWinner(t *testing.T) {
	players := []db.Player{
		{ID: 1, Name: "Alice"},
		{ID: 2, Name: "Bob"},
	}
	scores := map[int64]int64{1: 210, 2: 150}
	deltas := []rating.EloDelta{
		{PlayerID: 1, RatingBefore: 1000, RatingAfter: 1012, Delta: 12},
		{PlayerID: 2, RatingBefore: 1000, RatingAfter: 988, Delta: -12},
	}

	got := BuildWinText([]string{"Alice"}, 210, scores, players, deltas)

	want := "🏆 WINNER: Alice with 210 pts!\n" +
		"\n" +
		"Final Scores:\n" +
		"🥇 Alice   210 pts   (+12 → 1012)\n" +
		"🥈 Bob   150 pts   (-12 → 988)\n" +
		"\n" +
		"📈 Rating changes shown in parentheses."

	if got != want {
		t.Errorf("BuildWinText mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildWinTextJointWinners(t *testing.T) {
	players := []db.Player{
		{ID: 1, Name: "Alice"},
		{ID: 2, Name: "Bob"},
		{ID: 3, Name: "Carol"},
	}
	scores := map[int64]int64{1: 200, 2: 200, 3: 100}
	deltas := []rating.EloDelta{
		{PlayerID: 1, RatingAfter: 1006, Delta: 6},
		{PlayerID: 2, RatingAfter: 1006, Delta: 6},
		{PlayerID: 3, RatingAfter: 988, Delta: -12},
	}

	got := BuildWinText([]string{"Alice", "Bob"}, 200, scores, players, deltas)

	want := "🏆 JOINT WINNERS: Alice, Bob — tied at 200 pts!\n" +
		"\n" +
		"Final Scores:\n" +
		"🥇 Alice   200 pts   (+6 → 1006)\n" +
		"🥈 Bob   200 pts   (+6 → 1006)\n" +
		"🥉 Carol   100 pts   (-12 → 988)\n" +
		"\n" +
		"📈 Rating changes shown in parentheses."

	if got != want {
		t.Errorf("BuildWinText mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildWinTextNoDeltaForPlayer(t *testing.T) {
	players := []db.Player{{ID: 1, Name: "Solo"}}
	scores := map[int64]int64{1: 200}
	got := BuildWinText([]string{"Solo"}, 200, scores, players, nil)
	want := "🏆 WINNER: Solo with 200 pts!\n" +
		"\n" +
		"Final Scores:\n" +
		"🥇 Solo   200 pts\n" +
		"\n" +
		"📈 Rating changes shown in parentheses."
	if got != want {
		t.Errorf("BuildWinText mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderHallOfFameEmpty(t *testing.T) {
	if got := RenderHallOfFame(nil, 0); got != "No statistics yet. Finish some games first!" {
		t.Errorf("unexpected empty render: %q", got)
	}
}

func TestRenderHallOfFame(t *testing.T) {
	stats := []db.PlayerStats{
		{PlayerID: 1, PlayerName: "Alice", Games: 10, Wins: 6, WinRate: 0.6, AvgScore: 120, Rating: 1080},
		{PlayerID: 2, PlayerName: "Bob", Games: 8, Wins: 3, WinRate: 0.375, AvgScore: 95, Rating: 1010},
		{PlayerID: 3, PlayerName: "Charlie", Games: 5, Wins: 1, WinRate: 0.2, AvgScore: 60, Rating: 970},
		{PlayerID: 4, PlayerName: "AnExtremelyLongPlayerName", Games: 2, Wins: 0, WinRate: 0.0, AvgScore: 30, Rating: 940},
	}

	got := RenderHallOfFame(stats, 0)

	want := "```\n" +
		"📊 Flip 7 — Hall of Fame\n" +
		"(ranked by Elo rating — start at 1000, win to climb)\n" +
		"\n" +
		"#   Player         G   W  Win%   Avg    Elo\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		"🥇 1  Alice         10   6   60%   120   1080\n" +
		"🥈 2  Bob            8   3   38%    95   1010\n" +
		"🥉 3  Charlie        5   1   20%    60    970\n" +
		"   4  AnExtremelyL   2   0    0%    30    940\n" +
		"```"

	if got != want {
		t.Errorf("RenderHallOfFame mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderHallOfFameSecondPage(t *testing.T) {
	stats := make([]db.PlayerStats, 13)
	for i := range stats {
		stats[i] = db.PlayerStats{PlayerID: int64(i + 1), PlayerName: "P", Games: 1, Wins: 0, WinRate: 0, AvgScore: 0, Rating: 1000}
	}

	got := RenderHallOfFame(stats, 1)

	// Page 1 (zero-based) holds ranks 11–13; none are top-3, so all use the
	// blank prefix and one-based rank numbers continue from 11.
	want := "```\n" +
		"📊 Flip 7 — Hall of Fame\n" +
		"(ranked by Elo rating — start at 1000, win to climb)\n" +
		"\n" +
		"#   Player         G   W  Win%   Avg    Elo\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		"   11 P              1   0    0%     0   1000\n" +
		"   12 P              1   0    0%     0   1000\n" +
		"   13 P              1   0    0%     0   1000\n" +
		"```"

	if got != want {
		t.Errorf("RenderHallOfFame page 1 mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderPlayerDetail(t *testing.T) {
	stat := db.PlayerStats{
		PlayerName:   "Alice",
		Games:        10,
		Wins:         6,
		Losses:       4,
		WinRate:      0.6,
		HighestScore: 215,
		AvgScore:     120,
		TotalPoints:  1200,
		Rating:       1080,
	}

	got := RenderPlayerDetail(stat)

	want := "👤 Alice\n" +
		"\n" +
		"Games played:   10\n" +
		"Wins:            6  (60%)\n" +
		"Losses:          4\n" +
		"Highest score:  215 pts\n" +
		"Avg per game:   120 pts\n" +
		"Total pts ever: 1200 pts\n" +
		"Elo rating:     1080"

	if got != want {
		t.Errorf("RenderPlayerDetail mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}
