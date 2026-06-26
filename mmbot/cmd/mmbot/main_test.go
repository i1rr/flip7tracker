package main

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
	"github.com/rivan/flip7bot/mmbot/internal/server"
)

const testHMACKey = "0123456789abcdef0123456789abcdef" // 32 bytes

// fakePoster satisfies mm.Poster, recording the calls the wiring exercises.
type fakePoster struct {
	attachmentPosts int
}

func (f *fakePoster) PostMessage(context.Context, string, string) (string, error) { return "p", nil }
func (f *fakePoster) PostInThread(context.Context, string, string, string) (string, error) {
	return "p", nil
}
func (f *fakePoster) PostAttachment(context.Context, string, string, []*model.SlackAttachment) (string, error) {
	f.attachmentPosts++
	return "p", nil
}
func (f *fakePoster) PostAttachmentInThread(context.Context, string, string, string, []*model.SlackAttachment) (string, error) {
	return "p", nil
}
func (f *fakePoster) UpdatePost(context.Context, string, string) error { return nil }
func (f *fakePoster) UpdateAttachment(context.Context, string, string, []*model.SlackAttachment) error {
	return nil
}
func (f *fakePoster) DeletePost(context.Context, string) error                       { return nil }
func (f *fakePoster) OpenDialog(context.Context, string, string, model.Dialog) error { return nil }

func newWiring(t *testing.T) (server.Handlers, *fakePoster, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := db.Migrate(context.Background(), database); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	signer, err := mm.NewSigner(testHMACKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	poster := &fakePoster{}
	builder := nav.NewBuilder(signer, "http://example.test:8068", slog.Default())
	h := buildHandlers(database, poster, builder, slog.Default(), "chan1")
	return h, poster, database
}

// TestBuildHandlersWired asserts every injection seam is satisfied: the three
// server callbacks are non-nil and route end-to-end against a migrated DB.
func TestBuildHandlersWired(t *testing.T) {
	h, _, _ := newWiring(t)
	if h.Slash == nil || h.Action == nil || h.Dialog == nil {
		t.Fatalf("handlers not fully wired: slash=%v action=%v dialog=%v", h.Slash == nil, h.Action == nil, h.Dialog == nil)
	}
}

// TestSlashOpensMenu verifies the empty-arg /flip7 posts the main menu to the
// owner channel via the Poster.
func TestSlashOpensMenu(t *testing.T) {
	h, poster, _ := newWiring(t)
	resp, err := h.Slash(context.Background(), &server.SlashRequest{Text: ""})
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if resp == nil {
		t.Fatal("nil slash response")
	}
	if poster.attachmentPosts != 1 {
		t.Fatalf("expected 1 menu post, got %d", poster.attachmentPosts)
	}
}

// TestActionRoutesToScreen drives a verified action through the combinedAction
// router into a real screen (statistics) backed by the migrated DB, confirming
// the screens are registered and reachable.
func TestActionRoutesToScreen(t *testing.T) {
	h, _, _ := newWiring(t)
	// Statistics is owned by the stats screen; an empty DB renders an (empty)
	// Hall of Fame in place rather than erroring.
	resp, err := h.Action(context.Background(),
		&model.PostActionIntegrationRequest{UserId: "owner"},
		mm.NavState{Action: mm.ActMenuStats})
	if err != nil {
		t.Fatalf("action: %v", err)
	}
	if resp == nil {
		t.Fatal("nil action response")
	}
}

// TestDialogCancelled confirms a cancelled dialog short-circuits to a benign
// success through the dialog router.
func TestDialogCancelled(t *testing.T) {
	h, _, _ := newWiring(t)
	resp, err := h.Dialog(context.Background(),
		&model.SubmitDialogRequest{Cancelled: true},
		mm.NavState{})
	if err != nil {
		t.Fatalf("dialog: %v", err)
	}
	if resp == nil {
		t.Fatal("nil dialog response")
	}
}
