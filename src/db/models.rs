use chrono::{DateTime, Utc};
use sqlx::FromRow;

#[allow(dead_code)]
#[derive(Debug, Clone, FromRow)]
pub struct Player {
    pub id: i64,
    pub name: String,
    pub is_deleted: bool,
    pub created_at: DateTime<Utc>,
}

#[allow(dead_code)]
#[derive(Debug, Clone, FromRow)]
pub struct Game {
    pub id: i64,
    pub chat_id: i64,
    pub message_id: Option<i64>,
    pub started_at: DateTime<Utc>,
    pub finished_at: Option<DateTime<Utc>>,
    pub winner_player_id: Option<i64>,
}

#[allow(dead_code)]
#[derive(Debug, Clone, FromRow)]
pub struct ScoreEntry {
    pub id: i64,
    pub game_id: i64,
    pub player_id: i64,
    pub points: i64,
    pub created_at: DateTime<Utc>,
}

// PlayerStats does NOT derive FromRow -- constructed manually in queries.rs
#[derive(Debug, Clone)]
pub struct PlayerStats {
    pub player_id: i64,
    pub player_name: String,
    pub games: i64,
    pub wins: i64,
    pub losses: i64,
    pub win_rate: f64,
    pub avg_score: f64,
    pub highest_score: i64,
    pub total_points: i64,
    pub rating: f64,
}
