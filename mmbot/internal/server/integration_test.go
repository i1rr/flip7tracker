package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
)

// fakePoster records the calls handlers make so the end-to-end test can assert
// the re-render seam fired. It satisfies mm.Poster.
type fakePoster struct {
	mu                sync.Mutex
	updateAttachments int
	lastUpdatedPostID string
}

var _ mm.Poster = (*fakePoster)(nil)

func (f *fakePoster) PostMessage(_ context.Context, _, _ string) (string, error) {
	return "newpost", nil
}
func (f *fakePoster) PostInThread(_ context.Context, _, _, _ string) (string, error) {
	return "reply", nil
}
func (f *fakePoster) PostAttachment(_ context.Context, _, _ string, _ []*model.SlackAttachment) (string, error) {
	return "newpost", nil
}
func (f *fakePoster) PostAttachmentInThread(_ context.Context, _, _, _ string, _ []*model.SlackAttachment) (string, error) {
	return "reply", nil
}
func (f *fakePoster) UpdatePost(_ context.Context, _, _ string) error { return nil }
func (f *fakePoster) UpdateAttachment(_ context.Context, postID, _ string, _ []*model.SlackAttachment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateAttachments++
	f.lastUpdatedPostID = postID
	return nil
}
func (f *fakePoster) DeletePost(_ context.Context, _ string) error                    { return nil }
func (f *fakePoster) OpenDialog(_ context.Context, _, _ string, _ model.Dialog) error { return nil }

func (f *fakePoster) updateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updateAttachments
}

func newMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return sqlDB
}

func scoreCount(t *testing.T, sqlDB *sql.DB, gameID int64) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM score_entries WHERE game_id = ?", gameID).Scan(&n); err != nil {
		t.Fatalf("count score entries: %v", err)
	}
	return n
}

// TestEndToEndActionAndDialogChain drives the real Handler() over an
// httptest.Server with an in-memory migrated DB and a fake Poster. It exercises
// the highest-risk seam (verify -> owner check -> DB write -> re-render on the
// Poster) for both /action and /dialog, and asserts that a tampered context and
// a non-owner request are rejected before any DB write or re-render.
func TestEndToEndActionAndDialogChain(t *testing.T) {
	ctx := context.Background()
	sqlDB := newMigratedDB(t)

	game, err := db.CreateGame(ctx, sqlDB, "chan1")
	if err != nil {
		t.Fatalf("CreateGame: %v", err)
	}
	alice, err := db.CreateOrGetPlayer(ctx, sqlDB, "Alice")
	if err != nil {
		t.Fatalf("CreateOrGetPlayer: %v", err)
	}
	bob, err := db.CreateOrGetPlayer(ctx, sqlDB, "Bob")
	if err != nil {
		t.Fatalf("CreateOrGetPlayer: %v", err)
	}
	for _, p := range []int64{alice.ID, bob.ID} {
		if err := db.AddPlayerToGame(ctx, sqlDB, game.ID, p); err != nil {
			t.Fatalf("AddPlayerToGame: %v", err)
		}
	}

	poster := &fakePoster{}

	// Synthetic action handler: emulates an Edit-style action that touches the DB
	// (adds a fixed score for Alice) and re-renders the root post via the Poster.
	actionFn := func(ctx context.Context, req *model.PostActionIntegrationRequest, nav mm.NavState) (*model.PostActionIntegrationResponse, error) {
		if _, err := db.AddScoreEntry(ctx, sqlDB, nav.GameID, nav.PlayerID, 12); err != nil {
			return nil, err
		}
		if err := poster.UpdateAttachment(ctx, req.PostId, "rerendered", nil); err != nil {
			return nil, err
		}
		post := &model.Post{Id: req.PostId, Message: "rerendered"}
		return &model.PostActionIntegrationResponse{Update: post}, nil
	}

	// Synthetic dialog handler: emulates a score-entry submit — validates the
	// typed points, writes the score, and re-renders the originating post via the
	// Poster (a SubmitDialogResponse cannot update a post itself).
	dialogFn := func(ctx context.Context, req *model.SubmitDialogRequest, nav mm.NavState) (*model.SubmitDialogResponse, error) {
		raw, _ := req.Submission["points"].(string)
		pts, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if perr != nil || pts < 0 || pts > 999 {
			return &model.SubmitDialogResponse{Errors: map[string]string{"points": "enter a number 0-999"}}, nil
		}
		if _, err := db.AddScoreEntry(ctx, sqlDB, nav.GameID, nav.PlayerID, pts); err != nil {
			return nil, err
		}
		if err := poster.UpdateAttachment(ctx, nav.PostID, "rerendered", nil); err != nil {
			return nil, err
		}
		return &model.SubmitDialogResponse{}, nil
	}

	srv, signer := newTestServer(t, Handlers{Action: actionFn, Dialog: dialogFn})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// --- /action happy path: verify -> owner -> DB write -> re-render ---------
	actionTok, err := signer.SignContext(mm.NavState{Action: mm.ActGameEdit, GameID: game.ID, PlayerID: alice.ID})
	if err != nil {
		t.Fatalf("SignContext: %v", err)
	}
	body := fmt.Sprintf(`{"user_id":%q,"channel_id":"chan1","post_id":"root1","context":{%q:%q}}`,
		testOwner, NavContextKey, actionTok)
	resp, err := http.Post(ts.URL+"/action", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /action: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/action code = %d want 200", resp.StatusCode)
	}
	if got := scoreCount(t, sqlDB, game.ID); got != 1 {
		t.Fatalf("after /action: score entries = %d want 1", got)
	}
	if poster.updateCount() != 1 {
		t.Fatalf("after /action: UpdateAttachment calls = %d want 1", poster.updateCount())
	}
	if poster.lastUpdatedPostID != "root1" {
		t.Fatalf("after /action: re-rendered post id = %q want root1", poster.lastUpdatedPostID)
	}

	// --- /dialog happy path: verify -> owner -> DB write -> re-render ---------
	dialogState, err := signer.SignState(mm.NavState{Action: mm.ActScorePlayer, GameID: game.ID, PlayerID: bob.ID, PostID: "root1"})
	if err != nil {
		t.Fatalf("SignState: %v", err)
	}
	dlgBody := dialogBody(t, dialogState, testOwner, map[string]any{"points": "25"})
	resp, err = http.Post(ts.URL+"/dialog", "application/json", strings.NewReader(dlgBody))
	if err != nil {
		t.Fatalf("POST /dialog: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/dialog code = %d want 200", resp.StatusCode)
	}
	if got := scoreCount(t, sqlDB, game.ID); got != 2 {
		t.Fatalf("after /dialog: score entries = %d want 2", got)
	}
	if poster.updateCount() != 2 {
		t.Fatalf("after /dialog: UpdateAttachment calls = %d want 2", poster.updateCount())
	}

	// --- Rejected before any DB write/re-render -------------------------------
	priorScores := scoreCount(t, sqlDB, game.ID)
	priorUpdates := poster.updateCount()

	// (1) Tampered context: flip a byte in the signed payload.
	parts := strings.SplitN(actionTok, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected token shape: %q", actionTok)
	}
	tampered := flipFirst(parts[0]) + "." + parts[1]
	tamperBody := fmt.Sprintf(`{"user_id":%q,"channel_id":"chan1","post_id":"root1","context":{%q:%q}}`,
		testOwner, NavContextKey, tampered)
	resp, err = http.Post(ts.URL+"/action", "application/json", strings.NewReader(tamperBody))
	if err != nil {
		t.Fatalf("POST tampered /action: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered /action code = %d want 401", resp.StatusCode)
	}

	// (2) Non-owner with a valid signature.
	nonOwnerBody := fmt.Sprintf(`{"user_id":"intruder","channel_id":"chan1","post_id":"root1","context":{%q:%q}}`,
		NavContextKey, actionTok)
	resp, err = http.Post(ts.URL+"/action", "application/json", strings.NewReader(nonOwnerBody))
	if err != nil {
		t.Fatalf("POST non-owner /action: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("non-owner /action code = %d want 401", resp.StatusCode)
	}

	if got := scoreCount(t, sqlDB, game.ID); got != priorScores {
		t.Fatalf("rejected requests mutated DB: score entries = %d want %d", got, priorScores)
	}
	if poster.updateCount() != priorUpdates {
		t.Fatalf("rejected requests re-rendered: UpdateAttachment calls = %d want %d", poster.updateCount(), priorUpdates)
	}
}
