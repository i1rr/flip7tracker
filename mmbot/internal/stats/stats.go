package stats

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
	"github.com/rivan/flip7bot/mmbot/internal/scoreboard"
)

// perPage is the page size of the Hall of Fame, matching the Rust flow (10 per
// page) and the scoreboard renderer's own page size.
const perPage = 10

// MainMenuRenderer re-renders the originating post into the main menu (used by
// "← Back"). It is injected from the menu package (menu.MainMenuResponse) so the
// stats package need not import menu. A nil renderer falls back to the benign
// expired ephemeral.
type MainMenuRenderer func() *model.PostActionIntegrationResponse

// Screen renders the Hall of Fame leaderboard and per-player detail. It is
// stateless — the page and target player id travel in each button's signed
// nav-state and the stats are re-read from SQLite on every interaction.
type Screen struct {
	db       *sql.DB
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
func New(database *sql.DB, builder *nav.Builder, log *slog.Logger, opts ...Option) *Screen {
	if log == nil {
		log = slog.Default()
	}
	s := &Screen{db: database, nav: builder, log: log}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Owns reports the action codes the statistics flow claims. It claims menu:stats
// (the menu lists it as a fallback default), so it must be registered ahead of
// the menu in the router.
func (s *Screen) Owns(action string) bool {
	switch action {
	case mm.ActMenuStats, mm.ActStatsPlayer, mm.ActStatsPage, mm.ActStatsBack, mm.ActStatsBackToList:
		return true
	default:
		return false
	}
}

// Action handles a verified button click for the statistics flow.
func (s *Screen) Action(ctx context.Context, _ *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	switch ns.Action {
	case mm.ActMenuStats, mm.ActStatsBackToList:
		return s.renderHallOfFame(ctx, 0)
	case mm.ActStatsPage:
		return s.renderHallOfFame(ctx, ns.Page)
	case mm.ActStatsPlayer:
		return s.renderDetail(ctx, ns.PlayerID)
	case mm.ActStatsBack:
		if s.mainMenu != nil {
			return s.mainMenu(), nil
		}
		return nav.ExpiredResponse(), nil
	default:
		return nav.ExpiredResponse(), nil
	}
}

// renderHallOfFame re-renders the Hall of Fame at the (clamped) page in place.
func (s *Screen) renderHallOfFame(ctx context.Context, page int) (*model.PostActionIntegrationResponse, error) {
	all, err := db.GetAllStats(ctx, s.db)
	if err != nil {
		return nil, err
	}
	message, att := s.hallOfFameScreen(all, page)
	return nav.UpdateResponse(message, att), nil
}

// hallOfFameScreen builds the Hall of Fame body + interactive attachment for the
// given (rating-sorted) stats and (clamped) page.
func (s *Screen) hallOfFameScreen(all []db.PlayerStats, page int) (string, []*model.SlackAttachment) {
	if len(all) == 0 {
		actions := nav.CompactActions([]*model.PostAction{
			s.nav.Button("← Back", mm.NavState{Action: mm.ActStatsBack}),
		})
		return scoreboard.RenderHallOfFame(all, 0), []*model.SlackAttachment{{Actions: actions}}
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
	pageStats := all[start:end]

	actions := make([]*model.PostAction, 0, len(pageStats)+3)
	for _, st := range pageStats {
		actions = append(actions, s.nav.Button(st.PlayerName, mm.NavState{
			Action: mm.ActStatsPlayer, PlayerID: st.PlayerID,
		}))
	}
	if page > 0 {
		actions = append(actions, s.nav.Button("← Prev", mm.NavState{Action: mm.ActStatsPage, Page: page - 1}))
	}
	if page+1 < totalPages {
		actions = append(actions, s.nav.Button("Next →", mm.NavState{Action: mm.ActStatsPage, Page: page + 1}))
	}
	actions = append(actions, s.nav.Button("← Back", mm.NavState{Action: mm.ActStatsBack}))

	return scoreboard.RenderHallOfFame(all, page), []*model.SlackAttachment{{Actions: nav.CompactActions(actions)}}
}

// renderDetail re-renders the per-player detail card in place.
func (s *Screen) renderDetail(ctx context.Context, playerID int64) (*model.PostActionIntegrationResponse, error) {
	stat, err := db.GetPlayerStats(ctx, s.db, playerID)
	if err != nil {
		if err == sql.ErrNoRows {
			// The player was deleted since the button was minted — fall back to the
			// list rather than erroring.
			return s.renderHallOfFame(ctx, 0)
		}
		return nil, err
	}
	text := scoreboard.RenderPlayerDetail(stat)
	actions := nav.CompactActions([]*model.PostAction{
		s.nav.Button("← Back to Stats", mm.NavState{Action: mm.ActStatsBackToList}),
	})
	return nav.UpdateResponse(text, []*model.SlackAttachment{{Actions: actions}}), nil
}
