package rating

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Go<->Rust golden parity test (mirrors the jobHunter shared-fixture
// discipline): a single committed fixture, generated from the canonical Rust
// implementation, is the source of truth that both languages test against.
//
// testdata/elo_golden.json is generated from the real Rust compute_updates via
// examples/gen_elo_golden.rs. Regenerate it (after any change to the algorithm
// on either side) with, from the repo root:
//
//	cargo run --example gen_elo_golden > mmbot/internal/rating/testdata/elo_golden.json
//
// The fixture deliberately includes a multi-winner case (joint_winners_split,
// five_player_mixed_with_joint_winners) and an upset case (upset_underdog_wins)
// plus mixed non-default ratings, so the conservation and pairwise branches are
// all exercised against Rust's exact f64 output.

const goldenTolerance = 1e-9

type goldenEntry struct {
	PlayerID int64   `json:"player_id"`
	Score    int64   `json:"score"`
	Rating   float64 `json:"rating"`
}

type goldenDelta struct {
	PlayerID     int64   `json:"player_id"`
	RatingBefore float64 `json:"rating_before"`
	RatingAfter  float64 `json:"rating_after"`
	Delta        float64 `json:"delta"`
}

type goldenCase struct {
	Name     string        `json:"name"`
	Entries  []goldenEntry `json:"entries"`
	Expected []goldenDelta `json:"expected"`
}

type goldenFixture struct {
	Cases []goldenCase `json:"cases"`
}

func TestEloGoldenParityWithRust(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "elo_golden.json"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}

	var fixture goldenFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse golden fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("golden fixture has no cases")
	}

	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			entries := make([]EloEntry, len(c.Entries))
			for i, e := range c.Entries {
				entries[i] = EloEntry{PlayerID: e.PlayerID, Score: e.Score, Rating: e.Rating}
			}

			got := ComputeUpdates(entries)
			if len(got) != len(c.Expected) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(c.Expected))
			}

			for i, want := range c.Expected {
				g := got[i]
				if g.PlayerID != want.PlayerID {
					t.Errorf("[%d] PlayerID = %d, want %d", i, g.PlayerID, want.PlayerID)
				}
				if math.Abs(g.RatingBefore-want.RatingBefore) > goldenTolerance {
					t.Errorf("[%d] RatingBefore = %v, want %v", i, g.RatingBefore, want.RatingBefore)
				}
				if math.Abs(g.RatingAfter-want.RatingAfter) > goldenTolerance {
					t.Errorf("[%d] RatingAfter = %v, want %v", i, g.RatingAfter, want.RatingAfter)
				}
				if math.Abs(g.Delta-want.Delta) > goldenTolerance {
					t.Errorf("[%d] Delta = %v, want %v", i, g.Delta, want.Delta)
				}
			}
		})
	}
}
