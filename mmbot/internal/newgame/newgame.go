package newgame

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
	"github.com/rivan/flip7bot/mmbot/internal/scoreboard"
)

// knownPerPage is the page size of the known-player picker, matching the Rust
// flow (8 players per page).
const knownPerPage = 8

// maxNameRunes / minNameRunes bound a player name, mirroring the Rust check
// (1–50, no control characters); newlines and backticks are additionally
// rejected so names are safe inside the code-fenced scoreboard.
const (
	minNameLen = 1
	maxNameLen = 50
)

// User-facing copy.
const (
	startedStub      = "🎮 Game started — see the scoreboard thread ⬇️"
	needTwoPlayers   = "Add at least 2 players first."
	unfinishedGame   = "You have an unfinished game — load or end it first."
	noOtherPlayers   = "No other players to add."
	invalidNameError = "Invalid name. Use 1–50 characters, no control characters, newlines, or backticks."
	addPlayerTitle   = "Add New Player"
	dialogCallback   = "newgame:add_player"
)

// StartRenderer builds the live-game root-post screen (message + interactive
// attachments) for a freshly started game. It is injected from the game package
// so newgame need not import game (which would invert the dependency direction).
// A nil renderer falls back to a plain, control-less scoreboard so the package
// still works standalone (e.g. before the game screen is wired).
type StartRenderer func(gameID int64, scores map[int64]int64, players []db.Player) (string, []*model.SlackAttachment)

// MainMenuRenderer re-renders the originating post into the main menu (used by
// "← Back"). It is injected from the menu package (menu.MainMenuResponse) so
// newgame need not import menu. A nil renderer falls back to the benign expired
// ephemeral.
type MainMenuRenderer func() *model.PostActionIntegrationResponse

// Screen implements the new-game setup flow: the setup screen, the add-player
// dialog, the known-player picker, and game start. It is stateless — the chosen
// roster travels in each button's signed nav-state (NavState.Players), not in a
// server-side session.
type Screen struct {
	db       *sql.DB
	poster   mm.Poster
	nav      *nav.Builder
	log      *slog.Logger
	start    StartRenderer
	mainMenu MainMenuRenderer
}

// Option configures a Screen.
type Option func(*Screen)

// WithStartRenderer wires the live-game root-post renderer (the game package's
// builder). Without it, a started game's root post shows a plain scoreboard with
// no controls.
func WithStartRenderer(fn StartRenderer) Option {
	return func(s *Screen) { s.start = fn }
}

// WithMainMenuRenderer wires the "← Back" target (menu.MainMenuResponse).
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

// Owns reports the action codes the new-game flow claims. It is registered ahead
// of the menu in the router so it wins the contested entry codes (menu:new_game /
// game:new) that the menu also lists as its default fallback.
func (s *Screen) Owns(action string) bool {
	switch action {
	case mm.ActMenuNewGame, mm.ActGameNew,
		mm.ActSetupAddNew, mm.ActSetupKnown, mm.ActSetupBack, mm.ActSetupStart, mm.ActSetupDisabled,
		mm.ActPlayerRemove,
		mm.ActKnownAdd, mm.ActKnownPage, mm.ActKnownBack, mm.ActKnownStart, mm.ActKnownNoop:
		return true
	default:
		return false
	}
}

// Action handles a verified button click for the new-game flow.
func (s *Screen) Action(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	switch ns.Action {
	case mm.ActMenuNewGame, mm.ActGameNew:
		return s.entry(ctx, req)
	case mm.ActSetupAddNew:
		return s.openAddPlayer(ctx, req, ns)
	case mm.ActSetupKnown:
		return s.showKnown(ctx, ns.Players, 0)
	case mm.ActSetupStart, mm.ActKnownStart:
		return s.startGame(ctx, req, ns.Players)
	case mm.ActSetupDisabled:
		return nav.Ephemeral(needTwoPlayers), nil
	case mm.ActSetupBack:
		if s.mainMenu != nil {
			return s.mainMenu(), nil
		}
		return nav.ExpiredResponse(), nil
	case mm.ActPlayerRemove:
		return s.removePlayer(ctx, ns)
	case mm.ActKnownAdd:
		return s.knownAdd(ctx, ns)
	case mm.ActKnownPage:
		return s.showKnown(ctx, ns.Players, ns.Page)
	case mm.ActKnownBack:
		return s.renderSetup(ctx, ns.Players)
	case mm.ActKnownNoop:
		// The page-indicator button is inert.
		return &model.PostActionIntegrationResponse{}, nil
	default:
		return nav.ExpiredResponse(), nil
	}
}

// entry builds the setup screen, pre-loading the channel's last-used players
// (filtered to those still active).
func (s *Screen) entry(ctx context.Context, req *model.PostActionIntegrationRequest) (*model.PostActionIntegrationResponse, error) {
	all, err := db.GetAllActivePlayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	active := make(map[int64]bool, len(all))
	for _, p := range all {
		active[p.ID] = true
	}
	last, err := db.GetLastPlayers(ctx, s.db, req.ChannelId)
	if err != nil {
		// A missing/failed last-players read must not block a new game.
		s.log.Warn("newgame: get last players failed; starting empty", "err", err.Error())
		last = nil
	}
	players := make([]int64, 0, len(last))
	for _, id := range last {
		if active[id] {
			players = append(players, id)
		}
	}
	return nav.UpdateResponse(setupText(players, all), s.setupAttachments(players, all)), nil
}

// renderSetup re-renders the setup screen for the given roster.
func (s *Screen) renderSetup(ctx context.Context, players []int64) (*model.PostActionIntegrationResponse, error) {
	all, err := db.GetAllActivePlayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	return nav.UpdateResponse(setupText(players, all), s.setupAttachments(players, all)), nil
}

// removePlayer drops ns.PlayerID from the roster and re-renders the setup.
func (s *Screen) removePlayer(ctx context.Context, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	updated := remove(ns.Players, ns.PlayerID)
	return s.renderSetup(ctx, updated)
}

// openAddPlayer opens the add-player dialog as the FIRST action (the trigger_id
// window is ~3s), sealing the current roster + originating post id into the
// signed dialog state so the submit handler can re-render in place.
func (s *Screen) openAddPlayer(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	state, err := s.nav.SignState(mm.NavState{
		Action:  mm.ActSetupAddNew,
		Players: ns.Players,
		PostID:  req.PostId,
	})
	if err != nil {
		s.log.Error("newgame: sign dialog state failed", "err", err.Error())
		return nav.Ephemeral("Couldn't open the dialog — try again."), nil
	}
	dialog := model.Dialog{
		CallbackId:  dialogCallback,
		Title:       addPlayerTitle,
		SubmitLabel: "Add",
		State:       state,
		Elements: []model.DialogElement{{
			DisplayName: "Player name",
			Name:        "name",
			Type:        "text",
			SubType:     "text",
			MinLength:   minNameLen,
			MaxLength:   maxNameLen,
		}},
	}
	if err := s.poster.OpenDialog(ctx, req.TriggerId, s.nav.DialogURL(), dialog); err != nil {
		s.log.Error("newgame: open dialog failed", "err", err.Error())
		return nav.Ephemeral("Couldn't open the dialog — try again."), nil
	}
	// The originating post stays as the setup screen; the dialog submit
	// re-renders it in place via UpdateAttachment.
	return &model.PostActionIntegrationResponse{}, nil
}

// Dialog handles the add-player dialog submission.
func (s *Screen) Dialog(ctx context.Context, req *model.SubmitDialogRequest, ns mm.NavState) (*model.SubmitDialogResponse, error) {
	if ns.Action != mm.ActSetupAddNew {
		// Not ours (defensive — the router already dispatched by Owns).
		return &model.SubmitDialogResponse{}, nil
	}

	raw, _ := req.Submission["name"].(string)
	name := strings.TrimSpace(raw)
	if err := validateName(name); err != nil {
		return &model.SubmitDialogResponse{Errors: map[string]string{"name": err.Error()}}, nil
	}

	player, err := db.CreateOrGetPlayer(ctx, s.db, name)
	if err != nil {
		return nil, err
	}

	if contains(ns.Players, player.ID) {
		return &model.SubmitDialogResponse{
			Errors: map[string]string{"name": fmt.Sprintf("%s is already added.", name)},
		}, nil
	}

	updated := append(append([]int64(nil), ns.Players...), player.ID)
	all, err := db.GetAllActivePlayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	// A dialog submit cannot itself update a post, so re-render the originating
	// setup post in place. Updating a since-deleted post is a benign no-op.
	if err := s.poster.UpdateAttachment(ctx, ns.PostID, setupText(updated, all), s.setupAttachments(updated, all)); err != nil {
		return nil, err
	}
	return &model.SubmitDialogResponse{}, nil
}

// knownAdd appends the picked player to the roster, returning to the setup
// screen once no other known players remain, otherwise staying on the picker.
func (s *Screen) knownAdd(ctx context.Context, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	updated := ns.Players
	if !contains(updated, ns.PlayerID) {
		updated = append(append([]int64(nil), ns.Players...), ns.PlayerID)
	}
	all, err := db.GetAllActivePlayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if len(available(all, updated)) == 0 {
		// No more known players to add — return to the setup screen.
		return nav.UpdateResponse(setupText(updated, all), s.setupAttachments(updated, all)), nil
	}
	return s.renderKnown(updated, all, ns.Page), nil
}

// showKnown loads players and renders the known-player picker at the given page.
// An empty available set (everyone is already added) yields a benign ephemeral.
func (s *Screen) showKnown(ctx context.Context, players []int64, page int) (*model.PostActionIntegrationResponse, error) {
	all, err := db.GetAllActivePlayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if len(available(all, players)) == 0 {
		return nav.Ephemeral(noOtherPlayers), nil
	}
	return s.renderKnown(players, all, page), nil
}

// startGame is the shared start path for setup:start and known:start.
func (s *Screen) startGame(ctx context.Context, req *model.PostActionIntegrationRequest, players []int64) (*model.PostActionIntegrationResponse, error) {
	if len(players) < 2 {
		return nav.Ephemeral(needTwoPlayers), nil
	}
	channelID := req.ChannelId
	unfinished, err := db.GetUnfinishedGames(ctx, s.db, channelID)
	if err != nil {
		return nil, err
	}
	if len(unfinished) > 0 {
		return nav.Ephemeral(unfinishedGame), nil
	}

	game, err := db.CreateGame(ctx, s.db, channelID)
	if err != nil {
		return nil, err
	}
	for _, pid := range players {
		if err := db.AddPlayerToGame(ctx, s.db, game.ID, pid); err != nil {
			return nil, err
		}
	}

	gamePlayers, err := db.GetGamePlayers(ctx, s.db, game.ID)
	if err != nil {
		return nil, err
	}
	scores := map[int64]int64{}
	message, attachments := s.renderRoot(game.ID, scores, gamePlayers)

	postID, err := s.poster.PostAttachment(ctx, channelID, message, attachments)
	if err != nil {
		return nil, err
	}
	if err := db.UpdateGamePostID(ctx, s.db, game.ID, postID); err != nil {
		return nil, err
	}
	if err := db.SaveLastPlayers(ctx, s.db, channelID, players); err != nil {
		return nil, err
	}

	// Retire the originating setup/menu post so only the fresh root post carries
	// the live score/end controls.
	return nav.UpdateResponse(startedStub, nil), nil
}

// renderRoot builds the live-game root post via the injected StartRenderer, or a
// plain control-less scoreboard when none is wired.
func (s *Screen) renderRoot(gameID int64, scores map[int64]int64, players []db.Player) (string, []*model.SlackAttachment) {
	if s.start != nil {
		return s.start(gameID, scores, players)
	}
	return scoreboard.RenderScoreboard(scores, players, gameID), nil
}

// renderKnown builds the picker response for the given roster, player set, and
// (clamped) page.
func (s *Screen) renderKnown(players []int64, all []db.Player, page int) *model.PostActionIntegrationResponse {
	avail := available(all, players)
	totalPages := (len(avail) + knownPerPage - 1) / knownPerPage
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 0 {
		page = 0
	}
	if page > totalPages-1 {
		page = totalPages - 1
	}
	start := page * knownPerPage
	end := start + knownPerPage
	if end > len(avail) {
		end = len(avail)
	}
	pagePlayers := avail[start:end]

	addedCount := len(players)
	var header string
	if addedCount == 0 {
		header = fmt.Sprintf("Select a player (page %d/%d):", page+1, totalPages)
	} else {
		header = fmt.Sprintf("Select a player — %d added so far (page %d/%d):", addedCount, page+1, totalPages)
	}

	actions := make([]*model.PostAction, 0, len(pagePlayers)+5)
	for _, p := range pagePlayers {
		actions = append(actions, s.nav.Button(p.Name, mm.NavState{
			Action: mm.ActKnownAdd, PlayerID: p.ID, Players: players, Page: page,
		}))
	}
	if page > 0 {
		actions = append(actions, s.nav.Button("← Prev", mm.NavState{
			Action: mm.ActKnownPage, Players: players, Page: page - 1,
		}))
	}
	actions = append(actions, s.nav.Button(fmt.Sprintf("Page %d/%d", page+1, totalPages), mm.NavState{
		Action: mm.ActKnownNoop, Players: players,
	}))
	if page+1 < totalPages {
		actions = append(actions, s.nav.Button("Next →", mm.NavState{
			Action: mm.ActKnownPage, Players: players, Page: page + 1,
		}))
	}
	actions = append(actions, s.nav.Button("← Back to Setup", mm.NavState{
		Action: mm.ActKnownBack, Players: players,
	}))
	if addedCount >= 2 {
		actions = append(actions, s.nav.Button("▶ Start Game", mm.NavState{
			Action: mm.ActKnownStart, Players: players,
		}))
	}

	return nav.UpdateResponse(header, []*model.SlackAttachment{{Actions: nav.CompactActions(actions)}})
}

// setupAttachments builds the setup-screen interactive attachment.
func (s *Screen) setupAttachments(players []int64, all []db.Player) []*model.SlackAttachment {
	actions := make([]*model.PostAction, 0, len(players)+4)
	for _, pid := range players {
		actions = append(actions, s.nav.Button(nameOf(pid, all)+" ✕", mm.NavState{
			Action: mm.ActPlayerRemove, PlayerID: pid, Players: players,
		}))
	}
	actions = append(actions,
		s.nav.Button("+ New Player", mm.NavState{Action: mm.ActSetupAddNew, Players: players}),
		s.nav.Button("+ Known Player", mm.NavState{Action: mm.ActSetupKnown, Players: players}),
		s.nav.Button("← Back", mm.NavState{Action: mm.ActSetupBack, Players: players}),
	)
	if len(players) >= 2 {
		actions = append(actions, s.nav.Button("▶ Start Game", mm.NavState{
			Action: mm.ActSetupStart, Players: players,
		}))
	} else {
		actions = append(actions, s.nav.Button("▶ Start Game (need 2+ players)", mm.NavState{
			Action: mm.ActSetupDisabled, Players: players,
		}))
	}
	return []*model.SlackAttachment{{Actions: nav.CompactActions(actions)}}
}

// setupText is the setup-screen body listing the added players in roster order.
func setupText(players []int64, all []db.Player) string {
	names := make([]string, 0, len(players))
	for _, id := range players {
		if n := nameOf(id, all); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "New game setup:\nPlayers added: (none)"
	}
	return "New game setup:\nPlayers added: " + strings.Join(names, ", ")
}

// available returns the active players not already in the roster, preserving the
// name-ordered input order.
func available(all []db.Player, players []int64) []db.Player {
	out := make([]db.Player, 0, len(all))
	for _, p := range all {
		if !contains(players, p.ID) {
			out = append(out, p)
		}
	}
	return out
}

// nameOf returns the player's name for the given id, or "" if not found.
func nameOf(id int64, all []db.Player) string {
	for _, p := range all {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

// contains reports whether ids includes id.
func contains(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// remove returns ids with the first occurrence of id dropped (order preserved).
func remove(ids []int64, id int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, v := range ids {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

// validateName enforces the player-name rules: 1–50 characters, no control
// characters, newlines, or backticks (so names are safe inside code-fenced
// renders and cannot inject markdown/mentions). The name is assumed trimmed.
func validateName(name string) error {
	r := []rune(name)
	if len(r) < minNameLen || len(r) > maxNameLen {
		return errInvalidName
	}
	for _, c := range r {
		if unicode.IsControl(c) || c == '\n' || c == '`' {
			return errInvalidName
		}
	}
	return nil
}

// errInvalidName is the validation failure surfaced to the dialog.
var errInvalidName = invalidNameErr{}

type invalidNameErr struct{}

func (invalidNameErr) Error() string { return invalidNameError }
