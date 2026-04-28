use std::collections::HashMap;
use sqlx::SqlitePool;
use crate::db::models::{Game, Player, PlayerStats, ScoreEntry};

pub async fn create_or_get_player(pool: &SqlitePool, name: &str) -> Result<Player, sqlx::Error> {
    sqlx::query("INSERT OR IGNORE INTO players (name) VALUES (?)")
        .bind(name)
        .execute(pool)
        .await?;
    // Un-delete if soft-deleted
    sqlx::query("UPDATE players SET is_deleted = 0 WHERE name = ? AND is_deleted = 1")
        .bind(name)
        .execute(pool)
        .await?;
    sqlx::query_as::<_, Player>(
        "SELECT id, name, is_deleted, created_at, rating FROM players WHERE name = ?"
    )
    .bind(name)
    .fetch_one(pool)
    .await
}

pub async fn get_all_active_players(pool: &SqlitePool) -> Result<Vec<Player>, sqlx::Error> {
    sqlx::query_as::<_, Player>(
        "SELECT id, name, is_deleted, created_at, rating FROM players WHERE is_deleted = 0 ORDER BY name"
    )
    .fetch_all(pool)
    .await
}


pub async fn create_game(pool: &SqlitePool, chat_id: i64) -> Result<Game, sqlx::Error> {
    let result = sqlx::query("INSERT INTO games (chat_id) VALUES (?)")
        .bind(chat_id)
        .execute(pool)
        .await?;
    let id = result.last_insert_rowid();
    sqlx::query_as::<_, Game>(
        "SELECT id, chat_id, message_id, started_at, finished_at, winner_player_id FROM games WHERE id = ?"
    )
    .bind(id)
    .fetch_one(pool)
    .await
}

pub async fn get_unfinished_games(pool: &SqlitePool, chat_id: i64) -> Result<Vec<Game>, sqlx::Error> {
    sqlx::query_as::<_, Game>(
        "SELECT id, chat_id, message_id, started_at, finished_at, winner_player_id FROM games WHERE chat_id = ? AND finished_at IS NULL ORDER BY started_at DESC"
    )
    .bind(chat_id)
    .fetch_all(pool)
    .await
}

/// Mark a game as finished, record its winners, and apply Elo rating updates
/// — all in one transaction so we never end up with a finished game whose
/// ratings haven't been updated (or vice versa).
///
/// `winner_ids` may contain one player (sole winner) or several (joint winners
/// who tied at the highest score ≥ 200). `elo_deltas` must include every
/// player who participated in the game (winners and losers alike).
pub async fn finish_game_with_winners(
    pool: &SqlitePool,
    game_id: i64,
    winner_ids: &[i64],
    elo_deltas: &[crate::utils::rating::EloDelta],
) -> Result<(), sqlx::Error> {
    let mut tx = pool.begin().await?;
    let primary = winner_ids.first().copied();
    sqlx::query(
        "UPDATE games SET finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), winner_player_id = ? WHERE id = ?"
    )
    .bind(primary)
    .bind(game_id)
    .execute(&mut *tx)
    .await?;
    for &pid in winner_ids {
        sqlx::query("INSERT OR IGNORE INTO game_winners (game_id, player_id) VALUES (?, ?)")
            .bind(game_id)
            .bind(pid)
            .execute(&mut *tx)
            .await?;
    }
    for d in elo_deltas {
        sqlx::query("UPDATE players SET rating = ? WHERE id = ?")
            .bind(d.rating_after)
            .bind(d.player_id)
            .execute(&mut *tx)
            .await?;
        sqlx::query(
            "INSERT INTO rating_history (game_id, player_id, rating_before, rating_after, delta) \
             VALUES (?, ?, ?, ?, ?)"
        )
        .bind(game_id)
        .bind(d.player_id)
        .bind(d.rating_before)
        .bind(d.rating_after)
        .bind(d.delta)
        .execute(&mut *tx)
        .await?;
    }
    tx.commit().await?;
    Ok(())
}

/// Discard an unfinished game completely — removes its score entries, player
/// links, and the game row itself. Used when the user ends a game before
/// anyone reached 200 (so it doesn't pollute statistics).
pub async fn discard_game(pool: &SqlitePool, game_id: i64) -> Result<(), sqlx::Error> {
    let mut tx = pool.begin().await?;
    sqlx::query("DELETE FROM score_entries WHERE game_id = ?").bind(game_id).execute(&mut *tx).await?;
    sqlx::query("DELETE FROM game_players WHERE game_id = ?").bind(game_id).execute(&mut *tx).await?;
    sqlx::query("DELETE FROM game_winners WHERE game_id = ?").bind(game_id).execute(&mut *tx).await?;
    sqlx::query("DELETE FROM games WHERE id = ?").bind(game_id).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(())
}

pub async fn update_game_message_id(pool: &SqlitePool, game_id: i64, message_id: i64) -> Result<(), sqlx::Error> {
    sqlx::query("UPDATE games SET message_id = ? WHERE id = ?")
        .bind(message_id)
        .bind(game_id)
        .execute(pool)
        .await?;
    Ok(())
}

pub async fn add_player_to_game(pool: &SqlitePool, game_id: i64, player_id: i64) -> Result<(), sqlx::Error> {
    sqlx::query("INSERT OR IGNORE INTO game_players (game_id, player_id) VALUES (?, ?)")
        .bind(game_id)
        .bind(player_id)
        .execute(pool)
        .await?;
    Ok(())
}

pub async fn add_score_entry(pool: &SqlitePool, game_id: i64, player_id: i64, points: i64) -> Result<ScoreEntry, sqlx::Error> {
    let result = sqlx::query("INSERT INTO score_entries (game_id, player_id, points) VALUES (?, ?, ?)")
        .bind(game_id)
        .bind(player_id)
        .bind(points)
        .execute(pool)
        .await?;
    let id = result.last_insert_rowid();
    sqlx::query_as::<_, ScoreEntry>(
        "SELECT id, game_id, player_id, points, created_at FROM score_entries WHERE id = ?"
    )
    .bind(id)
    .fetch_one(pool)
    .await
}

pub async fn delete_last_score_entry(pool: &SqlitePool, game_id: i64) -> Result<Option<ScoreEntry>, sqlx::Error> {
    let entry = sqlx::query_as::<_, ScoreEntry>(
        "SELECT id, game_id, player_id, points, created_at FROM score_entries WHERE game_id = ? ORDER BY id DESC LIMIT 1"
    )
    .bind(game_id)
    .fetch_optional(pool)
    .await?;

    if let Some(ref e) = entry {
        sqlx::query("DELETE FROM score_entries WHERE id = ?")
            .bind(e.id)
            .execute(pool)
            .await?;
    }
    Ok(entry)
}

pub async fn get_game_scores(pool: &SqlitePool, game_id: i64) -> Result<HashMap<i64, i64>, sqlx::Error> {
    let rows: Vec<(i64, i64)> = sqlx::query_as(
        "SELECT player_id, COALESCE(SUM(points), 0) as total FROM score_entries WHERE game_id = ? GROUP BY player_id"
    )
    .bind(game_id)
    .fetch_all(pool)
    .await?;
    Ok(rows.into_iter().collect())
}

pub async fn get_game_players(pool: &SqlitePool, game_id: i64) -> Result<Vec<Player>, sqlx::Error> {
    sqlx::query_as::<_, Player>(
        "SELECT p.id, p.name, p.is_deleted, p.created_at, p.rating \
         FROM players p \
         JOIN game_players gp ON p.id = gp.player_id \
         WHERE gp.game_id = ? \
         ORDER BY p.name"
    )
    .bind(game_id)
    .fetch_all(pool)
    .await
}

pub async fn get_player_stats(pool: &SqlitePool, player_id: i64) -> Result<PlayerStats, sqlx::Error> {
    // Aggregate row: (id, name, rating, games, wins, total_points, highest_score)
    let row: (i64, String, f64, i64, i64, i64, i64) = sqlx::query_as(
        "SELECT
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
         ) sub ON sub.game_id = g.id AND sub.player_id = p.id
         WHERE p.id = ?
         GROUP BY p.id, p.name, p.rating"
    )
    .bind(player_id)
    .fetch_one(pool)
    .await?;

    let (pid, name, rating, games, wins, total_points, highest_score) = row;
    let losses = games - wins;
    let win_rate = if games > 0 { wins as f64 / games as f64 } else { 0.0 };
    let avg_score = if games > 0 { total_points as f64 / games as f64 } else { 0.0 };

    Ok(PlayerStats {
        player_id: pid,
        player_name: name,
        games,
        wins,
        losses,
        win_rate,
        avg_score,
        highest_score,
        total_points,
        rating,
    })
}

pub async fn rename_player(pool: &SqlitePool, player_id: i64, new_name: &str) -> Result<(), sqlx::Error> {
    sqlx::query("UPDATE players SET name = ? WHERE id = ?")
        .bind(new_name)
        .bind(player_id)
        .execute(pool)
        .await?;
    Ok(())
}

pub async fn hard_delete_player(pool: &SqlitePool, player_id: i64) -> Result<(), sqlx::Error> {
    let mut tx = pool.begin().await?;
    sqlx::query("DELETE FROM score_entries WHERE player_id = ?").bind(player_id).execute(&mut *tx).await?;
    sqlx::query("DELETE FROM game_players WHERE player_id = ?").bind(player_id).execute(&mut *tx).await?;
    sqlx::query("DELETE FROM game_winners WHERE player_id = ?").bind(player_id).execute(&mut *tx).await?;
    sqlx::query("DELETE FROM rating_history WHERE player_id = ?").bind(player_id).execute(&mut *tx).await?;
    sqlx::query("DELETE FROM chat_last_players WHERE player_id = ?").bind(player_id).execute(&mut *tx).await?;
    sqlx::query("UPDATE games SET winner_player_id = NULL WHERE winner_player_id = ?").bind(player_id).execute(&mut *tx).await?;
    sqlx::query("DELETE FROM players WHERE id = ?").bind(player_id).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(())
}

pub async fn save_last_players(pool: &SqlitePool, chat_id: i64, player_ids: &[i64]) -> Result<(), sqlx::Error> {
    sqlx::query("DELETE FROM chat_last_players WHERE chat_id = ?")
        .bind(chat_id)
        .execute(pool)
        .await?;
    for &pid in player_ids {
        sqlx::query("INSERT OR IGNORE INTO chat_last_players (chat_id, player_id) VALUES (?, ?)")
            .bind(chat_id)
            .bind(pid)
            .execute(pool)
            .await?;
    }
    Ok(())
}

pub async fn get_last_players(pool: &SqlitePool, chat_id: i64) -> Result<Vec<i64>, sqlx::Error> {
    let rows: Vec<(i64,)> = sqlx::query_as(
        "SELECT player_id FROM chat_last_players WHERE chat_id = ?"
    )
    .bind(chat_id)
    .fetch_all(pool)
    .await?;
    Ok(rows.into_iter().map(|(id,)| id).collect())
}

pub async fn get_all_stats(pool: &SqlitePool) -> Result<Vec<PlayerStats>, sqlx::Error> {
    let rows: Vec<(i64, String, f64, i64, i64, i64, i64)> = sqlx::query_as(
        "SELECT
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
         ) sub ON sub.game_id = g.id AND sub.player_id = p.id
         WHERE p.is_deleted = 0
         GROUP BY p.id, p.name, p.rating"
    )
    .fetch_all(pool)
    .await?;

    let mut stats: Vec<PlayerStats> = rows
        .into_iter()
        .map(|(pid, name, rating, games, wins, total_points, highest_score)| {
            let losses = games - wins;
            let win_rate = if games > 0 { wins as f64 / games as f64 } else { 0.0 };
            let avg_score = if games > 0 { total_points as f64 / games as f64 } else { 0.0 };
            PlayerStats {
                player_id: pid,
                player_name: name,
                games,
                wins,
                losses,
                win_rate,
                avg_score,
                highest_score,
                total_points,
                rating,
            }
        })
        .collect();

    // Hall of Fame ranking: rating desc, with name asc as a stable tiebreaker.
    stats.sort_by(|a, b| {
        b.rating
            .partial_cmp(&a.rating)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then_with(|| a.player_name.to_lowercase().cmp(&b.player_name.to_lowercase()))
    });

    Ok(stats)
}

// ---------------------------------------------------------------------------
// Integration tests against a real (in-memory) SQLite database.
// ---------------------------------------------------------------------------
#[cfg(test)]
mod integration_tests {
    use super::*;
    use crate::utils::rating;
    use sqlx::sqlite::SqlitePoolOptions;

    async fn setup_pool() -> SqlitePool {
        // Single shared in-memory connection so all migrations + queries see
        // the same schema and data.
        let pool = SqlitePoolOptions::new()
            .max_connections(1)
            .connect("sqlite::memory:")
            .await
            .expect("create in-memory pool");
        sqlx::migrate!("./migrations")
            .run(&pool)
            .await
            .expect("run migrations");
        pool
    }

    async fn fetch_rating(pool: &SqlitePool, player_id: i64) -> f64 {
        let (r,): (f64,) = sqlx::query_as("SELECT rating FROM players WHERE id = ?")
            .bind(player_id)
            .fetch_one(pool)
            .await
            .expect("fetch rating");
        r
    }

    async fn run_finished_game(
        pool: &SqlitePool,
        chat_id: i64,
        scores: &[(i64, i64)], // (player_id, final_score)
        winners: &[i64],
    ) -> i64 {
        let game = create_game(pool, chat_id).await.expect("create game");
        for &(pid, _) in scores {
            add_player_to_game(pool, game.id, pid).await.expect("add player");
        }
        for &(pid, points) in scores {
            add_score_entry(pool, game.id, pid, points).await.expect("add score");
        }
        let players = get_game_players(pool, game.id).await.expect("game players");
        let score_map = get_game_scores(pool, game.id).await.expect("game scores");
        let entries: Vec<rating::EloEntry> = players
            .iter()
            .map(|p| rating::EloEntry {
                player_id: p.id,
                score: *score_map.get(&p.id).unwrap_or(&0),
                rating: p.rating,
            })
            .collect();
        let deltas = rating::compute_updates(&entries);
        finish_game_with_winners(pool, game.id, winners, &deltas)
            .await
            .expect("finish game");
        game.id
    }

    #[tokio::test]
    async fn new_players_start_at_1000() {
        let pool = setup_pool().await;
        let alice = create_or_get_player(&pool, "Alice").await.unwrap();
        let bob = create_or_get_player(&pool, "Bob").await.unwrap();
        assert!((alice.rating - 1000.0).abs() < 1e-9);
        assert!((bob.rating - 1000.0).abs() < 1e-9);
    }

    #[tokio::test]
    async fn two_player_game_persists_rating_change() {
        let pool = setup_pool().await;
        let alice = create_or_get_player(&pool, "Alice").await.unwrap();
        let bob = create_or_get_player(&pool, "Bob").await.unwrap();

        run_finished_game(
            &pool,
            42,
            &[(alice.id, 210), (bob.id, 150)],
            &[alice.id],
        )
        .await;

        // Equal-rated, K=24: winner +12, loser -12.
        let r_alice = fetch_rating(&pool, alice.id).await;
        let r_bob = fetch_rating(&pool, bob.id).await;
        assert!((r_alice - 1012.0).abs() < 1e-6, "alice rating = {}", r_alice);
        assert!((r_bob - 988.0).abs() < 1e-6, "bob rating = {}", r_bob);

        // Total rating is conserved across the game.
        assert!((r_alice + r_bob - 2000.0).abs() < 1e-6);
    }

    #[tokio::test]
    async fn joint_winners_each_gain_against_loser() {
        let pool = setup_pool().await;
        let alice = create_or_get_player(&pool, "Alice").await.unwrap();
        let bob = create_or_get_player(&pool, "Bob").await.unwrap();
        let carol = create_or_get_player(&pool, "Carol").await.unwrap();

        // Alice & Bob tie at 210 (joint winners). Carol trails at 180.
        let game_id = run_finished_game(
            &pool,
            42,
            &[(alice.id, 210), (bob.id, 210), (carol.id, 180)],
            &[alice.id, bob.id],
        )
        .await;

        let r_alice = fetch_rating(&pool, alice.id).await;
        let r_bob = fetch_rating(&pool, bob.id).await;
        let r_carol = fetch_rating(&pool, carol.id).await;

        // K_per_pair = 12. Alice vs Bob → tie (0). Each beats Carol → +6.
        // Carol loses both pairs → -12.
        assert!((r_alice - 1006.0).abs() < 1e-6);
        assert!((r_bob - 1006.0).abs() < 1e-6);
        assert!((r_carol - 988.0).abs() < 1e-6);

        // Both winners are recorded in game_winners.
        let winners: Vec<(i64,)> =
            sqlx::query_as("SELECT player_id FROM game_winners WHERE game_id = ? ORDER BY player_id")
                .bind(game_id)
                .fetch_all(&pool)
                .await
                .unwrap();
        assert_eq!(winners.len(), 2);
        assert_eq!(winners[0].0, alice.id);
        assert_eq!(winners[1].0, bob.id);
    }

    #[tokio::test]
    async fn discard_game_does_not_change_ratings() {
        let pool = setup_pool().await;
        let alice = create_or_get_player(&pool, "Alice").await.unwrap();
        let bob = create_or_get_player(&pool, "Bob").await.unwrap();

        let game = create_game(&pool, 42).await.unwrap();
        add_player_to_game(&pool, game.id, alice.id).await.unwrap();
        add_player_to_game(&pool, game.id, bob.id).await.unwrap();
        add_score_entry(&pool, game.id, alice.id, 150).await.unwrap();
        add_score_entry(&pool, game.id, bob.id, 100).await.unwrap();

        discard_game(&pool, game.id).await.unwrap();

        // Ratings untouched.
        assert!((fetch_rating(&pool, alice.id).await - 1000.0).abs() < 1e-9);
        assert!((fetch_rating(&pool, bob.id).await - 1000.0).abs() < 1e-9);

        // Game and its rows are gone.
        let game_count: (i64,) = sqlx::query_as("SELECT COUNT(*) FROM games WHERE id = ?")
            .bind(game.id)
            .fetch_one(&pool)
            .await
            .unwrap();
        assert_eq!(game_count.0, 0);
        let score_count: (i64,) = sqlx::query_as("SELECT COUNT(*) FROM score_entries WHERE game_id = ?")
            .bind(game.id)
            .fetch_one(&pool)
            .await
            .unwrap();
        assert_eq!(score_count.0, 0);
        let history_count: (i64,) = sqlx::query_as("SELECT COUNT(*) FROM rating_history WHERE game_id = ?")
            .bind(game.id)
            .fetch_one(&pool)
            .await
            .unwrap();
        assert_eq!(history_count.0, 0);
    }

    #[tokio::test]
    async fn rating_history_is_recorded_per_game() {
        let pool = setup_pool().await;
        let alice = create_or_get_player(&pool, "Alice").await.unwrap();
        let bob = create_or_get_player(&pool, "Bob").await.unwrap();

        let game_id =
            run_finished_game(&pool, 42, &[(alice.id, 210), (bob.id, 150)], &[alice.id]).await;

        let rows: Vec<(i64, f64, f64, f64)> = sqlx::query_as(
            "SELECT player_id, rating_before, rating_after, delta \
             FROM rating_history WHERE game_id = ? ORDER BY player_id"
        )
        .bind(game_id)
        .fetch_all(&pool)
        .await
        .unwrap();

        assert_eq!(rows.len(), 2);
        for (_, before, after, delta) in &rows {
            assert!((before + delta - after).abs() < 1e-9);
            assert!((before - 1000.0).abs() < 1e-9);
        }
    }

    #[tokio::test]
    async fn rating_drifts_over_multiple_games_for_consistent_winner() {
        let pool = setup_pool().await;
        let alice = create_or_get_player(&pool, "Alice").await.unwrap();
        let bob = create_or_get_player(&pool, "Bob").await.unwrap();

        // Alice wins three games in a row. Each win is worth less because she
        // is increasingly the "favoured" player — that's classic Elo behaviour.
        let mut prev_alice = 1000.0;
        let mut gains = Vec::new();
        for _ in 0..3 {
            run_finished_game(&pool, 42, &[(alice.id, 210), (bob.id, 100)], &[alice.id]).await;
            let r = fetch_rating(&pool, alice.id).await;
            gains.push(r - prev_alice);
            prev_alice = r;
        }
        // Each successive gain should be smaller (Elo's diminishing returns).
        assert!(gains[1] < gains[0], "expected diminishing returns: {:?}", gains);
        assert!(gains[2] < gains[1], "expected diminishing returns: {:?}", gains);

        // Conservation across all games.
        let sum = fetch_rating(&pool, alice.id).await + fetch_rating(&pool, bob.id).await;
        assert!((sum - 2000.0).abs() < 1e-6);
    }

    #[tokio::test]
    async fn get_all_stats_reads_stored_rating_and_sorts_by_it() {
        let pool = setup_pool().await;
        let alice = create_or_get_player(&pool, "Alice").await.unwrap();
        let bob = create_or_get_player(&pool, "Bob").await.unwrap();
        let _carol = create_or_get_player(&pool, "Carol").await.unwrap();

        // Bob wins a game vs Alice; Carol stays at default 1000.
        run_finished_game(&pool, 42, &[(alice.id, 150), (bob.id, 220)], &[bob.id]).await;

        let stats = get_all_stats(&pool).await.unwrap();
        assert_eq!(stats.len(), 3);
        // Sort order: Bob (1012), Carol (1000, alphabetically before Alice), Alice (988).
        assert_eq!(stats[0].player_name, "Bob");
        assert!((stats[0].rating - 1012.0).abs() < 1e-6);
        assert_eq!(stats[1].player_name, "Carol");
        assert!((stats[1].rating - 1000.0).abs() < 1e-9);
        assert_eq!(stats[2].player_name, "Alice");
        assert!((stats[2].rating - 988.0).abs() < 1e-6);
    }

    #[tokio::test]
    async fn upset_rewards_underdog_more_than_routine_win() {
        let pool = setup_pool().await;
        let strong = create_or_get_player(&pool, "Strong").await.unwrap();
        let weak = create_or_get_player(&pool, "Weak").await.unwrap();

        // Build a rating gap: Strong wins 5 games in a row.
        for _ in 0..5 {
            run_finished_game(&pool, 42, &[(strong.id, 210), (weak.id, 100)], &[strong.id]).await;
        }
        let r_strong_before = fetch_rating(&pool, strong.id).await;
        let r_weak_before = fetch_rating(&pool, weak.id).await;
        assert!(r_strong_before > r_weak_before + 50.0, "expected gap, got {} vs {}", r_strong_before, r_weak_before);

        // Now the underdog wins.
        run_finished_game(&pool, 42, &[(strong.id, 100), (weak.id, 210)], &[weak.id]).await;
        let r_weak_after = fetch_rating(&pool, weak.id).await;
        let upset_gain = r_weak_after - r_weak_before;

        // Upset gain should be > K/2 = 12 (would be exactly 12 if equal-rated).
        assert!(upset_gain > 12.0, "upset gain {} should exceed equal-rated gain", upset_gain);
    }

    #[tokio::test]
    async fn four_player_sweep_total_change_is_independent_of_player_count() {
        // Reaffirms the K/(N-1) normalization end-to-end through the DB.
        let pool = setup_pool().await;
        let p1 = create_or_get_player(&pool, "P1").await.unwrap();
        let p2 = create_or_get_player(&pool, "P2").await.unwrap();
        let p3 = create_or_get_player(&pool, "P3").await.unwrap();
        let p4 = create_or_get_player(&pool, "P4").await.unwrap();

        run_finished_game(
            &pool,
            42,
            &[(p1.id, 250), (p2.id, 200), (p3.id, 150), (p4.id, 100)],
            &[p1.id],
        )
        .await;

        let r1 = fetch_rating(&pool, p1.id).await;
        let r4 = fetch_rating(&pool, p4.id).await;
        // 4-player sweep with equal ratings: top wins 3 pairs at K/3=8 each → +24? No wait:
        // K_per_pair = K/(N-1) = 24/3 = 8. Top wins 3 pairs at +4 each (8*0.5=4) → +12.
        // Bottom loses 3 pairs at -4 each → -12. Same magnitude as a 2-player game.
        assert!((r1 - 1012.0).abs() < 1e-6, "top: {}", r1);
        assert!((r4 - 988.0).abs() < 1e-6, "bottom: {}", r4);
    }
}
