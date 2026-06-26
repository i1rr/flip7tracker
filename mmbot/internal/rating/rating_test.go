package rating

import (
	"math"
	"testing"
)

// These tests are a verbatim port of the #[cfg(test)] module in
// src/utils/rating.rs. They must stay numerically identical to the Rust
// originals; the cross-language golden parity test lives in golden_test.go.

func approxEq(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

func entry(id, score int64, rating float64) EloEntry {
	return EloEntry{PlayerID: id, Score: score, Rating: rating}
}

func TestTwoEqualPlayersWinnerGainsHalfK(t *testing.T) {
	// r_a = r_b = 1000, A scores higher → expected = 0.5, K = 24
	// delta_a = 24 * (1 - 0.5) = 12
	res := ComputeUpdates([]EloEntry{
		entry(1, 200, 1000.0),
		entry(2, 150, 1000.0),
	})
	if !approxEq(res[0].Delta, 12.0, 1e-9) {
		t.Fatalf("res[0].Delta = %v, want 12.0", res[0].Delta)
	}
	if !approxEq(res[1].Delta, -12.0, 1e-9) {
		t.Fatalf("res[1].Delta = %v, want -12.0", res[1].Delta)
	}
}

func TestEqualScoresMeansZeroDelta(t *testing.T) {
	res := ComputeUpdates([]EloEntry{
		entry(1, 200, 1000.0),
		entry(2, 200, 1000.0),
	})
	if !approxEq(res[0].Delta, 0.0, 1e-9) {
		t.Fatalf("res[0].Delta = %v, want 0.0", res[0].Delta)
	}
	if !approxEq(res[1].Delta, 0.0, 1e-9) {
		t.Fatalf("res[1].Delta = %v, want 0.0", res[1].Delta)
	}
}

func TestFavouriteBeatsUnderdogSmallGain(t *testing.T) {
	// 1300 vs 1000, favourite wins.
	// Expected for 1300 = 1 / (1 + 10^(-0.75)) ≈ 0.849021
	// delta = 24 × (1 − 0.849021) ≈ 3.6235
	res := ComputeUpdates([]EloEntry{
		entry(1, 210, 1300.0),
		entry(2, 100, 1000.0),
	})
	expectedForA := expected(1300.0, 1000.0)
	expectedDelta := 24.0 * (1.0 - expectedForA)
	if !approxEq(res[0].Delta, expectedDelta, 1e-9) {
		t.Fatalf("res[0].Delta = %v, want %v", res[0].Delta, expectedDelta)
	}
	if !approxEq(res[1].Delta, -expectedDelta, 1e-9) {
		t.Fatalf("res[1].Delta = %v, want %v", res[1].Delta, -expectedDelta)
	}
	// Sanity: the favourite's gain is small (well under half of K).
	if !(res[0].Delta > 0.0 && res[0].Delta < KFactor/4.0) {
		t.Fatalf("favourite gain %v not in (0, K/4)", res[0].Delta)
	}
}

func TestUpsetUnderdogWinsBigGain(t *testing.T) {
	// 1000 vs 1300, underdog wins.
	// Expected for 1000 ≈ 0.150979
	// delta_winner ≈ 24 × (1 − 0.150979) ≈ 20.3765
	res := ComputeUpdates([]EloEntry{
		entry(1, 220, 1000.0),
		entry(2, 200, 1300.0),
	})
	expectedForUnderdog := expected(1000.0, 1300.0)
	expectedDelta := 24.0 * (1.0 - expectedForUnderdog)
	if !approxEq(res[0].Delta, expectedDelta, 1e-9) {
		t.Fatalf("res[0].Delta = %v, want %v", res[0].Delta, expectedDelta)
	}
	if !approxEq(res[1].Delta, -expectedDelta, 1e-9) {
		t.Fatalf("res[1].Delta = %v, want %v", res[1].Delta, -expectedDelta)
	}
	// Sanity: upset gain is large (more than 3/4 of K).
	if !(res[0].Delta > KFactor*0.75) {
		t.Fatalf("upset gain %v not > 0.75K", res[0].Delta)
	}
}

func TestThreePlayerDistinctScores(t *testing.T) {
	// K_per_pair = 12. All equal-rated.
	// 1st: wins both pairs → +6 + +6 = +12
	// 2nd: loses to 1st (−6), beats 3rd (+6) → 0
	// 3rd: loses both → −12
	res := ComputeUpdates([]EloEntry{
		entry(1, 250, 1000.0),
		entry(2, 200, 1000.0),
		entry(3, 150, 1000.0),
	})
	if !approxEq(res[0].Delta, 12.0, 1e-9) {
		t.Fatalf("res[0].Delta = %v, want 12.0", res[0].Delta)
	}
	if !approxEq(res[1].Delta, 0.0, 1e-9) {
		t.Fatalf("res[1].Delta = %v, want 0.0", res[1].Delta)
	}
	if !approxEq(res[2].Delta, -12.0, 1e-9) {
		t.Fatalf("res[2].Delta = %v, want -12.0", res[2].Delta)
	}
}

func TestJointWinnersSplitAgainstEachOther(t *testing.T) {
	// A and B tie at 210, C trails at 180. All 1000.
	// A vs B: 0,0
	// A vs C: A wins → +6 / -6
	// B vs C: B wins → +6 / -6
	// ⇒ A: +6, B: +6, C: -12
	res := ComputeUpdates([]EloEntry{
		entry(1, 210, 1000.0),
		entry(2, 210, 1000.0),
		entry(3, 180, 1000.0),
	})
	if !approxEq(res[0].Delta, 6.0, 1e-9) {
		t.Fatalf("res[0].Delta = %v, want 6.0", res[0].Delta)
	}
	if !approxEq(res[1].Delta, 6.0, 1e-9) {
		t.Fatalf("res[1].Delta = %v, want 6.0", res[1].Delta)
	}
	if !approxEq(res[2].Delta, -12.0, 1e-9) {
		t.Fatalf("res[2].Delta = %v, want -12.0", res[2].Delta)
	}
}

func TestConservationHoldsForArbitraryGames(t *testing.T) {
	// Sum of deltas must be ≈ 0 regardless of player count, scores, or
	// pre-existing ratings. Total rating is conserved across the game.
	cases := [][]EloEntry{
		{entry(1, 200, 1000.0), entry(2, 150, 1100.0)},
		{
			entry(1, 220, 1050.0),
			entry(2, 180, 950.0),
			entry(3, 140, 1200.0),
		},
		{
			entry(1, 240, 800.0),
			entry(2, 240, 1400.0),
			entry(3, 100, 1000.0),
			entry(4, 50, 1100.0),
		},
		{
			entry(1, 200, 1000.0),
			entry(2, 200, 1000.0),
			entry(3, 200, 1000.0),
		}, // three-way tie
	}
	for _, c := range cases {
		res := ComputeUpdates(c)
		var sum float64
		for _, d := range res {
			sum += d.Delta
		}
		if !approxEq(sum, 0.0, 1e-9) {
			t.Fatalf("conservation failed for %v: sum = %v", c, sum)
		}
	}
}

func TestSinglePlayerNoChange(t *testing.T) {
	res := ComputeUpdates([]EloEntry{entry(1, 200, 1000.0)})
	if len(res) != 1 {
		t.Fatalf("len(res) = %d, want 1", len(res))
	}
	if !approxEq(res[0].Delta, 0.0, 1e-9) {
		t.Fatalf("res[0].Delta = %v, want 0.0", res[0].Delta)
	}
	if !approxEq(res[0].RatingAfter, 1000.0, 1e-9) {
		t.Fatalf("res[0].RatingAfter = %v, want 1000.0", res[0].RatingAfter)
	}
}

func TestEmptyInputReturnsEmpty(t *testing.T) {
	res := ComputeUpdates([]EloEntry{})
	if len(res) != 0 {
		t.Fatalf("len(res) = %d, want 0", len(res))
	}
}

func TestRatingAfterEqualsBeforePlusDelta(t *testing.T) {
	res := ComputeUpdates([]EloEntry{
		entry(1, 220, 987.5),
		entry(2, 180, 1042.3),
		entry(3, 100, 1100.0),
	})
	for _, d := range res {
		if !approxEq(d.RatingAfter, d.RatingBefore+d.Delta, 1e-9) {
			t.Fatalf("rating_after %v != before+delta %v", d.RatingAfter, d.RatingBefore+d.Delta)
		}
	}
}

func TestOrderPreserved(t *testing.T) {
	// Output must be in same order as input.
	res := ComputeUpdates([]EloEntry{
		entry(42, 200, 1000.0),
		entry(7, 150, 1000.0),
		entry(13, 100, 1000.0),
	})
	if res[0].PlayerID != 42 {
		t.Fatalf("res[0].PlayerID = %d, want 42", res[0].PlayerID)
	}
	if res[1].PlayerID != 7 {
		t.Fatalf("res[1].PlayerID = %d, want 7", res[1].PlayerID)
	}
	if res[2].PlayerID != 13 {
		t.Fatalf("res[2].PlayerID = %d, want 13", res[2].PlayerID)
	}
}

func TestKFactorScalesWithPlayerCount(t *testing.T) {
	// Two players: K_per_pair = 24. Top gets +12.
	r2 := ComputeUpdates([]EloEntry{entry(1, 200, 1000.0), entry(2, 100, 1000.0)})
	// Four players, top vs bottom: per-pair K = 8; top wins all 3 pairs
	// against equal-rated opponents → +12.
	r4 := ComputeUpdates([]EloEntry{
		entry(1, 250, 1000.0),
		entry(2, 200, 1000.0),
		entry(3, 150, 1000.0),
		entry(4, 100, 1000.0),
	})
	// Top player's gain in a sweep should be the same magnitude regardless
	// of N when everyone is equal-rated — this is the whole point of
	// dividing K by (N−1).
	if !approxEq(r2[0].Delta, r4[0].Delta, 1e-9) {
		t.Fatalf("r2[0].Delta = %v, r4[0].Delta = %v", r2[0].Delta, r4[0].Delta)
	}
}
