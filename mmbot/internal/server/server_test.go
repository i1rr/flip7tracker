package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
)

const (
	testHMACKey  = "0123456789abcdef0123456789abcdef" // exactly 32 bytes
	testOwner    = "owner-user-id-0001"
	testSlashTok = "flip7-token-secret"
)

func newTestServer(t *testing.T, h Handlers) (*Server, *mm.Signer) {
	t.Helper()
	signer, err := mm.NewSigner(testHMACKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	srv, err := New(Config{
		ListenAddr:      "127.0.0.1:0",
		OwnerUserID:     testOwner,
		SlashTokenFlip7: testSlashTok,
		Signer:          signer,
	}, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, signer
}

// okHandlers returns Handlers whose every callback succeeds, so a 200 means the
// auth middleware admitted the request.
func okHandlers() Handlers {
	return Handlers{
		Slash: func(_ context.Context, _ *SlashRequest) (*model.CommandResponse, error) {
			return &model.CommandResponse{Text: "ok"}, nil
		},
		Action: func(_ context.Context, _ *model.PostActionIntegrationRequest, _ mm.NavState) (*model.PostActionIntegrationResponse, error) {
			return &model.PostActionIntegrationResponse{EphemeralText: "ok"}, nil
		},
		Dialog: func(_ context.Context, _ *model.SubmitDialogRequest, _ mm.NavState) (*model.SubmitDialogResponse, error) {
			return &model.SubmitDialogResponse{}, nil
		},
	}
}

func slashForm(token, userID string) io.Reader {
	v := url.Values{}
	v.Set("token", token)
	v.Set("user_id", userID)
	v.Set("channel_id", "chan1")
	v.Set("team_id", "team1")
	v.Set("command", "/flip7")
	v.Set("trigger_id", "trig1")
	return strings.NewReader(v.Encode())
}

func actionBody(t *testing.T, navToken, userID string) string {
	t.Helper()
	req := model.PostActionIntegrationRequest{
		UserId:    userID,
		ChannelId: "chan1",
		PostId:    "post1",
		TriggerId: "trig1",
		Context:   map[string]any{NavContextKey: navToken},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	return string(b)
}

func dialogBody(t *testing.T, state, userID string, submission map[string]any) string {
	t.Helper()
	req := model.SubmitDialogRequest{
		UserId:     userID,
		ChannelId:  "chan1",
		State:      state,
		CallbackId: "cb1",
		Submission: submission,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal dialog: %v", err)
	}
	return string(b)
}

// --- Slash route auth -------------------------------------------------------

func TestSlashAuth(t *testing.T) {
	srv, _ := newTestServer(t, okHandlers())
	h := srv.Handler()

	cases := []struct {
		name     string
		token    string
		userID   string
		wantCode int
	}{
		{"valid", testSlashTok, testOwner, http.StatusOK},
		{"wrong token", "nope", testOwner, http.StatusUnauthorized},
		{"empty token", "", testOwner, http.StatusUnauthorized},
		{"wrong user", testSlashTok, "intruder", http.StatusUnauthorized},
		{"valid token empty user", testSlashTok, "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/slash/flip7", slashForm(tc.token, tc.userID))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantCode {
				t.Fatalf("code = %d want %d", w.Code, tc.wantCode)
			}
		})
	}
}

func TestSlashMethodNotPost(t *testing.T) {
	srv, _ := newTestServer(t, okHandlers())
	r := httptest.NewRequest(http.MethodGet, "/slash/flip7", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("GET to slash route should not be admitted, got %d", w.Code)
	}
}

// --- Action route auth ------------------------------------------------------

func TestActionAuth(t *testing.T) {
	srv, signer := newTestServer(t, okHandlers())
	h := srv.Handler()

	validTok, err := signer.SignContext(mm.NavState{Action: mm.ActGameEdit, GameID: 7})
	if err != nil {
		t.Fatalf("SignContext: %v", err)
	}
	// Tamper: flip a payload char so the MAC fails.
	parts := strings.SplitN(validTok, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected token shape: %q", validTok)
	}
	tamperedTok := flipFirst(parts[0]) + "." + parts[1]
	// Forged with a different (valid-length) key.
	other, _ := mm.NewSigner("ffffffffffffffffffffffffffffffff")
	forgedTok, _ := other.SignContext(mm.NavState{Action: mm.ActGameEdit, GameID: 7})

	cases := []struct {
		name     string
		navToken string
		userID   string
		wantCode int
	}{
		{"valid", validTok, testOwner, http.StatusOK},
		{"missing signature", "", testOwner, http.StatusUnauthorized},
		{"tampered hmac", tamperedTok, testOwner, http.StatusUnauthorized},
		{"forged hmac wrong key", forgedTok, testOwner, http.StatusUnauthorized},
		{"valid hmac wrong user", validTok, "intruder", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(actionBody(t, tc.navToken, tc.userID)))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantCode {
				t.Fatalf("code = %d want %d", w.Code, tc.wantCode)
			}
		})
	}
}

// --- Dialog route auth ------------------------------------------------------

func TestDialogAuth(t *testing.T) {
	srv, signer := newTestServer(t, okHandlers())
	h := srv.Handler()

	validState, err := signer.SignState(mm.NavState{Action: mm.ActScorePlayer, GameID: 7, PlayerID: 3, PostID: "post1"})
	if err != nil {
		t.Fatalf("SignState: %v", err)
	}

	cases := []struct {
		name     string
		state    string
		userID   string
		wantCode int
	}{
		{"valid", validState, testOwner, http.StatusOK},
		{"missing signature", "", testOwner, http.StatusUnauthorized},
		{"garbage state", "not-a-valid-token", testOwner, http.StatusUnauthorized},
		{"valid state wrong user", validState, "intruder", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/dialog", strings.NewReader(dialogBody(t, tc.state, tc.userID, nil)))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantCode {
				t.Fatalf("code = %d want %d", w.Code, tc.wantCode)
			}
		})
	}
}

// TestDialogStateMaxAge verifies the optional freshness window on dialog state:
// a state older than DialogStateMaxAge is rejected at the middleware (401),
// while a fresh one is admitted.
func TestDialogStateMaxAge(t *testing.T) {
	signer, err := mm.NewSigner(testHMACKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	srv, err := New(Config{
		ListenAddr:        "127.0.0.1:0",
		OwnerUserID:       testOwner,
		SlashTokenFlip7:   testSlashTok,
		Signer:            signer,
		DialogStateMaxAge: 5 * time.Minute,
	}, okHandlers())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := srv.Handler()

	// Stale: IssuedAt well outside the window.
	staleState, err := signer.SignState(mm.NavState{Action: mm.ActScorePlayer, IssuedAt: time.Now().Add(-time.Hour).Unix()})
	if err != nil {
		t.Fatalf("SignState: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/dialog", strings.NewReader(dialogBody(t, staleState, testOwner, nil)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("stale dialog state code = %d want 401", w.Code)
	}

	// Fresh: IssuedAt now.
	freshState, err := signer.SignState(mm.NavState{Action: mm.ActScorePlayer})
	if err != nil {
		t.Fatalf("SignState: %v", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/dialog", strings.NewReader(dialogBody(t, freshState, testOwner, nil)))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("fresh dialog state code = %d want 200", w.Code)
	}
}

// --- Oversized body ---------------------------------------------------------

func TestOversizedBodyRejected(t *testing.T) {
	srv, _ := newTestServer(t, okHandlers())
	h := srv.Handler()

	// A body well over maxBodyBytes — MaxBytesReader must abort the decode,
	// yielding a non-2xx (the body never reaches the auth comparison cleanly).
	big := strings.Repeat("a", maxBodyBytes+1024)
	body := `{"user_id":"` + testOwner + `","context":{"nav":"` + big + `"}}`

	r := httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("oversized body must be rejected, got %d", w.Code)
	}
}

// --- Health -----------------------------------------------------------------

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t, Handlers{})
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("healthz code = %d want 200", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "ok" {
		t.Fatalf("healthz body = %q want %q (must echo nothing else)", got, "ok")
	}
}

func TestHealthzHeadAllowedPostRejected(t *testing.T) {
	srv, _ := newTestServer(t, Handlers{})
	h := srv.Handler()

	rHead := httptest.NewRequest(http.MethodHead, "/healthz", nil)
	wHead := httptest.NewRecorder()
	h.ServeHTTP(wHead, rHead)
	if wHead.Code != http.StatusOK {
		t.Fatalf("HEAD /healthz code = %d want 200", wHead.Code)
	}

	rPost := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	wPost := httptest.NewRecorder()
	h.ServeHTTP(wPost, rPost)
	if wPost.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz code = %d want 405", wPost.Code)
	}
}

// --- Nil handlers (benign stubs) --------------------------------------------

func TestNilHandlersAdmitButStub(t *testing.T) {
	srv, signer := newTestServer(t, Handlers{}) // no business handlers wired
	h := srv.Handler()

	// A fully-authenticated action still returns 200 with a stub response.
	tok, _ := signer.SignContext(mm.NavState{Action: mm.ActMenuNewGame})
	r := httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(actionBody(t, tok, testOwner)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("stubbed action code = %d want 200", w.Code)
	}
}

func flipFirst(s string) string {
	if s == "" {
		return "x"
	}
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}
