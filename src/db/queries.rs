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
        "SELECT id, name, is_deleted, created_at FROM players WHERE name = ?"
    )
    .bind(name)
    .fetch_one(pool)
    .await
}

pub async fn get_all_active_players(pool: &SqlitePool) -> Result<Vec<Player>, sqlx::Error> {
    sqlx::query_as::<_, Player>(
        "SELECT id, name, is_deleted, created_at FROM players WHERE is_deleted = 0 ORDER BY name"
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

pub async fn finish_game(pool: &SqlitePool, game_id: i64, winner_id: Option<i64>) -> Result<(), sqlx::Error> {
    sqlx::query(
        "UPDATE games SET finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), winner_player_id = ? WHERE id = ?"
    )
    .bind(winner_id)
    .bind(game_id)
    .execute(pool)
    .await?;
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
        "SELECT p.id, p.name, p.is_deleted, p.created_at \
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
    // Aggregate row: (player_id, player_name, games, wins, total_points, highest_score)
    let row: (i64, String, i64, i64, i64, i64) = sqlx::query_as(
        "SELECT
            p.id,
            p.name,
            COUNT(DISTINCT gp.game_id) AS games,
            COUNT(DISTINCT g_win.id) AS wins,
            COALESCE(SUM(se.points), 0) AS total_points,
            COALESCE(MAX(sub.game_total), 0) AS highest_score
         FROM players p
         LEFT JOIN game_players gp ON gp.player_id = p.id
         LEFT JOIN games g ON g.id = gp.game_id AND g.finished_at IS NOT NULL
         LEFT JOIN games g_win ON g_win.id = gp.game_id
             AND g_win.finished_at IS NOT NULL
             AND g_win.winner_player_id = p.id
         LEFT JOIN score_entries se ON se.game_id = g.id AND se.player_id = p.id
         LEFT JOIN (
             SELECT game_id, player_id, SUM(points) AS game_total
             FROM score_entries
             GROUP BY game_id, player_id
         ) sub ON sub.game_id = g.id AND sub.player_id = p.id
         WHERE p.id = ?
         GROUP BY p.id, p.name"
    )
    .bind(player_id)
    .fetch_one(pool)
    .await?;

    let (pid, name, games, wins, total_points, highest_score) = row;
    let losses = games - wins;
    let win_rate = if games > 0 { wins as f64 / games as f64 } else { 0.0 };
    let avg_score = if games > 0 { total_points as f64 / games as f64 } else { 0.0 };
    let win_rate_pct = win_rate * 100.0;
    let rating = 1000.0 + (wins as f64 * 100.0) + (win_rate_pct * 50.0) + (avg_score * 0.5);

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
    let rows: Vec<(i64, String, i64, i64, i64, i64)> = sqlx::query_as(
        "SELECT
            p.id,
            p.name,
            COUNT(DISTINCT gp.game_id) AS games,
            COUNT(DISTINCT g_win.id) AS wins,
            COALESCE(SUM(se.points), 0) AS total_points,
            COALESCE(MAX(sub.game_total), 0) AS highest_score
         FROM players p
         LEFT JOIN game_players gp ON gp.player_id = p.id
         LEFT JOIN games g ON g.id = gp.game_id AND g.finished_at IS NOT NULL
         LEFT JOIN games g_win ON g_win.id = gp.game_id
             AND g_win.finished_at IS NOT NULL
             AND g_win.winner_player_id = p.id
         LEFT JOIN score_entries se ON se.game_id = g.id AND se.player_id = p.id
         LEFT JOIN (
             SELECT game_id, player_id, SUM(points) AS game_total
             FROM score_entries
             GROUP BY game_id, player_id
         ) sub ON sub.game_id = g.id AND sub.player_id = p.id
         WHERE p.is_deleted = 0
         GROUP BY p.id, p.name
         ORDER BY p.name"
    )
    .fetch_all(pool)
    .await?;

    let stats = rows
        .into_iter()
        .map(|(pid, name, games, wins, total_points, highest_score)| {
            let losses = games - wins;
            let win_rate = if games > 0 { wins as f64 / games as f64 } else { 0.0 };
            let avg_score = if games > 0 { total_points as f64 / games as f64 } else { 0.0 };
            let win_rate_pct = win_rate * 100.0;
            let rating = 1000.0 + (wins as f64 * 100.0) + (win_rate_pct * 50.0) + (avg_score * 0.5);
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

    Ok(stats)
}
