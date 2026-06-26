// Command mmbot is the flip7bot Mattermost bot entry point and composition root.
// It wires every internal package together: config load → DB open + embedded
// migrations → Mattermost Resolve (team/channel/owner/bot ids) → legacy-channel
// backfill → HMAC signer + Poster → the screen packages and their cross-package
// injection seams (start renderer, main-menu renderer, scoreboard reposter) →
// the action/dialog routers → the inbound HTTP server, served with a graceful
// SIGTERM/SIGINT shutdown.
//
// The /action dispatcher is the combinedAction composition: a nav.ActionRouter
// that routes a verified NavState to the first screen package whose Owns(action)
// returns true (menu being the explicit default owner). This keeps the nav
// package free of any screen-package import — the dependency direction stays
// acyclic: nav ← screens ← main.
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/rivan/flip7bot/mmbot/internal/config"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/game"
	"github.com/rivan/flip7bot/mmbot/internal/menu"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
	"github.com/rivan/flip7bot/mmbot/internal/newgame"
	"github.com/rivan/flip7bot/mmbot/internal/players"
	"github.com/rivan/flip7bot/mmbot/internal/server"
	"github.com/rivan/flip7bot/mmbot/internal/stats"
)

// dialogStateMaxAge bounds the freshness of a dialog `state` HMAC. The dialog is
// opened inside the ~3s trigger_id window but a human may take a little while to
// type; ten minutes is generous while still expiring a stale dialog. Action
// `context` HMACs are always verified with NO max-age (a game's scoreboard/end
// buttons must verify for the game's whole life).
const dialogStateMaxAge = 10 * time.Minute

// startupTimeout bounds the blocking startup work (Resolve + migrate + backfill)
// so a wedged Mattermost server cannot hang the process forever.
const startupTimeout = 30 * time.Second

func main() {
	// Load and validate configuration before logging is fully configured, so a
	// fatal config error still surfaces on stderr.
	cfg, err := config.Load(".env")
	if err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).
			Error("configuration error", "err", err.Error())
		os.Exit(1)
	}

	// A single process-wide text handler at the configured level.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(logger)
	logger.Info("flip7bot configuration loaded", "config", cfg.Redacted())

	if err := run(cfg, logger); err != nil {
		logger.Error("fatal", "err", err.Error())
		os.Exit(1)
	}
	logger.Info("flip7bot shut down cleanly")
}

// run performs the full wiring and serves until SIGTERM/SIGINT. It returns the
// first fatal error (or nil on a clean graceful shutdown).
func run(cfg *config.Config, logger *slog.Logger) error {
	// Serve-lifetime context: cancelled on SIGTERM/SIGINT to trigger a graceful
	// drain.
	serveCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// --- Storage: open the DB and run embedded migrations (idempotent). ---
	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	startupCtx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	if err := db.Migrate(startupCtx, database); err != nil {
		return err
	}
	logger.Info("database opened and migrations applied", "database_url", cfg.DatabaseURL)

	// --- Mattermost: client + Resolve (team/channel/owner/bot ids). ---
	api := mm.NewAPIClient(cfg.MMURL, cfg.MMBotToken)
	resolved, err := mm.Resolve(startupCtx, api, logger, cfg.MMTeam, cfg.MMChannel, cfg.OwnerUsername, cfg.OwnerUserID)
	if err != nil {
		return err
	}
	logger.Info("mattermost resolved",
		"team_id", resolved.TeamID,
		"channel_id", resolved.ChannelID,
		"bot_user_id", resolved.BotUserID,
		"owner_user_id", resolved.OwnerUserID,
	)

	// --- Legacy-channel backfill: claim pre-migration channel_id='' rows (games
	// + last-players) into the configured owner channel. Idempotent. ---
	if err := db.BackfillLegacyChannel(startupCtx, database, resolved.ChannelID); err != nil {
		return err
	}

	// --- Signing + posting. ---
	signer, err := mm.NewSigner(cfg.HMACKey)
	if err != nil {
		return err
	}
	poster := mm.NewPoster(api, resolved.ChannelID)
	builder := nav.NewBuilder(signer, cfg.IntegrationBaseURL, logger)

	// --- Screen packages, routers, and the server Handlers (combinedAction). ---
	handlers := buildHandlers(database, poster, builder, logger, resolved.ChannelID)

	// --- HTTP server. ---
	srv, err := server.New(server.Config{
		ListenAddr:        cfg.ListenAddr,
		OwnerUserID:       resolved.OwnerUserID,
		SlashTokenFlip7:   cfg.SlashTokenFlip7,
		Signer:            signer,
		Logger:            logger,
		DialogStateMaxAge: dialogStateMaxAge,
	}, handlers)
	if err != nil {
		return err
	}

	logger.Info("flip7bot starting", "listen_addr", cfg.ListenAddr, "integration_base_url", cfg.IntegrationBaseURL)
	return srv.Run(serveCtx)
}

// buildHandlers constructs every screen package, wires the cross-package
// injection seams, composes the action/dialog routers (the combinedAction), and
// returns the server.Handlers. It is extracted from run so the wiring can be
// unit-tested with a fake Poster and an in-memory DB without needing a live
// Mattermost server. ownerChannelID is the resolved owner-only channel — all
// slash activity is routed there for consistency with the channel-scoped
// queries and the legacy backfill.
func buildHandlers(database *sql.DB, poster mm.Poster, builder *nav.Builder, logger *slog.Logger, ownerChannelID string) server.Handlers {
	// menu ↔ game is a mutual reference: the menu's `/flip7 scoreboard` reposter
	// is the game package's RepostScoreboard, while the game (and every other)
	// screen's "← Back" target is the menu's MainMenuResponse. We break the cycle
	// with a late-bound closure over gameScreen (assigned just below) for the
	// reposter; MainMenuResponse is a plain method value taken after the menu is
	// built.
	var gameScreen *game.Screen
	menuScreen := menu.New(poster, builder, logger,
		menu.WithScoreboardReposter(func(ctx context.Context, channelID string) (*model.CommandResponse, error) {
			return gameScreen.RepostScoreboard(ctx, channelID)
		}),
	)

	gameScreen = game.New(database, poster, builder, logger,
		game.WithMainMenuRenderer(menuScreen.MainMenuResponse),
	)
	newgameScreen := newgame.New(database, poster, builder, logger,
		newgame.WithStartRenderer(gameScreen.RenderRoot),
		newgame.WithMainMenuRenderer(menuScreen.MainMenuResponse),
	)
	playersScreen := players.New(database, poster, builder, logger,
		players.WithMainMenuRenderer(menuScreen.MainMenuResponse),
	)
	statsScreen := stats.New(database, builder, logger,
		stats.WithMainMenuRenderer(menuScreen.MainMenuResponse),
	)

	// Routers: dedicated screens are consulted before the menu, which is the
	// explicit default owner (fallback), so the action partition is total — any
	// verified action no screen claims renders the menu / a benign expired
	// ephemeral.
	actionRouter := nav.NewActionRouter(menuScreen, newgameScreen, gameScreen, playersScreen, statsScreen)
	dialogRouter := nav.NewDialogRouter(newgameScreen, gameScreen, playersScreen)

	return server.Handlers{
		Slash: func(ctx context.Context, req *server.SlashRequest) (*model.CommandResponse, error) {
			return menuScreen.Slash(ctx, ownerChannelID, req.Text)
		},
		Action: actionRouter.Action,
		Dialog: dialogRouter.Dialog,
	}
}
