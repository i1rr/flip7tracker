package rating

import "math"

// KFactor is the Elo K-factor: how aggressively ratings move per game. 24 is
// mid-volatility — stable enough for a casual leaderboard, responsive enough
// that new players settle within ~10 games. New players start at 1000 (set by
// the schema default on players.rating in migration 004).
//
// Ported verbatim from the Rust K_FACTOR in src/utils/rating.rs.
const KFactor = 24.0

// eloDivisor is the Elo logistic divisor (the classic 400 in the expected-score
// formula). baseRating (1000) is the schema default for a new player's rating;
// it is documented here for parity but the computation itself never references
// it — ratings are always supplied per entry.
const (
	eloDivisor = 400.0
	baseRating = 1000.0
)

// EloEntry is a single player's input to an Elo update for one finished game:
// their id, their final score in that game, and their rating going in.
type EloEntry struct {
	PlayerID int64
	Score    int64
	Rating   float64
}

// expected returns the Elo expected score of a player rated rSelf against an
// opponent rated rOpp: 1 / (1 + 10^((rOpp - rSelf) / 400)).
func expected(rSelf, rOpp float64) float64 {
	return 1.0 / (1.0 + math.Pow(10, (rOpp-rSelf)/eloDivisor))
}

// ComputeUpdates computes Elo deltas for a single finished game.
//
// It returns one EloDelta per input entry, in the same order. The game's final
// ranking (by score, ties allowed) is decomposed into pairwise matchups: for
// every (i, j) pair the higher-scoring player "beat" the lower one (1.0 / 0.0),
// and equal scores count as a draw (0.5 / 0.5). The K-factor is divided by
// (N-1) so a game's overall rating impact is roughly independent of the player
// count, and pairwise updates always conserve total rating (sum of deltas ≈ 0).
//
// Ported verbatim from compute_updates in src/utils/rating.rs.
func ComputeUpdates(entries []EloEntry) []EloDelta {
	n := len(entries)
	if n < 2 {
		out := make([]EloDelta, n)
		for i, e := range entries {
			out[i] = EloDelta{
				PlayerID:     e.PlayerID,
				RatingBefore: e.Rating,
				RatingAfter:  e.Rating,
				Delta:        0.0,
			}
		}
		return out
	}

	kPerPair := KFactor / (float64(n) - 1.0)
	deltas := make([]float64, n)

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			eI := expected(entries[i].Rating, entries[j].Rating)
			eJ := 1.0 - eI

			var aI, aJ float64
			switch {
			case entries[i].Score > entries[j].Score:
				aI, aJ = 1.0, 0.0
			case entries[i].Score < entries[j].Score:
				aI, aJ = 0.0, 1.0
			default:
				aI, aJ = 0.5, 0.5
			}

			deltas[i] += kPerPair * (aI - eI)
			deltas[j] += kPerPair * (aJ - eJ)
		}
	}

	out := make([]EloDelta, n)
	for idx, e := range entries {
		out[idx] = EloDelta{
			PlayerID:     e.PlayerID,
			RatingBefore: e.Rating,
			RatingAfter:  e.Rating + deltas[idx],
			Delta:        deltas[idx],
		}
	}
	return out
}
