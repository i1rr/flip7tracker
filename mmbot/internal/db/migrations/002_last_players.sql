CREATE TABLE IF NOT EXISTS chat_last_players (
    chat_id INTEGER NOT NULL,
    player_id INTEGER NOT NULL REFERENCES players(id),
    PRIMARY KEY (chat_id, player_id)
);
