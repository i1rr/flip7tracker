package scoreboard

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/rating"
)

// fence is a Markdown code fence. Wrapping monospace-aligned output in a fence
// replaces Telegram's implicit <pre> so column padding survives Mattermost's
// proportional rendering.
const fence = "```"

// medals holds the rank emoji for the top three positions (ranks 1–3).
var medals = [3]string{"🥇", "🥈", "🥉"}

// wrapFence wraps a pre-built monospace body in a Markdown code fence.
func wrapFence(body string) string {
	return fence + "\n" + body + "\n" + fence
}

// padName left-aligns name to exactly width runes: shorter names are padded on
// the right with '.', longer names are truncated. This mirrors Rust's
// `{name:.<15}` and additionally truncates (names are already validated to
// exclude backticks/newlines/control chars, so they cannot break the fence).
func padName(name string, width int) string {
	r := []rune(name)
	if len(r) > width {
		return string(r[:width])
	}
	return name + strings.Repeat(".", width-len(r))
}

// RenderScoreboard renders the live scoreboard for a game as a fenced
// Mattermost code block. Players are sorted by score descending (stable across
// ties, preserving the input order). Ranks 1–3 get medals; 4+ get a numeric
// prefix. Names are dot-padded/truncated to 15 columns.
func RenderScoreboard(scores map[int64]int64, players []db.Player, gameID int64) string {
	type row struct {
		player db.Player
		score  int64
	}
	rows := make([]row, len(players))
	for i, p := range players {
		rows[i] = row{player: p, score: scores[p.ID]}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].score > rows[j].score
	})

	lines := make([]string, 0, len(rows)+4)
	lines = append(lines, fmt.Sprintf("🎮 Flip 7 — Game #%d", gameID), "")
	for i, r := range rows {
		var rank string
		if i < 3 {
			rank = fmt.Sprintf("%s %d.", medals[i], i+1)
		} else {
			rank = fmt.Sprintf("   %d.", i+1)
		}
		lines = append(lines, fmt.Sprintf("%s %s %d pts", rank, padName(r.player.Name, 15), r.score))
	}
	lines = append(lines, "", "🏁 First to 200 wins!")
	return wrapFence(strings.Join(lines, "\n"))
}

// BuildWinText renders the end-of-game win screen as plain text (no fence, to
// match the original). It shows a single or joint winner header, the final
// scores with medals and Elo deltas, and a footer. Players are sorted by score
// descending.
func BuildWinText(winnerNames []string, winnerScore int64, scores map[int64]int64, players []db.Player, deltas []rating.EloDelta) string {
	type row struct {
		player db.Player
		score  int64
	}
	rows := make([]row, len(players))
	for i, p := range players {
		rows[i] = row{player: p, score: scores[p.ID]}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].score > rows[j].score
	})

	var header string
	if len(winnerNames) == 1 {
		header = fmt.Sprintf("🏆 WINNER: %s with %d pts!", winnerNames[0], winnerScore)
	} else {
		header = fmt.Sprintf("🏆 JOINT WINNERS: %s — tied at %d pts!", strings.Join(winnerNames, ", "), winnerScore)
	}

	lines := make([]string, 0, len(rows)+5)
	lines = append(lines, header, "", "Final Scores:")
	for i, r := range rows {
		medal := "  "
		if i < 3 {
			medal = medals[i]
		}
		deltaStr := ""
		for _, d := range deltas {
			if d.PlayerID == r.player.ID {
				sign := ""
				if d.Delta >= 0.0 {
					sign = "+"
				}
				deltaStr = fmt.Sprintf("   (%s%.0f → %.0f)", sign, d.Delta, d.RatingAfter)
				break
			}
		}
		lines = append(lines, fmt.Sprintf("%s %s   %d pts%s", medal, r.player.Name, r.score, deltaStr))
	}
	lines = append(lines, "", "📈 Rating changes shown in parentheses.")
	return strings.Join(lines, "\n")
}

// hallOfFamePerPage is the number of players shown per Hall-of-Fame page.
const hallOfFamePerPage = 10

// RenderHallOfFame renders one page of the Hall of Fame leaderboard as a fenced
// Mattermost code block. stats must already be sorted by rating descending (the
// query layer does this). page is zero-based; out-of-range pages render an empty
// table body. An empty stats slice yields the plain "no statistics yet" notice.
func RenderHallOfFame(stats []db.PlayerStats, page int) string {
	if len(stats) == 0 {
		return "No statistics yet. Finish some games first!"
	}

	start := page * hallOfFamePerPage
	if start < 0 {
		start = 0
	}
	end := start + hallOfFamePerPage
	if start > len(stats) {
		start = len(stats)
	}
	if end > len(stats) {
		end = len(stats)
	}
	pageStats := stats[start:end]

	header := "📊 Flip 7 — Hall of Fame\n(ranked by Elo rating — start at 1000, win to climb)\n\n"
	colHeader := fmt.Sprintf("%-3s %-12s %3s %3s %5s %5s %6s\n", "#", "Player", "G", "W", "Win%", "Avg", "Elo")
	separator := strings.Repeat("━", len([]rune(colHeader))-1) + "\n"

	var rows strings.Builder
	for i, s := range pageStats {
		rank := start + i
		prefix := "  "
		if rank < 3 {
			prefix = medals[rank]
		}
		rankNum := fmt.Sprintf("%d", rank+1)
		winPct := fmt.Sprintf("%.0f%%", s.WinRate*100.0)
		avg := fmt.Sprintf("%.0f", s.AvgScore)
		ratingStr := fmt.Sprintf("%.0f", s.Rating)
		name := truncRunes(s.PlayerName, 12)
		rows.WriteString(fmt.Sprintf(
			"%s %-2s %-12s %3d %3d %5s %5s %6s\n",
			prefix, rankNum, name, s.Games, s.Wins, winPct, avg, ratingStr,
		))
	}

	// The body already ends with a newline (every row + the separator do), so
	// the closing fence follows directly rather than via wrapFence (which would
	// inject a spurious blank line before it).
	return fence + "\n" + header + colHeader + separator + rows.String() + fence
}

// RenderPlayerDetail renders a single player's detail card as plain text,
// preserving the original column layout.
func RenderPlayerDetail(stat db.PlayerStats) string {
	return fmt.Sprintf(
		"👤 %s\n\nGames played:   %d\nWins:            %d  (%.0f%%)\nLosses:          %d\nHighest score:  %d pts\nAvg per game:   %.0f pts\nTotal pts ever: %d pts\nElo rating:     %.0f",
		stat.PlayerName, stat.Games, stat.Wins, stat.WinRate*100.0,
		stat.Losses, stat.HighestScore, stat.AvgScore, stat.TotalPoints, stat.Rating,
	)
}

// truncRunes returns the first width runes of s (or s unchanged if shorter),
// mirroring Rust's `s.chars().take(width)`.
func truncRunes(s string, width int) string {
	r := []rune(s)
	if len(r) > width {
		return string(r[:width])
	}
	return s
}
