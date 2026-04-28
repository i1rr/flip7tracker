//! Elo rating system adapted for multiplayer Flip 7 games.
//!
//! Each game's final ranking (by score, with ties allowed) is decomposed into
//! pairwise matchups: for every (i, j) pair, the higher-scoring player "beat"
//! the lower one (1.0 / 0.0), and equal scores count as a draw (0.5 / 0.5).
//! The K-factor is divided by (N − 1) so a game's overall rating impact is
//! roughly independent of the player count.

/// K-factor: how aggressively ratings move per game. 24 is mid-volatility —
/// stable enough for a casual leaderboard, responsive enough that new players
/// settle within ~10 games. (New players start at 1000, set by the schema
/// default on `players.rating` in migration 004.)
pub const K_FACTOR: f64 = 24.0;

#[derive(Debug, Clone, Copy)]
pub struct EloEntry {
    pub player_id: i64,
    pub score: i64,
    pub rating: f64,
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct EloDelta {
    pub player_id: i64,
    pub rating_before: f64,
    pub rating_after: f64,
    pub delta: f64,
}

fn expected(r_self: f64, r_opponent: f64) -> f64 {
    1.0 / (1.0 + 10f64.powf((r_opponent - r_self) / 400.0))
}

/// Compute Elo deltas for a single finished game.
///
/// Returns one EloDelta per input entry, in the same order. Pairwise updates
/// always conserve total rating (sum of deltas ≈ 0), so the leaderboard can't
/// inflate just because more games are played.
pub fn compute_updates(entries: &[EloEntry]) -> Vec<EloDelta> {
    let n = entries.len();
    if n < 2 {
        return entries
            .iter()
            .map(|e| EloDelta {
                player_id: e.player_id,
                rating_before: e.rating,
                rating_after: e.rating,
                delta: 0.0,
            })
            .collect();
    }

    let k_per_pair = K_FACTOR / (n as f64 - 1.0);
    let mut deltas = vec![0.0_f64; n];

    for i in 0..n {
        for j in (i + 1)..n {
            let e_i = expected(entries[i].rating, entries[j].rating);
            let e_j = 1.0 - e_i;

            let (a_i, a_j) = match entries[i].score.cmp(&entries[j].score) {
                std::cmp::Ordering::Greater => (1.0, 0.0),
                std::cmp::Ordering::Less => (0.0, 1.0),
                std::cmp::Ordering::Equal => (0.5, 0.5),
            };

            deltas[i] += k_per_pair * (a_i - e_i);
            deltas[j] += k_per_pair * (a_j - e_j);
        }
    }

    entries
        .iter()
        .enumerate()
        .map(|(idx, e)| EloDelta {
            player_id: e.player_id,
            rating_before: e.rating,
            rating_after: e.rating + deltas[idx],
            delta: deltas[idx],
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn approx_eq(a: f64, b: f64, eps: f64) -> bool {
        (a - b).abs() < eps
    }

    fn entry(id: i64, score: i64, rating: f64) -> EloEntry {
        EloEntry { player_id: id, score, rating }
    }

    #[test]
    fn two_equal_players_winner_gains_half_k() {
        // r_a = r_b = 1000, A scores higher → expected = 0.5, K = 24
        // delta_a = 24 * (1 - 0.5) = 12
        let res = compute_updates(&[
            entry(1, 200, 1000.0),
            entry(2, 150, 1000.0),
        ]);
        assert!(approx_eq(res[0].delta, 12.0, 1e-9));
        assert!(approx_eq(res[1].delta, -12.0, 1e-9));
    }

    #[test]
    fn equal_scores_means_zero_delta() {
        let res = compute_updates(&[
            entry(1, 200, 1000.0),
            entry(2, 200, 1000.0),
        ]);
        assert!(approx_eq(res[0].delta, 0.0, 1e-9));
        assert!(approx_eq(res[1].delta, 0.0, 1e-9));
    }

    #[test]
    fn favourite_beats_underdog_small_gain() {
        // 1300 vs 1000, favourite wins.
        // Expected for 1300 = 1 / (1 + 10^(-0.75)) ≈ 0.849021
        // delta = 24 × (1 − 0.849021) ≈ 3.6235
        let res = compute_updates(&[
            entry(1, 210, 1300.0),
            entry(2, 100, 1000.0),
        ]);
        let expected_for_a = expected(1300.0, 1000.0);
        let expected_delta = 24.0 * (1.0 - expected_for_a);
        assert!(approx_eq(res[0].delta, expected_delta, 1e-9));
        assert!(approx_eq(res[1].delta, -expected_delta, 1e-9));
        // Sanity: the favourite's gain is small (well under half of K).
        assert!(res[0].delta > 0.0 && res[0].delta < K_FACTOR / 4.0);
    }

    #[test]
    fn upset_underdog_wins_big_gain() {
        // 1000 vs 1300, underdog wins.
        // Expected for 1000 ≈ 0.150979
        // delta_winner ≈ 24 × (1 − 0.150979) ≈ 20.3765
        let res = compute_updates(&[
            entry(1, 220, 1000.0),
            entry(2, 200, 1300.0),
        ]);
        let expected_for_underdog = expected(1000.0, 1300.0);
        let expected_delta = 24.0 * (1.0 - expected_for_underdog);
        assert!(approx_eq(res[0].delta, expected_delta, 1e-9));
        assert!(approx_eq(res[1].delta, -expected_delta, 1e-9));
        // Sanity: upset gain is large (more than 3/4 of K).
        assert!(res[0].delta > K_FACTOR * 0.75);
    }

    #[test]
    fn three_player_distinct_scores() {
        // K_per_pair = 12. All equal-rated.
        // 1st: wins both pairs → +6 + +6 = +12
        // 2nd: loses to 1st (−6), beats 3rd (+6) → 0
        // 3rd: loses both → −12
        let res = compute_updates(&[
            entry(1, 250, 1000.0),
            entry(2, 200, 1000.0),
            entry(3, 150, 1000.0),
        ]);
        assert!(approx_eq(res[0].delta, 12.0, 1e-9));
        assert!(approx_eq(res[1].delta, 0.0, 1e-9));
        assert!(approx_eq(res[2].delta, -12.0, 1e-9));
    }

    #[test]
    fn joint_winners_split_against_each_other() {
        // A and B tie at 210, C trails at 180. All 1000.
        // A vs B: 0,0
        // A vs C: A wins → +6 / -6
        // B vs C: B wins → +6 / -6
        // ⇒ A: +6, B: +6, C: -12
        let res = compute_updates(&[
            entry(1, 210, 1000.0),
            entry(2, 210, 1000.0),
            entry(3, 180, 1000.0),
        ]);
        assert!(approx_eq(res[0].delta, 6.0, 1e-9));
        assert!(approx_eq(res[1].delta, 6.0, 1e-9));
        assert!(approx_eq(res[2].delta, -12.0, 1e-9));
    }

    #[test]
    fn conservation_holds_for_arbitrary_games() {
        // Sum of deltas must be ≈ 0 regardless of player count, scores, or
        // pre-existing ratings. Total rating is conserved across the game.
        let cases: Vec<Vec<EloEntry>> = vec![
            vec![entry(1, 200, 1000.0), entry(2, 150, 1100.0)],
            vec![
                entry(1, 220, 1050.0),
                entry(2, 180, 950.0),
                entry(3, 140, 1200.0),
            ],
            vec![
                entry(1, 240, 800.0),
                entry(2, 240, 1400.0),
                entry(3, 100, 1000.0),
                entry(4, 50, 1100.0),
            ],
            vec![
                entry(1, 200, 1000.0),
                entry(2, 200, 1000.0),
                entry(3, 200, 1000.0),
            ], // three-way tie
        ];
        for case in cases {
            let res = compute_updates(&case);
            let sum: f64 = res.iter().map(|d| d.delta).sum();
            assert!(
                approx_eq(sum, 0.0, 1e-9),
                "conservation failed for {:?}: sum = {}",
                case,
                sum
            );
        }
    }

    #[test]
    fn single_player_no_change() {
        let res = compute_updates(&[entry(1, 200, 1000.0)]);
        assert_eq!(res.len(), 1);
        assert!(approx_eq(res[0].delta, 0.0, 1e-9));
        assert!(approx_eq(res[0].rating_after, 1000.0, 1e-9));
    }

    #[test]
    fn empty_input_returns_empty() {
        let res = compute_updates(&[]);
        assert!(res.is_empty());
    }

    #[test]
    fn rating_after_equals_before_plus_delta() {
        let res = compute_updates(&[
            entry(1, 220, 987.5),
            entry(2, 180, 1042.3),
            entry(3, 100, 1100.0),
        ]);
        for d in &res {
            assert!(approx_eq(d.rating_after, d.rating_before + d.delta, 1e-9));
        }
    }

    #[test]
    fn order_preserved() {
        // Output must be in same order as input.
        let res = compute_updates(&[
            entry(42, 200, 1000.0),
            entry(7, 150, 1000.0),
            entry(13, 100, 1000.0),
        ]);
        assert_eq!(res[0].player_id, 42);
        assert_eq!(res[1].player_id, 7);
        assert_eq!(res[2].player_id, 13);
    }

    #[test]
    fn k_factor_scales_with_player_count() {
        // Two players: K_per_pair = 24. Top gets +12.
        let r2 = compute_updates(&[entry(1, 200, 1000.0), entry(2, 100, 1000.0)]);
        // Four players, top vs bottom: per-pair K = 8; top wins all 3 pairs
        // against equal-rated opponents → +12.
        let r4 = compute_updates(&[
            entry(1, 250, 1000.0),
            entry(2, 200, 1000.0),
            entry(3, 150, 1000.0),
            entry(4, 100, 1000.0),
        ]);
        // Top player's gain in a sweep should be the same magnitude regardless
        // of N when everyone is equal-rated — this is the whole point of
        // dividing K by (N−1).
        assert!(approx_eq(r2[0].delta, r4[0].delta, 1e-9));
    }
}
