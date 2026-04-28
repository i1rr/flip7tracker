CREATE TABLE IF NOT EXISTS game_winners (
    game_id INTEGER NOT NULL REFERENCES games(id),
    player_id INTEGER NOT NULL REFERENCES players(id),
    PRIMARY KEY (game_id, player_id)
);

CREATE INDEX IF NOT EXISTS idx_game_winners_game_id ON game_winners(game_id);
CREATE INDEX IF NOT EXISTS idx_game_winners_player_id ON game_winners(player_id);

INSERT OR IGNORE INTO game_winners (game_id, player_id)
SELECT id, winner_player_id FROM games
WHERE winner_player_id IS NOT NULL AND finished_at IS NOT NULL;
