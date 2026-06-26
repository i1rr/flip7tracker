package rating

// EloDelta is the per-player result of an Elo update for a single finished game.
// It is the owned, canonical type for a rating change: the db layer consumes it
// when persisting a finished game (one rating_history row + the players.rating
// update per delta) and when reading a finished game back to re-render its win
// screen from persisted values.
//
// The Elo computation itself (EloEntry, the constants, expected(), and
// ComputeUpdates) is ported alongside this type in rating.go; EloDelta lives in
// its own file so it can be defined exactly once and consumed by internal/db
// without duplicating the type.
type EloDelta struct {
	PlayerID     int64
	RatingBefore float64
	RatingAfter  float64
	Delta        float64
}
