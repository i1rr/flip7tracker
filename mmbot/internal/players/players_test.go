package players

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
)

const testKey = "0123456789abcdef0123456789abcdef" // 32 bytes
const testChannel = "chan1"

// fakePoster records the surface the player-management flow exercises.
type fakePoster struct {
	openedDialog model.Dialog
	openedTrig   string
	dialogs      int

	updatedPost string
	updatedMsg  string
	updatedAtt  []*model.SlackAttachment
	updates     int
}

func (f *fakePoster) PostMessage(context.Context, string, string) (string, error) { return "", nil }
func (f *fakePoster) PostInThread(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakePoster) PostAttachment(context.Context, string, string, []*model.SlackAttachment) (string, error) {
	return "", nil
}
func (f *fakePoster) PostAttachmentInThread(context.Context, string, string, string, []*model.SlackAttachment) (string, error) {
	return "", nil
}
func (f *fakePoster) UpdatePost(context.Context, string, string) error { return nil }
func (f *fakePoster) UpdateAttachment(_ context.Context, postID, message string, att []*model.SlackAttachment) error {
	f.updatedPost = postID
	f.updatedMsg = message
	f.updatedAtt = att
	f.updates++
	return nil
}
func (f *fakePoster) DeletePost(context.Context, string) error { return nil }
func (f *fakePoster) OpenDialog(_ context.Context, triggerID, _ string, dialog model.Dialog) error {
	f.openedTrig = triggerID
	f.openedDialog = dialog
	f.dialogs++
	return nil
}

func newTestScreen(t *testing.T, opts ...Option) (*Screen, *fakePoster, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(context.Background(), database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	signer, err := mm.NewSigner(testKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	p := &fakePoster{}
	b := nav.NewBuilder(signer, "http://bot.example:8068", nil)
	return New(database, p, b, nil, opts...), p, database
}

func seedPlayer(t *testing.T, database *sql.DB, name string) db.Player {
	t.Helper()
	pl, err := db.CreateOrGetPlayer(context.Background(), database, name)
	if err != nil {
		t.Fatalf("seed player %q: %v", name, err)
	}
	return pl
}

// buttonLabels flattens every action label across the attachments.
func buttonLabels(att []*model.SlackAttachment) []string {
	var out []string
	for _, a := range att {
		for _, act := range a.Actions {
			out = append(out, act.Name)
		}
	}
	return out
}

func hasLabel(att []*model.SlackAttachment, substr string) bool {
	for _, l := range buttonLabels(att) {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

func TestOwns(t *testing.T) {
	s, _, _ := newTestScreen(t)
	owned := []string{
		mm.ActMenuPlayers,
		mm.ActMgmtRename, mm.ActMgmtDelete, mm.ActMgmtPage, mm.ActMgmtNoop, mm.ActMgmtBack,
		mm.ActMgmtDeleteConfirm, mm.ActMgmtDeleteCancel,
	}
	for _, a := range owned {
		if !s.Owns(a) {
			t.Errorf("should own %q", a)
		}
	}
	for _, a := range []string{mm.ActMenuStats, mm.ActScorePlayer, mm.ActStatsPlayer, "", "garbage"} {
		if s.Owns(a) {
			t.Errorf("should not own %q", a)
		}
	}
}

func TestListEmpty(t *testing.T) {
	s, _, _ := newTestScreen(t)
	msg, att := s.listScreen(nil, 0)
	if msg != emptyList {
		t.Errorf("empty body = %q, want %q", msg, emptyList)
	}
	if !hasLabel(att, "Back") {
		t.Errorf("empty list must offer a Back button; got %v", buttonLabels(att))
	}
}

func TestListPagination(t *testing.T) {
	_, _, database := newTestScreen(t)
	s, _, _ := newTestScreenWith(t, database)
	// 10 active players => 2 pages of 8 + 2.
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	for _, n := range names {
		seedPlayer(t, database, n)
	}
	all, err := db.GetAllActivePlayers(context.Background(), database)
	if err != nil {
		t.Fatalf("get players: %v", err)
	}

	_, att0 := s.listScreen(all, 0)
	if !hasLabel(att0, "1/2") {
		t.Errorf("page 0 should show page indicator 1/2; got %v", buttonLabels(att0))
	}
	if !hasLabel(att0, "Next") {
		t.Errorf("page 0 should show Next; got %v", buttonLabels(att0))
	}
	if hasLabel(att0, "Prev") {
		t.Errorf("page 0 should not show Prev; got %v", buttonLabels(att0))
	}
	// 8 players * (rename+delete) = 16 player buttons on page 0.
	if got := countPrefixed(att0, "✏️ "); got != 8 {
		t.Errorf("page 0 rename buttons = %d, want 8", got)
	}

	_, att1 := s.listScreen(all, 1)
	if !hasLabel(att1, "Prev") {
		t.Errorf("page 1 should show Prev; got %v", buttonLabels(att1))
	}
	if hasLabel(att1, "Next") {
		t.Errorf("page 1 should not show Next; got %v", buttonLabels(att1))
	}
	if got := countPrefixed(att1, "✏️ "); got != 2 {
		t.Errorf("page 1 rename buttons = %d, want 2", got)
	}

	// Out-of-range page clamps to the last page rather than panicking.
	_, attHi := s.listScreen(all, 99)
	if got := countPrefixed(attHi, "✏️ "); got != 2 {
		t.Errorf("clamped page rename buttons = %d, want 2", got)
	}
}

func countPrefixed(att []*model.SlackAttachment, prefix string) int {
	n := 0
	for _, l := range buttonLabels(att) {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// newTestScreenWith builds a Screen over an existing DB so a test can seed once
// and share the handle.
func newTestScreenWith(t *testing.T, database *sql.DB) (*Screen, *fakePoster, *sql.DB) {
	t.Helper()
	signer, err := mm.NewSigner(testKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	p := &fakePoster{}
	b := nav.NewBuilder(signer, "http://bot.example:8068", nil)
	return New(database, p, b, nil), p, database
}

func TestRenameSuccess(t *testing.T) {
	s, p, database := newTestScreen(t)
	pl := seedPlayer(t, database, "Alice")

	ns := mm.NavState{Action: mm.ActMgmtRename, PlayerID: pl.ID, Page: 0, PostID: "post1"}
	req := &model.SubmitDialogRequest{Submission: map[string]any{"name": "Alicia"}}
	resp, err := s.Dialog(context.Background(), req, ns)
	if err != nil {
		t.Fatalf("Dialog: %v", err)
	}
	if len(resp.Errors) != 0 || resp.Error != "" {
		t.Fatalf("unexpected dialog error: %+v", resp)
	}
	if p.updates != 1 || p.updatedPost != "post1" {
		t.Errorf("expected one in-place update of post1; got updates=%d post=%q", p.updates, p.updatedPost)
	}

	all, _ := db.GetAllActivePlayers(context.Background(), database)
	if nameOf(pl.ID, all) != "Alicia" {
		t.Errorf("rename did not persist; got %q", nameOf(pl.ID, all))
	}
}

func TestRenameDuplicate(t *testing.T) {
	s, p, database := newTestScreen(t)
	a := seedPlayer(t, database, "Alice")
	seedPlayer(t, database, "Bob")

	ns := mm.NavState{Action: mm.ActMgmtRename, PlayerID: a.ID, PostID: "post1"}
	req := &model.SubmitDialogRequest{Submission: map[string]any{"name": "Bob"}}
	resp, err := s.Dialog(context.Background(), req, ns)
	if err != nil {
		t.Fatalf("Dialog: %v", err)
	}
	if resp.Errors["name"] == "" {
		t.Errorf("expected a name uniqueness error; got %+v", resp)
	}
	if p.updates != 0 {
		t.Errorf("duplicate rename must not update any post; updates=%d", p.updates)
	}
}

func TestRenameInvalid(t *testing.T) {
	s, _, database := newTestScreen(t)
	a := seedPlayer(t, database, "Alice")
	ns := mm.NavState{Action: mm.ActMgmtRename, PlayerID: a.ID, PostID: "post1"}
	for _, bad := range []string{"", "  ", "with`backtick", strings.Repeat("x", 51)} {
		req := &model.SubmitDialogRequest{Submission: map[string]any{"name": bad}}
		resp, err := s.Dialog(context.Background(), req, ns)
		if err != nil {
			t.Fatalf("Dialog(%q): %v", bad, err)
		}
		if resp.Errors["name"] == "" {
			t.Errorf("name %q should be rejected", bad)
		}
	}
}

func TestOpenRenameOpensDialogFirst(t *testing.T) {
	s, p, database := newTestScreen(t)
	a := seedPlayer(t, database, "Alice")
	req := &model.PostActionIntegrationRequest{ChannelId: testChannel, PostId: "post1", TriggerId: "trig1"}
	ns := mm.NavState{Action: mm.ActMgmtRename, PlayerID: a.ID, Page: 0}
	resp, err := s.Action(context.Background(), req, ns)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if p.dialogs != 1 || p.openedTrig != "trig1" {
		t.Errorf("rename should open dialog with the trigger id; dialogs=%d trig=%q", p.dialogs, p.openedTrig)
	}
	if resp.Update != nil {
		t.Errorf("opening the dialog should leave the post unchanged")
	}
}

func TestDeleteFlow(t *testing.T) {
	s, _, database := newTestScreen(t)
	a := seedPlayer(t, database, "Alice")
	req := &model.PostActionIntegrationRequest{ChannelId: testChannel, PostId: "post1"}

	// Prompt carries the player's name.
	resp, err := s.Action(context.Background(), req, mm.NavState{Action: mm.ActMgmtDelete, PlayerID: a.ID})
	if err != nil {
		t.Fatalf("delete prompt: %v", err)
	}
	if resp.Update == nil || !strings.Contains(resp.Update.Message, "Alice") {
		t.Errorf("delete prompt should mention the player; got %+v", resp.Update)
	}

	// Confirm hard-deletes.
	if _, err := s.Action(context.Background(), req, mm.NavState{Action: mm.ActMgmtDeleteConfirm, PlayerID: a.ID}); err != nil {
		t.Fatalf("delete confirm: %v", err)
	}
	all, _ := db.GetAllActivePlayers(context.Background(), database)
	if nameOf(a.ID, all) != "" {
		t.Errorf("player should be deleted; still present")
	}
}

func TestDeleteGuardActiveGame(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")
	g, err := db.CreateGame(ctx, database, testChannel)
	if err != nil {
		t.Fatalf("create game: %v", err)
	}
	for _, pid := range []int64{a.ID, b.ID} {
		if err := db.AddPlayerToGame(ctx, database, g.ID, pid); err != nil {
			t.Fatalf("add player: %v", err)
		}
	}

	req := &model.PostActionIntegrationRequest{ChannelId: testChannel, PostId: "post1"}
	resp, err := s.Action(ctx, req, mm.NavState{Action: mm.ActMgmtDeleteConfirm, PlayerID: a.ID})
	if err != nil {
		t.Fatalf("delete confirm: %v", err)
	}
	if !strings.Contains(resp.EphemeralText, "active game") {
		t.Errorf("expected active-game refusal ephemeral; got %+v", resp)
	}
	all, _ := db.GetAllActivePlayers(ctx, database)
	if nameOf(a.ID, all) == "" {
		t.Errorf("player must NOT be deleted while in an active game")
	}
}
