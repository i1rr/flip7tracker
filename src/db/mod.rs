pub mod models;
pub mod queries;

use sqlx::sqlite::{SqliteConnectOptions, SqliteJournalMode, SqlitePoolOptions};
use sqlx::SqlitePool;
use std::str::FromStr;

pub async fn create_pool(database_url: &str) -> Result<SqlitePool, sqlx::Error> {
    let connect_options = SqliteConnectOptions::from_str(database_url)?
        .journal_mode(SqliteJournalMode::Wal)
        .foreign_keys(true)
        .create_if_missing(true);

    SqlitePoolOptions::new()
        .min_connections(1)
        .max_connections(5)
        .connect_with(connect_options)
        .await
}
