// Command mmbot is the flip7bot Mattermost bot entry point. Full wiring (DB,
// Mattermost client, Resolve, signer, poster, HTTP server) is added in later
// batches (see plan.md Step 21). For now it loads and validates configuration
// and initialises process-wide structured logging so the scaffold builds and
// fail-fast config behaviour can be exercised.
package main

import (
	"log/slog"
	"os"

	"github.com/rivan/flip7bot/mmbot/internal/config"
)

func main() {
	// envPath is the optional .env file; missing is non-fatal.
	cfg, err := config.Load(".env")
	if err != nil {
		// Logging is not configured yet, so write the fatal config error to
		// stderr and exit non-zero.
		slog.New(slog.NewTextHandler(os.Stderr, nil)).
			Error("configuration error", "err", err.Error())
		os.Exit(1)
	}

	// A single process-wide text handler at the configured level.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(logger)

	logger.Info("flip7bot configuration loaded", "config", cfg.Redacted())
	logger.Info("scaffold ready; service wiring lands in a later batch")
}
