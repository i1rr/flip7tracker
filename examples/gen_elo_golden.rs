//! Golden-fixture generator for the Go<->Rust Elo parity test.
//!
//! It includes the canonical Rust Elo implementation (src/utils/rating.rs)
//! directly via #[path] and emits the exact `compute_updates` output for a set
//! of cases as JSON, so the Go port (mmbot/internal/rating) can be tested for
//! numeric parity against the Rust source of truth.
//!
//! Floats are printed with Rust's `{:?}` formatting, which yields the shortest
//! decimal that round-trips to the identical f64; Go's json decoder parses that
//! decimal back to the same bit pattern, giving exact cross-language parity.
//!
//! Regenerate the committed fixture with:
//!   cargo run --example gen_elo_golden > mmbot/internal/rating/testdata/elo_golden.json

#[path = "../src/utils/rating.rs"]
mod rating;

use rating::{compute_updates, EloEntry};

struct Case {
    name: &'static str,
    entries: Vec<EloEntry>,
}

fn e(id: i64, score: i64, rating: f64) -> EloEntry {
    EloEntry { player_id: id, score, rating }
}

fn main() {
    let cases = vec![
        Case {
            name: "two_equal_players_winner",
            entries: vec![e(1, 200, 1000.0), e(2, 150, 1000.0)],
        },
        Case {
            name: "equal_scores_draw",
            entries: vec![e(1, 200, 1000.0), e(2, 200, 1000.0)],
        },
        Case {
            name: "favourite_beats_underdog",
            entries: vec![e(1, 210, 1300.0), e(2, 100, 1000.0)],
        },
        Case {
            name: "upset_underdog_wins",
            entries: vec![e(1, 220, 1000.0), e(2, 200, 1300.0)],
        },
        Case {
            name: "three_player_distinct_scores",
            entries: vec![e(1, 250, 1000.0), e(2, 200, 1000.0), e(3, 150, 1000.0)],
        },
        Case {
            name: "joint_winners_split",
            entries: vec![e(1, 210, 1000.0), e(2, 210, 1000.0), e(3, 180, 1000.0)],
        },
        Case {
            name: "mixed_ratings_four_players_with_tie",
            entries: vec![
                e(1, 240, 800.0),
                e(2, 240, 1400.0),
                e(3, 100, 1000.0),
                e(4, 50, 1100.0),
            ],
        },
        Case {
            name: "non_default_ratings_three_players",
            entries: vec![e(1, 220, 987.5), e(2, 180, 1042.3), e(3, 100, 1100.0)],
        },
        Case {
            name: "single_player_no_change",
            entries: vec![e(7, 200, 1234.5)],
        },
        Case {
            name: "empty_input",
            entries: vec![],
        },
        Case {
            name: "five_player_mixed_with_joint_winners",
            entries: vec![
                e(11, 205, 1150.0),
                e(22, 205, 980.0),
                e(33, 190, 1075.0),
                e(44, 130, 1000.0),
                e(55, 130, 1320.0),
            ],
        },
    ];

    let mut out = String::new();
    out.push_str("{\n");
    out.push_str("  \"_generated_by\": \"cargo run --example gen_elo_golden > mmbot/internal/rating/testdata/elo_golden.json\",\n");
    out.push_str("  \"cases\": [\n");
    for (ci, c) in cases.iter().enumerate() {
        let deltas = compute_updates(&c.entries);
        out.push_str("    {\n");
        out.push_str(&format!("      \"name\": \"{}\",\n", c.name));
        out.push_str("      \"entries\": [");
        for (i, en) in c.entries.iter().enumerate() {
            if i > 0 {
                out.push_str(", ");
            }
            out.push_str(&format!(
                "{{\"player_id\": {}, \"score\": {}, \"rating\": {:?}}}",
                en.player_id, en.score, en.rating
            ));
        }
        out.push_str("],\n");
        out.push_str("      \"expected\": [");
        for (i, d) in deltas.iter().enumerate() {
            if i > 0 {
                out.push_str(", ");
            }
            out.push_str(&format!(
                "{{\"player_id\": {}, \"rating_before\": {:?}, \"rating_after\": {:?}, \"delta\": {:?}}}",
                d.player_id, d.rating_before, d.rating_after, d.delta
            ));
        }
        out.push_str("]\n");
        out.push_str("    }");
        if ci + 1 < cases.len() {
            out.push(',');
        }
        out.push('\n');
    }
    out.push_str("  ]\n");
    out.push_str("}\n");

    print!("{}", out);
}
