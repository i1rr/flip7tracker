package game

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
	"github.com/rivan/flip7bot/mmbot/internal/rating"
	"github.com/rivan/flip7bot/mmbot/internal/scoreboard"
)

// winThreshold is the score a player must reach for a game to be winnable; below
// it End Game discards the game (matching the Rust "first to 200" rule).
const winThreshold = 200

// Score bounds for a single typed entry (parity with the Rust 0–999 check).
const (
	minPoints = 0
	maxPoints = 999
)

// User-facing copy.
const (
	gameOverMsg     = "This game is already over."
	playerGoneMsg   = "That player is no longer in this game."
	invalidPtsMsg   = "Score must be a whole number between 0 and 999."
	noScoresMsg     = "No scores to edit yet."
	gameMissingMsg  = "That game is no longer available."
	discardStub     = "🚩 Game discarded — nobody reached 200."
	discardReply    = "🚩 Game discarded — no one reached 200, so it wasn't saved to statistics."
	resumedStub     = "🎮 Game resumed — see the scoreboard thread ⬇️"
	noUnfinishedMsg = "No unfinished games found."
	loadPrompt      = "Select a game to resume:"
	scoreDialogID   = "game:score"
)

// MainMenuRenderer re-renders the originating post into the main menu (used by
// the load list's "← Back"). Injected from the menu package
// (menu.MainMenuResponse) so game need not import menu. A nil renderer falls
// back to the benign expired ephemeral.
type MainMenuRenderer func() *model.PostActionIntegrationResponse

// Screen drives the active-game lifecycle: the live scoreboard root post, typed
// score entry, edit-last, end-game (discard or finish + Elo), and load/resume.
// It is stateless — the game id travels in each button's signed nav-state and
// the game is re-read from SQLite on every interaction.
type Screen struct {
	db       *sql.DB
	poster   mm.Poster
	nav      *nav.Builder
	log      *slog.Logger
	mainMenu MainMenuRenderer
}

// Option configures a Screen.
type Option func(*Screen)

// WithMainMenuRenderer wires the load list's "← Back" target
// (menu.MainMenuResponse).
func WithMainMenuRenderer(fn MainMenuRenderer) Option {
	return func(s *Screen) { s.mainMenu = fn }
}

// New constructs a Screen. A nil logger falls back to slog.Default.
func New(database *sql.DB, poster mm.Poster, builder *nav.Builder, log *slog.Logger, opts ...Option) *Screen {
	if log == nil {
		log = slog.Default()
	}
	s := &Screen{db: database, poster: poster, nav: builder, log: log}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Owns reports the action codes the active-game and load flows claim. It claims
// menu:load_game (the menu lists it as a fallback default) so it must be
// registered ahead of the menu in the router, exactly like newgame claims
// menu:new_game.
func (s *Screen) Owns(action string) bool {
	switch action {
	case mm.ActMenuLoadGame, mm.ActGameLoad, mm.ActLoadBack,
		mm.ActScorePlayer,
		mm.ActGameEdit, mm.ActEditConfirm, mm.ActEditCancel,
		mm.ActGameEnd, mm.ActGameEndConfirm, mm.ActGameEndCancel:
		return true
	default:
		return false
	}
}

// Action handles a verified button click for the active-game and load flows.
func (s *Screen) Action(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	switch ns.Action {
	case mm.ActMenuLoadGame:
		return s.showLoadList(ctx, req.ChannelId)
	case mm.ActGameLoad:
		return s.loadGame(ctx, req.ChannelId, ns.GameID)
	case mm.ActLoadBack:
		if s.mainMenu != nil {
			return s.mainMenu(), nil
		}
		return nav.ExpiredResponse(), nil

	case mm.ActScorePlayer:
		return s.openScoreDialog(ctx, req, ns)

	case mm.ActGameEdit:
		return s.editLast(ctx, ns.GameID)
	case mm.ActEditConfirm:
		if _, err := db.DeleteScoreEntryByID(ctx, s.db, ns.GameID, ns.EntryID); err != nil {
			return nil, err
		}
		return s.renderActive(ctx, ns.GameID)
	case mm.ActEditCancel:
		return s.renderActive(ctx, ns.GameID)

	case mm.ActGameEnd:
		return s.endPrompt(ctx, ns.GameID)
	case mm.ActGameEndConfirm:
		return s.endConfirm(ctx, req, ns.GameID)
	case mm.ActGameEndCancel:
		return s.renderActive(ctx, ns.GameID)

	default:
		return nav.ExpiredResponse(), nil
	}
}

// RenderRoot builds the live-game root-post screen (scoreboard text + the
// interactive attachment carrying a per-player score button plus Edit Last /
// End Game). It matches newgame.StartRenderer so main.go can wire it via
// newgame.WithStartRenderer; it is also used to render a resumed game.
func (s *Screen) RenderRoot(gameID int64, scores map[int64]int64, players []db.Player) (string, []*model.SlackAttachment) {
	message := scoreboard.RenderScoreboard(scores, players, gameID)
	actions := make([]*model.PostAction, 0, len(players)+2)
	for _, p := range players {
		actions = append(actions, s.nav.Button(p.Name, mm.NavState{
			Action: mm.ActScorePlayer, GameID: gameID, PlayerID: p.ID,
		}))
	}
	actions = append(actions,
		s.nav.Button("✏️ Edit Last", mm.NavState{Action: mm.ActGameEdit, GameID: gameID}),
		s.nav.Button("🚩 End Game", mm.NavState{Action: mm.ActGameEnd, GameID: gameID}),
	)
	return message, []*model.SlackAttachment{{Actions: nav.CompactActions(actions)}}
}

// renderActive re-reads the game and re-renders the live scoreboard in place
// (the response updates the originating root post).
func (s *Screen) renderActive(ctx context.Context, gameID int64) (*model.PostActionIntegrationResponse, error) {
	players, scores, err := s.loadState(ctx, gameID)
	if err != nil {
		return nil, err
	}
	message, att := s.RenderRoot(gameID, scores, players)
	return nav.UpdateResponse(message, att), nil
}

// loadState reads a game's players and scores.
func (s *Screen) loadState(ctx context.Context, gameID int64) ([]db.Player, map[int64]int64, error) {
	players, err := db.GetGamePlayers(ctx, s.db, gameID)
	if err != nil {
		return nil, nil, err
	}
	scores, err := db.GetGameScores(ctx, s.db, gameID)
	if err != nil {
		return nil, nil, err
	}
	return players, scores, nil
}

// openScoreDialog opens the points dialog as the FIRST action (the trigger_id
// window is ~3s), sealing the game + player + originating post into the signed
// dialog state so the submit handler can re-render the root post in place.
func (s *Screen) openScoreDialog(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	state, err := s.nav.SignState(mm.NavState{
		Action:   mm.ActScorePlayer,
		GameID:   ns.GameID,
		PlayerID: ns.PlayerID,
		PostID:   req.PostId,
	})
	if err != nil {
		s.log.Error("game: sign dialog state failed", "err", err.Error())
		return nav.Ephemeral("Couldn't open the dialog — try again."), nil
	}
	dialog := model.Dialog{
		CallbackId:  scoreDialogID,
		Title:       "Enter score",
		SubmitLabel: "Save",
		State:       state,
		Elements: []model.DialogElement{{
			DisplayName: "Points (0–999)",
			Name:        "points",
			Type:        "text",
			SubType:     "number",
		}},
	}
	if err := s.poster.OpenDialog(ctx, req.TriggerId, s.nav.DialogURL(), dialog); err != nil {
		s.log.Error("game: open dialog failed", "err", err.Error())
		return nav.Ephemeral("Couldn't open the dialog — try again."), nil
	}
	// Keep the full game keyboard visible (a misclick is corrected by tapping the
	// right player); the dialog submit re-renders the root post in place.
	return &model.PostActionIntegrationResponse{}, nil
}

// Dialog handles the points dialog submission for score entry.
func (s *Screen) Dialog(ctx context.Context, req *model.SubmitDialogRequest, ns mm.NavState) (*model.SubmitDialogResponse, error) {
	if ns.Action != mm.ActScorePlayer {
		// Not ours (defensive — the router already dispatched by Owns).
		return &model.SubmitDialogResponse{}, nil
	}

	// Guard: the game must still be unfinished. A finished/discarded game is no
	// longer in the channel's unfinished set.
	unfinished, err := db.GetUnfinishedGames(ctx, s.db, req.ChannelId)
	if err != nil {
		return nil, err
	}
	if !gameInList(unfinished, ns.GameID) {
		return &model.SubmitDialogResponse{Error: gameOverMsg}, nil
	}

	// Guard: the player must still be a member of this game (a stale button for a
	// since-deleted player must not cause an FK-violation 500).
	players, err := db.GetGamePlayers(ctx, s.db, ns.GameID)
	if err != nil {
		return nil, err
	}
	name, ok := playerName(players, ns.PlayerID)
	if !ok {
		return &model.SubmitDialogResponse{Error: playerGoneMsg}, nil
	}

	raw := pointsSubmissionString(req.Submission["points"])
	pts, ok := parsePoints(raw)
	if !ok {
		return &model.SubmitDialogResponse{Errors: map[string]string{"points": invalidPtsMsg}}, nil
	}

	if _, err := db.AddScoreEntry(ctx, s.db, ns.GameID, ns.PlayerID, pts); err != nil {
		return nil, err
	}

	// Re-render the root post in place (a SubmitDialogResponse cannot itself
	// update a post). Updating a since-deleted post is a benign no-op.
	_, scores, err := s.loadState(ctx, ns.GameID)
	if err != nil {
		return nil, err
	}
	message, att := s.RenderRoot(ns.GameID, scores, players)
	if err := s.poster.UpdateAttachment(ctx, ns.PostID, message, att); err != nil {
		return nil, err
	}
	// Post the per-entry confirmation as a thread reply (best-effort, parity with
	// the Rust "✏️ {name}: {pts}" line).
	s.reply(ctx, req.ChannelId, ns.PostID, fmt.Sprintf("✏️ %s: %d", name, pts))
	return &model.SubmitDialogResponse{}, nil
}

// editLast renders the edit-last confirmation for the game's most recent entry,
// carrying that specific entry id in the confirm button's signed context (so a
// replay can only ever delete the one entry it was minted for).
func (s *Screen) editLast(ctx context.Context, gameID int64) (*model.PostActionIntegrationResponse, error) {
	entry, err := db.GetLastScoreEntry(ctx, s.db, gameID)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nav.Ephemeral(noScoresMsg), nil
	}
	players, err := db.GetGamePlayers(ctx, s.db, gameID)
	if err != nil {
		return nil, err
	}
	name, _ := playerName(players, entry.PlayerID)
	if name == "" {
		name = "Unknown"
	}
	text := fmt.Sprintf("Last entry: %s — %d pts\nDelete this entry?", name, entry.Points)
	actions := nav.CompactActions([]*model.PostAction{
		s.nav.Button("🗑 Delete Entry", mm.NavState{Action: mm.ActEditConfirm, GameID: gameID, EntryID: entry.ID}),
		s.nav.Button("← Cancel", mm.NavState{Action: mm.ActEditCancel, GameID: gameID}),
	})
	return nav.UpdateResponse(text, []*model.SlackAttachment{{Actions: actions}}), nil
}

// endPrompt renders the End Game confirmation, with copy that depends on whether
// the top score has reached the win threshold.
func (s *Screen) endPrompt(ctx context.Context, gameID int64) (*model.PostActionIntegrationResponse, error) {
	players, scores, err := s.loadState(ctx, gameID)
	if err != nil {
		return nil, err
	}
	max := maxScore(players, scores)

	var prompt string
	if max >= winThreshold {
		names := winnerNames(players, scores, max)
		if len(names) == 1 {
			prompt = fmt.Sprintf("End the game now? %s wins with %d pts.", names[0], max)
		} else {
			prompt = fmt.Sprintf("End the game now? Joint winners at %d pts: %s.", max, strings.Join(names, ", "))
		}
	} else {
		prompt = "End the game now? No one has reached 200 yet — the game will be discarded and won't be saved to statistics."
	}

	actions := nav.CompactActions([]*model.PostAction{
		s.nav.Button("Yes, End", mm.NavState{Action: mm.ActGameEndConfirm, GameID: gameID}),
		s.nav.Button("Cancel", mm.NavState{Action: mm.ActGameEndCancel, GameID: gameID}),
	})
	return nav.UpdateResponse(prompt, []*model.SlackAttachment{{Actions: actions}}), nil
}

// endConfirm finalizes the game: discard when the top score is below the
// threshold, otherwise compute Elo and finish with winner(s). Both paths are
// idempotent against a replayed/double-tapped confirm button.
func (s *Screen) endConfirm(ctx context.Context, req *model.PostActionIntegrationRequest, gameID int64) (*model.PostActionIntegrationResponse, error) {
	players, scores, err := s.loadState(ctx, gameID)
	if err != nil {
		return nil, err
	}
	max := maxScore(players, scores)

	if max < winThreshold {
		discarded, err := db.DiscardGame(ctx, s.db, gameID)
		if err != nil {
			return nil, err
		}
		if discarded {
			s.reply(ctx, req.ChannelId, req.PostId, discardReply)
		}
		// On a replay (already discarded) just render the same benign ended stub
		// with no controls and post nothing.
		return nav.UpdateResponse(discardStub, nil), nil
	}

	winners := winnerIDs(players, scores, max)
	entries := make([]rating.EloEntry, len(players))
	for i, p := range players {
		entries[i] = rating.EloEntry{PlayerID: p.ID, Score: scores[p.ID], Rating: p.Rating}
	}
	deltas := rating.ComputeUpdates(entries)

	finalized, err := db.FinishGameWithWinners(ctx, s.db, gameID, winners, deltas)
	if err != nil {
		return nil, err
	}

	if finalized {
		if err := db.SaveLastPlayers(ctx, s.db, req.ChannelId, playerIDs(players)); err != nil {
			return nil, err
		}
		names := namesFor(players, winners)
		s.reply(ctx, req.ChannelId, req.PostId, winHeader(names, max))
		return s.winScreen(names, max, scores, players, deltas), nil
	}

	// Already finalized by a prior tap: do NOT recompute Elo (ratings were already
	// updated) and do NOT re-post the announcement. Rebuild the win screen from
	// the persisted winners + deltas.
	result, err := db.GetFinishedGameResult(ctx, s.db, gameID)
	if err != nil {
		return nil, err
	}
	names := namesFor(players, result.WinnerIDs)
	return s.winScreen(names, max, scores, players, result.Deltas), nil
}

// winScreen renders the end-of-game win screen into the originating root post,
// carrying the post-game navigation buttons.
func (s *Screen) winScreen(names []string, winnerScore int64, scores map[int64]int64, players []db.Player, deltas []rating.EloDelta) *model.PostActionIntegrationResponse {
	text := scoreboard.BuildWinText(names, winnerScore, scores, players, deltas)
	actions := nav.CompactActions([]*model.PostAction{
		s.nav.Button("📊 View Stats", mm.NavState{Action: mm.ActGameStats}),
		s.nav.Button("🎮 New Game", mm.NavState{Action: mm.ActGameNew}),
		s.nav.Button("🏠 Main Menu", mm.NavState{Action: mm.ActGameHome}),
	})
	return nav.UpdateResponse(text, []*model.SlackAttachment{{Actions: actions}})
}

// RepostScoreboard reposts the most-recent active game's scoreboard at the bottom
// of the channel (for `/flip7 scoreboard`). It matches menu.ScoreboardReposter so
// main.go can wire it via menu.WithScoreboardReposter. The old root post is
// deleted (best-effort) and replaced so only one live board drives the game.
func (s *Screen) RepostScoreboard(ctx context.Context, channelID string) (*model.CommandResponse, error) {
	games, err := db.GetUnfinishedGames(ctx, s.db, channelID)
	if err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return &model.CommandResponse{ResponseType: model.CommandResponseTypeEphemeral, Text: "No active game."}, nil
	}
	g := games[0]
	if g.PostID.Valid && g.PostID.String != "" {
		_ = s.poster.DeletePost(ctx, g.PostID.String)
	}
	players, scores, err := s.loadState(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	message, att := s.RenderRoot(g.ID, scores, players)
	postID, err := s.poster.PostAttachment(ctx, channelID, message, att)
	if err != nil {
		return nil, err
	}
	if err := db.UpdateGamePostID(ctx, s.db, g.ID, postID); err != nil {
		return nil, err
	}
	// The board is posted directly; the command itself stays silent.
	return &model.CommandResponse{}, nil
}

// reply posts a best-effort thread reply, logging (never failing the request) on
// error — parity with the Rust `let _ = bot...` confirmation lines.
func (s *Screen) reply(ctx context.Context, channelID, rootID, message string) {
	if _, err := s.poster.PostInThread(ctx, channelID, rootID, message); err != nil {
		s.log.Warn("game: post thread reply failed", "err", err.Error())
	}
}

// --- pure helpers -----------------------------------------------------------

// parsePoints parses a typed score, enforcing a whole number in [0, 999].
// pointsSubmissionString coerces the dialog "points" submission value to a
// string for parsePoints. A text element with SubType "number" is delivered by
// Mattermost as a JSON number (float64), NOT a string, so a plain string
// type-assertion silently drops every entry. A whole float renders without a
// decimal point (20.0 -> "20") so parsePoints accepts it; a fractional value
// keeps its '.' and is correctly rejected as not a whole number.
func pointsSubmissionString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func parsePoints(raw string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	if v < minPoints || v > maxPoints {
		return 0, false
	}
	return v, true
}

// maxScore returns the highest per-player total (0 when there are no players).
func maxScore(players []db.Player, scores map[int64]int64) int64 {
	var max int64
	for _, p := range players {
		if scores[p.ID] > max {
			max = scores[p.ID]
		}
	}
	return max
}

// winnerNames returns the names of every player tied at the target score, in
// game-player order.
func winnerNames(players []db.Player, scores map[int64]int64, target int64) []string {
	var out []string
	for _, p := range players {
		if scores[p.ID] == target {
			out = append(out, p.Name)
		}
	}
	return out
}

// winnerIDs returns the ids of every player tied at the target score.
func winnerIDs(players []db.Player, scores map[int64]int64, target int64) []int64 {
	var out []int64
	for _, p := range players {
		if scores[p.ID] == target {
			out = append(out, p.ID)
		}
	}
	return out
}

// namesFor maps a set of player ids to their names, in game-player order.
func namesFor(players []db.Player, ids []int64) []string {
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []string
	for _, p := range players {
		if want[p.ID] {
			out = append(out, p.Name)
		}
	}
	return out
}

// playerIDs returns the ids of the given players, in order.
func playerIDs(players []db.Player) []int64 {
	out := make([]int64, len(players))
	for i, p := range players {
		out[i] = p.ID
	}
	return out
}

// playerName returns the player's name for id and whether it was found.
func playerName(players []db.Player, id int64) (string, bool) {
	for _, p := range players {
		if p.ID == id {
			return p.Name, true
		}
	}
	return "", false
}

// gameInList reports whether gameID appears in games.
func gameInList(games []db.Game, gameID int64) bool {
	for _, g := range games {
		if g.ID == gameID {
			return true
		}
	}
	return false
}

// winHeader is the concise winner-announcement line posted as a thread reply.
func winHeader(names []string, score int64) string {
	if len(names) == 1 {
		return fmt.Sprintf("🏆 WINNER: %s with %d pts!", names[0], score)
	}
	return fmt.Sprintf("🏆 JOINT WINNERS: %s — tied at %d pts!", strings.Join(names, ", "), score)
}
