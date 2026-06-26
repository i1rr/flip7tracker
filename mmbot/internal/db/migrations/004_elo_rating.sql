-- Elo rating: every player has a current rating (default 1000), and every
-- finished game writes a row per participating player to rating_history so
-- the trail can be audited or shown to users.
ALTER TABLE players ADD COLUMN rating REAL NOT NULL DEFAULT 1000.0;

CREATE TABLE IF NOT EXISTS rating_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    game_id INTEGER NOT NULL REFERENCES games(id),
    player_id INTEGER NOT NULL REFERENCES players(id),
    rating_before REAL NOT NULL,
    rating_after REAL NOT NULL,
    delta REAL NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_rating_history_game_id ON rating_history(game_id);
CREATE INDEX IF NOT EXISTS idx_rating_history_player_id ON rating_history(player_id);
