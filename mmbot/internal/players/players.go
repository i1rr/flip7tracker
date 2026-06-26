package players

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
)

// perPage is the page size of the player-management list, matching the Rust flow
// (8 players per page).
const perPage = 8

// Name bounds, mirroring the Rust check (1–50, no control characters); newlines
// and backticks are additionally rejected so a renamed player is safe inside the
// code-fenced scoreboard.
const (
	minNameLen = 1
	maxNameLen = 50
)

// User-facing copy.
const (
	emptyList        = "No players yet."
	invalidNameError = "Invalid name. Use 1–50 characters, no control characters, newlines, or backticks."
	renameTitle      = "Rename player"
	dialogCallback   = "players:rename"
)

// MainMenuRenderer re-renders the originating post into the main menu (used by
// "← Back"). It is injected from the menu package (menu.MainMenuResponse) so the
// players package need not import menu. A nil renderer falls back to the benign
// expired ephemeral.
type MainMenuRenderer func() *model.PostActionIntegrationResponse

// Screen implements player management: the paginated active-player list, the
// rename dialog, and hard-delete with an active-game guard. It is stateless — the
// target player id and page travel in each button's signed nav-state.
type Screen struct {
	db       *sql.DB
	poster   mm.Poster
	nav      *nav.Builder
	log      *slog.Logger
	mainMenu MainMenuRenderer
}

// Option configures a Screen.
type Option func(*Screen)

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

// Owns reports the action codes the player-management flow claims. It claims
// menu:players (the menu lists it as a fallback default), so it must be
// registered ahead of the menu in the router.
func (s *Screen) Owns(action string) bool {
	switch action {
	case mm.ActMenuPlayers,
		mm.ActMgmtRename, mm.ActMgmtDelete, mm.ActMgmtPage, mm.ActMgmtNoop, mm.ActMgmtBack,
		mm.ActMgmtDeleteConfirm, mm.ActMgmtDeleteCancel:
		return true
	default:
		return false
	}
}

// Action handles a verified button click for the player-management flow.
func (s *Screen) Action(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	switch ns.Action {
	case mm.ActMenuPlayers:
		return s.renderList(ctx, 0)
	case mm.ActMgmtPage:
		return s.renderList(ctx, ns.Page)
	case mm.ActMgmtNoop:
		// The page-indicator button is inert.
		return &model.PostActionIntegrationResponse{}, nil
	case mm.ActMgmtBack:
		if s.mainMenu != nil {
			return s.mainMenu(), nil
		}
		return nav.ExpiredResponse(), nil
	case mm.ActMgmtRename:
		return s.openRename(ctx, req, ns)
	case mm.ActMgmtDelete:
		return s.deletePrompt(ctx, ns)
	case mm.ActMgmtDeleteConfirm:
		return s.deleteConfirm(ctx, ns)
	case mm.ActMgmtDeleteCancel:
		return s.renderList(ctx, ns.Page)
	default:
		return nav.ExpiredResponse(), nil
	}
}

// renderList re-renders the player-management list at the (clamped) page in
// place.
func (s *Screen) renderList(ctx context.Context, page int) (*model.PostActionIntegrationResponse, error) {
	all, err := db.GetAllActivePlayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	message, att := s.listScreen(all, page)
	return nav.UpdateResponse(message, att), nil
}

// listScreen builds the player-management list body + interactive attachment for
// the given players and (clamped) page.
func (s *Screen) listScreen(all []db.Player, page int) (string, []*model.SlackAttachment) {
	if len(all) == 0 {
		actions := nav.CompactActions([]*model.PostAction{
			s.nav.Button("← Back", mm.NavState{Action: mm.ActMgmtBack}),
		})
		return emptyList, []*model.SlackAttachment{{Actions: actions}}
	}

	totalPages := (len(all) + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 0 {
		page = 0
	}
	if page > totalPages-1 {
		page = totalPages - 1
	}
	start := page * perPage
	end := start + perPage
	if end > len(all) {
		end = len(all)
	}
	pagePlayers := all[start:end]

	actions := make([]*model.PostAction, 0, len(pagePlayers)*2+4)
	for _, p := range pagePlayers {
		actions = append(actions,
			s.nav.Button("✏️ "+p.Name, mm.NavState{Action: mm.ActMgmtRename, PlayerID: p.ID, Page: page}),
			s.nav.Button("🗑 "+p.Name, mm.NavState{Action: mm.ActMgmtDelete, PlayerID: p.ID, Page: page}),
		)
	}
	if totalPages > 1 {
		if page > 0 {
			actions = append(actions, s.nav.Button("← Prev", mm.NavState{Action: mm.ActMgmtPage, Page: page - 1}))
		}
		actions = append(actions, s.nav.Button(fmt.Sprintf("%d/%d", page+1, totalPages), mm.NavState{Action: mm.ActMgmtNoop}))
		if page+1 < totalPages {
			actions = append(actions, s.nav.Button("Next →", mm.NavState{Action: mm.ActMgmtPage, Page: page + 1}))
		}
	}
	actions = append(actions, s.nav.Button("← Back", mm.NavState{Action: mm.ActMgmtBack}))

	text := fmt.Sprintf("Players (%d):\nTap ✏️ to rename, 🗑 to delete.", len(all))
	return text, []*model.SlackAttachment{{Actions: nav.CompactActions(actions)}}
}

// openRename opens the rename dialog as the FIRST action (the trigger_id window
// is ~3s), sealing the target player + page + originating post id into the
// signed dialog state so the submit handler can re-render the list in place.
func (s *Screen) openRename(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	state, err := s.nav.SignState(mm.NavState{
		Action:   mm.ActMgmtRename,
		PlayerID: ns.PlayerID,
		Page:     ns.Page,
		PostID:   req.PostId,
	})
	if err != nil {
		s.log.Error("players: sign dialog state failed", "err", err.Error())
		return nav.Ephemeral("Couldn't open the dialog — try again."), nil
	}
	dialog := model.Dialog{
		CallbackId:  dialogCallback,
		Title:       renameTitle,
		SubmitLabel: "Rename",
		State:       state,
		Elements: []model.DialogElement{{
			DisplayName: "New name",
			Name:        "name",
			Type:        "text",
			SubType:     "text",
			MinLength:   minNameLen,
			MaxLength:   maxNameLen,
		}},
	}
	if err := s.poster.OpenDialog(ctx, req.TriggerId, s.nav.DialogURL(), dialog); err != nil {
		s.log.Error("players: open dialog failed", "err", err.Error())
		return nav.Ephemeral("Couldn't open the dialog — try again."), nil
	}
	// The originating post stays as the list; the dialog submit re-renders it in
	// place via UpdateAttachment.
	return &model.PostActionIntegrationResponse{}, nil
}

// Dialog handles the rename dialog submission.
func (s *Screen) Dialog(ctx context.Context, req *model.SubmitDialogRequest, ns mm.NavState) (*model.SubmitDialogResponse, error) {
	if ns.Action != mm.ActMgmtRename {
		// Not ours (defensive — the router already dispatched by Owns).
		return &model.SubmitDialogResponse{}, nil
	}

	raw, _ := req.Submission["name"].(string)
	name := strings.TrimSpace(raw)
	if err := validateName(name); err != nil {
		return &model.SubmitDialogResponse{Errors: map[string]string{"name": err.Error()}}, nil
	}

	// Uniqueness across ALL rows (including soft-deleted) pre-empts the
	// players.name UNIQUE index, surfacing a friendly error instead of a 500.
	taken, err := db.NameExists(ctx, s.db, name, ns.PlayerID)
	if err != nil {
		return nil, err
	}
	if taken {
		return &model.SubmitDialogResponse{
			Errors: map[string]string{"name": fmt.Sprintf("%q is already taken.", name)},
		}, nil
	}

	if err := db.RenamePlayer(ctx, s.db, ns.PlayerID, name); err != nil {
		return nil, err
	}

	// Re-render the originating list post in place (a SubmitDialogResponse cannot
	// itself update a post). Updating a since-deleted post is a benign no-op.
	all, err := db.GetAllActivePlayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	message, att := s.listScreen(all, ns.Page)
	if err := s.poster.UpdateAttachment(ctx, ns.PostID, message, att); err != nil {
		return nil, err
	}
	return &model.SubmitDialogResponse{}, nil
}

// deletePrompt renders the delete-confirmation for the target player, carrying
// the player id and page in the confirm button's signed context.
func (s *Screen) deletePrompt(ctx context.Context, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	all, err := db.GetAllActivePlayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	name := nameOf(ns.PlayerID, all)
	if name == "" {
		// Already gone — just re-render the list.
		return s.renderList(ctx, ns.Page)
	}
	text := fmt.Sprintf("Delete %s and all their game history?\nThis cannot be undone.", name)
	actions := nav.CompactActions([]*model.PostAction{
		s.nav.Button("Yes, Delete", mm.NavState{Action: mm.ActMgmtDeleteConfirm, PlayerID: ns.PlayerID, Page: ns.Page}),
		s.nav.Button("Cancel", mm.NavState{Action: mm.ActMgmtDeleteCancel, Page: ns.Page}),
	})
	return nav.UpdateResponse(text, []*model.SlackAttachment{{Actions: actions}}), nil
}

// deleteConfirm hard-deletes the player after guarding against an active game, so
// a stale score:player button can't later reference a deleted player.
func (s *Screen) deleteConfirm(ctx context.Context, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	inGame, err := db.IsPlayerInUnfinishedGame(ctx, s.db, ns.PlayerID)
	if err != nil {
		return nil, err
	}
	if inGame {
		all, err := db.GetAllActivePlayers(ctx, s.db)
		if err != nil {
			return nil, err
		}
		name := nameOf(ns.PlayerID, all)
		if name == "" {
			name = "that player"
		}
		return nav.Ephemeral(fmt.Sprintf("Can't delete %s — they're in an active game. End or discard it first.", name)), nil
	}
	if err := db.HardDeletePlayer(ctx, s.db, ns.PlayerID); err != nil {
		return nil, err
	}
	return s.renderList(ctx, ns.Page)
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

// validateName enforces the player-name rules: 1–50 characters, no control
// characters, newlines, or backticks. The name is assumed trimmed.
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
