package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/rivan/flip7bot/mmbot/internal/mm"
)

const (
	// maxBodyBytes caps every authenticated request body. Slash/action/dialog
	// payloads are small; 1 MiB is generous and stops a memory-exhaustion POST.
	maxBodyBytes = 1 << 20

	// requestTimeout bounds the time any single inbound request may take. It is
	// generous enough for a synchronous dialog-open (which must land inside the
	// ~3s trigger_id window) plus the small Mattermost round-trips a handler
	// makes, while still shedding a stuck request.
	requestTimeout = 15 * time.Second

	// readHeaderTimeout guards against slow-loris header trickling.
	readHeaderTimeout = 5 * time.Second

	// shutdownTimeout bounds graceful drain on Run() context cancellation.
	shutdownTimeout = 10 * time.Second

	// NavContextKey is the key under which the HMAC-signed nav-state token is
	// carried inside an action `context` map. The token (not the raw key) is the
	// only authenticator; the signing key is never embedded in any post.
	NavContextKey = "nav"
)

// SlashRequest is the parsed application/x-www-form-urlencoded payload
// Mattermost POSTs for a slash command.
type SlashRequest struct {
	Token       string
	TeamID      string
	TeamDomain  string
	ChannelID   string
	ChannelName string
	UserID      string
	UserName    string
	Command     string
	Text        string
	ResponseURL string
	TriggerID   string
}

// Handlers are the pluggable business-logic callbacks. Later batches set the
// fields they implement; a nil field yields a benign "not yet wired" response so
// the listener (and its auth) can be stood up and tested before the navigation
// handlers exist. Each callback receives an already-authenticated request.
type Handlers struct {
	// Slash handles the /flip7 slash command (and its arg routing). It returns a
	// Mattermost CommandResponse (ephemeral or in-channel).
	Slash SlashFunc
	// Action handles a verified interactive button click. It receives the parsed
	// request and the verified nav-state decoded from the action `context`.
	Action ActionFunc
	// Dialog handles a verified dialog submission. It receives the parsed request
	// and the verified nav-state decoded from the dialog `state`.
	Dialog DialogFunc
}

// Config holds the listener's static configuration and authenticators.
type Config struct {
	ListenAddr      string
	OwnerUserID     string
	SlashTokenFlip7 string
	Signer          *mm.Signer
	Logger          *slog.Logger
	// DialogStateMaxAge optionally bounds the freshness of a dialog `state`
	// HMAC. Zero disables the window (action `context` HMACs are ALWAYS verified
	// with no max-age — a game's scoreboard/end buttons must verify for the
	// game's whole life).
	DialogStateMaxAge time.Duration
}

// Server is the inbound HTTP listener.
type Server struct {
	cfg      Config
	handlers Handlers
	log      *slog.Logger
	httpSrv  *http.Server
}

// New constructs a Server. The signer and a non-empty listen address are
// required.
func New(cfg Config, handlers Handlers) (*Server, error) {
	if cfg.Signer == nil {
		return nil, errors.New("server: nil signer")
	}
	if cfg.ListenAddr == "" {
		return nil, errors.New("server: empty listen address")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, handlers: handlers, log: log}, nil
}

// Handler builds the route mux. It is exported so tests can drive the listener
// via httptest without binding a socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health: a static 200 with no version/build/config echoed. GET/HEAD only.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Slash route — per-command bearer token + owner check.
	mux.Handle("/slash/flip7", s.withLimits(s.slashHandler(s.cfg.SlashTokenFlip7, func() SlashFunc { return s.handlers.Slash })))

	// Action / dialog — HMAC over nav-state + owner check.
	mux.Handle("/action", s.withLimits(http.HandlerFunc(s.actionHandler)))
	mux.Handle("/dialog", s.withLimits(http.HandlerFunc(s.dialogHandler)))

	return mux
}

// withLimits wraps an authenticated route with a body-size cap and a
// per-request context timeout. Health is intentionally excluded.
func (s *Server) withLimits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Run binds the listener and serves until ctx is cancelled, then drains
// gracefully (bounded by shutdownTimeout). The caller (main) wires SIGTERM to
// the cancellation of ctx, giving a graceful shutdown on SIGTERM.
//
// The listener binds 0.0.0.0:<port> inside the container; the port is published
// only to the Mattermost bridge, never to the host, so there is no gateway-IP
// bind race to retry around (unlike jobHunter's gateway-IP bind).
func (s *Server) Run(ctx context.Context) error {
	s.httpSrv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.ListenAddr)
	if err != nil {
		return err
	}

	errc := make(chan error, 1)
	go func() {
		s.log.Info("inbound listener serving", "listen_addr", ln.Addr().String())
		serveErr := s.httpSrv.Serve(ln)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errc <- serveErr
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		s.log.Info("inbound listener shutting down")
		return s.httpSrv.Shutdown(shutCtx)
	}
}
