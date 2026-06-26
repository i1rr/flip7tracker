package menu

import (
	"context"
	"log/slog"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
)

// mainMenuText is the prompt shown above the main-menu buttons.
const mainMenuText = "Welcome to Flip7! Choose an option:"

// helpText is the onboarding blurb returned by `/flip7 help`. It replaces the
// Telegram bot's /start and /help.
const helpText = "Flip7 Bot commands:\n" +
	"`/flip7` — Open the main menu\n" +
	"`/flip7 scoreboard` — Re-post the active game's scoreboard at the bottom\n" +
	"`/flip7 help` — Show this message\n\n" +
	"The host controls the game. Add players, start, then enter scores after each hand."

// noActiveGame is the ephemeral shown for `/flip7 scoreboard` when there is no
// active game (and as the default when no reposter is wired).
const noActiveGame = "No active game."

// ScoreboardReposter reposts the most-recent active game's scoreboard at the
// bottom of the channel for `/flip7 scoreboard`. It lives in the game package
// and is injected here so menu need not import game (menu imports only
// nav+mm+db). A nil reposter yields the "No active game." ephemeral.
type ScoreboardReposter func(ctx context.Context, channelID string) (*model.CommandResponse, error)

// Menu renders the main menu and the post-game navigation, and is the explicit
// default owner in the action dispatcher: any verified action no other screen
// claims routes here (main menu for the known nav codes, a benign "expired"
// ephemeral otherwise).
type Menu struct {
	poster     mm.Poster
	nav        *nav.Builder
	log        *slog.Logger
	scoreboard ScoreboardReposter
}

// Option configures a Menu.
type Option func(*Menu)

// WithScoreboardReposter wires the `/flip7 scoreboard` handler (the game
// package's reposter). Without it, `/flip7 scoreboard` replies "No active game.".
func WithScoreboardReposter(fn ScoreboardReposter) Option {
	return func(m *Menu) { m.scoreboard = fn }
}

// New constructs a Menu. A nil logger falls back to slog.Default.
func New(poster mm.Poster, builder *nav.Builder, log *slog.Logger, opts ...Option) *Menu {
	if log == nil {
		log = slog.Default()
	}
	m := &Menu{poster: poster, nav: builder, log: log}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// mainMenuAttachments builds the interactive main-menu attachment. Buttons that
// fail to sign are dropped (CompactActions) so the screen still renders.
func (m *Menu) mainMenuAttachments() []*model.SlackAttachment {
	actions := nav.CompactActions([]*model.PostAction{
		m.nav.Button("New Game", mm.NavState{Action: mm.ActMenuNewGame}),
		m.nav.Button("Load Game", mm.NavState{Action: mm.ActMenuLoadGame}),
		m.nav.Button("Statistics", mm.NavState{Action: mm.ActMenuStats}),
		m.nav.Button("\U0001F465 Players", mm.NavState{Action: mm.ActMenuPlayers}),
	})
	return []*model.SlackAttachment{{Actions: actions}}
}

// MainMenuResponse re-renders the originating post into the main menu in place.
// It is the "← Back" target injected into screen packages (e.g.
// newgame.WithMainMenuRenderer) so they can return to the menu without importing
// menu's unexported builders.
func (m *Menu) MainMenuResponse() *model.PostActionIntegrationResponse {
	return nav.UpdateResponse(mainMenuText, m.mainMenuAttachments())
}

// PostMainMenu posts a fresh main-menu nav post to the channel and returns its
// id. Used by `/flip7` (empty arg).
func (m *Menu) PostMainMenu(ctx context.Context, channelID string) (string, error) {
	return m.poster.PostAttachment(ctx, channelID, mainMenuText, m.mainMenuAttachments())
}

// Slash routes the `/flip7` subcommands. channelID is the originating channel;
// text is the raw argument string. The empty arg opens the main menu (posting a
// fresh nav post and returning an empty response); `help` returns the onboarding
// ephemeral; `scoreboard` delegates to the wired reposter (or "No active game.").
func (m *Menu) Slash(ctx context.Context, channelID, text string) (*model.CommandResponse, error) {
	arg := strings.ToLower(strings.TrimSpace(text))
	switch arg {
	case "":
		if _, err := m.PostMainMenu(ctx, channelID); err != nil {
			return nil, err
		}
		// The menu is posted directly; the command itself stays silent.
		return &model.CommandResponse{}, nil
	case "scoreboard":
		if m.scoreboard == nil {
			return ephemeral(noActiveGame), nil
		}
		return m.scoreboard(ctx, channelID)
	case "help":
		return ephemeral(helpText), nil
	default:
		// Unknown argument: show the onboarding blurb.
		return ephemeral(helpText), nil
	}
}

// Owns reports the action codes the menu claims: the four top-level menu entries
// and the post-game navigation (home/new/stats). As the default owner it is
// consulted last in the dispatcher, so dedicated screen packages that also claim
// menu:new_game / game:new / etc. (registered earlier) take precedence.
func (m *Menu) Owns(action string) bool {
	switch action {
	case mm.ActMenuNewGame, mm.ActMenuLoadGame, mm.ActMenuStats, mm.ActMenuPlayers,
		mm.ActGameHome, mm.ActGameNew, mm.ActGameStats:
		return true
	default:
		return false
	}
}

// Action handles a verified button click. The known menu/post-game nav codes
// re-render the originating post into the main menu; anything else (the default
// fallthrough) yields a benign "expired" ephemeral, so the dispatch partition is
// total.
func (m *Menu) Action(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	switch ns.Action {
	case mm.ActMenuNewGame, mm.ActMenuLoadGame, mm.ActMenuStats, mm.ActMenuPlayers,
		mm.ActGameHome, mm.ActGameNew, mm.ActGameStats:
		return nav.UpdateResponse(mainMenuText, m.mainMenuAttachments()), nil
	default:
		return nav.ExpiredResponse(), nil
	}
}

// ephemeral builds an ephemeral slash command response.
func ephemeral(text string) *model.CommandResponse {
	return &model.CommandResponse{
		ResponseType: model.CommandResponseTypeEphemeral,
		Text:         text,
	}
}
