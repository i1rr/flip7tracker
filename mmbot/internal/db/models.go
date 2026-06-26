package db

import "database/sql"

// Game mirrors the `games` table after the 005 migration: the legacy Telegram
// integer ids (chat_id/message_id) are now Mattermost strings
// (channel_id/post_id). PostID is nullable because a fresh CreatePost is forced
// on the next render/resume.
type Game struct {
	ID             int64
	ChannelID      string
	PostID         sql.NullString
	StartedAt      string
	FinishedAt     sql.NullString
	WinnerPlayerID sql.NullInt64
}

// Player mirrors the `players` table, including the Elo `rating` column added in
// migration 004.
type Player struct {
	ID        int64
	Name      string
	IsDeleted bool
	CreatedAt string
	Rating    float64
}

// ScoreEntry mirrors a row in `score_entries`.
type ScoreEntry struct {
	ID        int64
	GameID    int64
	PlayerID  int64
	Points    int64
	CreatedAt string
}

// PlayerStats is built manually in the query layer (it has no backing table),
// mirroring the Rust `PlayerStats` struct field-for-field.
type PlayerStats struct {
	PlayerID     int64
	PlayerName   string
	Games        int64
	Wins         int64
	Losses       int64
	WinRate      float64
	AvgScore     float64
	HighestScore int64
	TotalPoints  int64
	Rating       float64
}
