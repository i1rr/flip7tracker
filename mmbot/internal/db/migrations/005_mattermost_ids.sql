-- Migrate the legacy Telegram integer ids to Mattermost string ids by rebuilding
-- the affected tables (SQLite cannot change a column's type in place).
--
-- FK enforcement is handled by the migration RUNNER (it sets
-- PRAGMA foreign_keys=OFF outside any transaction for the whole batch and runs
-- PRAGMA foreign_key_check afterwards). This file MUST NOT rely on an
-- in-transaction PRAGMA foreign_keys, which is a documented no-op.
--
-- games.chat_id    INTEGER -> games.channel_id TEXT   (legacy rows set to '')
-- games.message_id INTEGER -> games.post_id     TEXT   (legacy rows set to NULL)
-- chat_last_players.chat_id INTEGER -> channel_id TEXT (legacy rows set to '')
--
-- channel_id is left '' here; the real owner channel id is backfilled at startup
-- (UPDATE ... WHERE channel_id='') once mm.Resolve yields it.

-- Rebuild `games`. Re-declare INTEGER PRIMARY KEY AUTOINCREMENT so new ids
-- continue above the existing high-water mark (no id reuse / FK aliasing).
CREATE TABLE games_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id TEXT NOT NULL,
    post_id TEXT,
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at TEXT,
    winner_player_id INTEGER REFERENCES players(id)
);

INSERT INTO games_new (id, channel_id, post_id, started_at, finished_at, winner_player_id)
SELECT id, '', NULL, started_at, finished_at, winner_player_id
FROM games;

DROP TABLE games;
ALTER TABLE games_new RENAME TO games;

CREATE INDEX IF NOT EXISTS idx_games_channel_id ON games(channel_id);

-- Rebuild `chat_last_players`. Its PK becomes (channel_id, player_id); collapsing
-- every legacy chat_id to '' can collide a player who appeared under two Telegram
-- chats, so dedupe with INSERT OR IGNORE + SELECT DISTINCT instead of aborting on
-- the UNIQUE-PK violation.
CREATE TABLE chat_last_players_new (
    channel_id TEXT NOT NULL,
    player_id INTEGER NOT NULL REFERENCES players(id),
    PRIMARY KEY (channel_id, player_id)
);

INSERT OR IGNORE INTO chat_last_players_new (channel_id, player_id)
SELECT DISTINCT '', player_id
FROM chat_last_players;

DROP TABLE chat_last_players;
ALTER TABLE chat_last_players_new RENAME TO chat_last_players;
