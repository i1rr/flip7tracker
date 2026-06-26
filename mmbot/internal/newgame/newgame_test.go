package newgame

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

// fakePoster records the surface the new-game flow exercises.
type fakePoster struct {
	openedDialog model.Dialog
	openedURL    string
	openedTrig   string
	dialogs      int

	createdMsg string
	createdAtt []*model.SlackAttachment
	createID   string
	creates    int

	updatedPost string
	updatedMsg  string
	updatedAtt  []*model.SlackAttachment
	updates     int
}

func (f *fakePoster) PostMessage(context.Context, string, string) (string, error) { return "", nil }
func (f *fakePoster) PostInThread(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakePoster) PostAttachment(_ context.Context, _ string, message string, att []*model.SlackAttachment) (string, error) {
	f.createdMsg = message
	f.createdAtt = att
	f.creates++
	return f.createID, nil
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
func (f *fakePoster) OpenDialog(_ context.Context, triggerID, url string, dialog model.Dialog) error {
	f.openedTrig = triggerID
	f.openedURL = url
	f.openedDialog = dialog
	f.dialogs++
	return nil
}

func newTestScreen(t *testing.T, opts ...Option) (*Screen, *fakePoster, *sql.DB) {
	t.Helper()
	// modernc's :memory: gives each pooled connection its own DB, so a temp file
	// is used instead (mirrors internal/db's test discipline).
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
	p := &fakePoster{createID: "rootpost1"}
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

func actReq() *model.PostActionIntegrationRequest {
	return &model.PostActionIntegrationRequest{ChannelId: testChannel, PostId: "setuppost1", TriggerId: "trig1"}
}

func TestOwns(t *testing.T) {
	s, _, _ := newTestScreen(t)
	owned := []string{
		mm.ActMenuNewGame, mm.ActGameNew,
		mm.ActSetupAddNew, mm.ActSetupKnown, mm.ActSetupBack, mm.ActSetupStart, mm.ActSetupDisabled,
		mm.ActPlayerRemove,
		mm.ActKnownAdd, mm.ActKnownPage, mm.ActKnownBack, mm.ActKnownStart, mm.ActKnownNoop,
	}
	for _, a := range owned {
		if !s.Owns(a) {
			t.Errorf("should own %q", a)
		}
	}
	for _, a := range []string{mm.ActMenuStats, mm.ActScorePlayer, mm.ActMgmtRename, "", "garbage"} {
		if s.Owns(a) {
			t.Errorf("should not own %q", a)
		}
	}
}

func TestEntryPreloadsLastPlayers(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")
	c := seedPlayer(t, database, "Carol")
	// Remember Alice + Carol; Bob is excluded from last-players.
	if err := db.SaveLastPlayers(ctx, database, testChannel, []int64{a.ID, c.ID}); err != nil {
		t.Fatalf("save last: %v", err)
	}
	_ = b

	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActMenuNewGame})
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if resp.Update == nil {
		t.Fatal("expected an Update")
	}
	if !strings.Contains(resp.Update.Message, "Alice") || !strings.Contains(resp.Update.Message, "Carol") {
		t.Errorf("setup text missing preloaded players: %q", resp.Update.Message)
	}
	if strings.Contains(resp.Update.Message, "Bob") {
		t.Errorf("Bob should not be preloaded: %q", resp.Update.Message)
	}
}

func TestEntryDropsInactiveLastPlayer(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	gone := seedPlayer(t, database, "Ghost")
	if err := db.SaveLastPlayers(ctx, database, testChannel, []int64{a.ID, gone.ID}); err != nil {
		t.Fatalf("save last: %v", err)
	}
	if err := db.HardDeletePlayer(ctx, database, gone.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActMenuNewGame})
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if strings.Contains(resp.Update.Message, "Ghost") {
		t.Errorf("inactive player should be dropped: %q", resp.Update.Message)
	}
}

func TestAddPlayerOpensDialog(t *testing.T) {
	s, p, _ := newTestScreen(t)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActSetupAddNew, Players: []int64{7}})
	if err != nil {
		t.Fatalf("add_new: %v", err)
	}
	if p.dialogs != 1 {
		t.Fatalf("expected dialog opened once, got %d", p.dialogs)
	}
	if p.openedTrig != "trig1" {
		t.Errorf("trigger id = %q", p.openedTrig)
	}
	if !strings.HasSuffix(p.openedURL, "/dialog") {
		t.Errorf("dialog url = %q", p.openedURL)
	}
	if p.openedDialog.State == "" {
		t.Error("dialog state should be signed and non-empty")
	}
	if resp.Update != nil || resp.EphemeralText != "" {
		t.Errorf("add_new response should be an empty no-op, got %+v", resp)
	}
	// The signed state must round-trip with the roster + originating post id.
	signer, _ := mm.NewSigner(testKey)
	ns, err := signer.VerifyState(p.openedDialog.State, 0)
	if err != nil {
		t.Fatalf("verify dialog state: %v", err)
	}
	if ns.Action != mm.ActSetupAddNew || ns.PostID != "setuppost1" || len(ns.Players) != 1 || ns.Players[0] != 7 {
		t.Errorf("dialog state mismatch: %+v", ns)
	}
}

func TestDialogValidationRejectsBadNames(t *testing.T) {
	s, _, _ := newTestScreen(t)
	for _, bad := range []string{"", "  ", strings.Repeat("x", 51), "back`tick", "line\nbreak"} {
		req := &model.SubmitDialogRequest{Submission: map[string]any{"name": bad}}
		resp, err := s.Dialog(context.Background(), req, mm.NavState{Action: mm.ActSetupAddNew})
		if err != nil {
			t.Fatalf("dialog(%q): %v", bad, err)
		}
		if resp.Errors["name"] == "" {
			t.Errorf("expected validation error for %q", bad)
		}
	}
}

func TestDialogAddsPlayerAndReRenders(t *testing.T) {
	s, p, database := newTestScreen(t)
	ctx := context.Background()
	existing := seedPlayer(t, database, "Alice")
	req := &model.SubmitDialogRequest{
		ChannelId:  testChannel,
		Submission: map[string]any{"name": "Bob"},
	}
	resp, err := s.Dialog(ctx, req, mm.NavState{Action: mm.ActSetupAddNew, Players: []int64{existing.ID}, PostID: "setuppost1"})
	if err != nil {
		t.Fatalf("dialog: %v", err)
	}
	if len(resp.Errors) != 0 || resp.Error != "" {
		t.Fatalf("unexpected dialog error: %+v", resp)
	}
	if p.updates != 1 || p.updatedPost != "setuppost1" {
		t.Fatalf("expected in-place re-render of setuppost1, got updates=%d post=%q", p.updates, p.updatedPost)
	}
	if !strings.Contains(p.updatedMsg, "Alice") || !strings.Contains(p.updatedMsg, "Bob") {
		t.Errorf("re-render missing players: %q", p.updatedMsg)
	}
}

func TestDialogDuplicateName(t *testing.T) {
	s, p, database := newTestScreen(t)
	ctx := context.Background()
	alice := seedPlayer(t, database, "Alice")
	req := &model.SubmitDialogRequest{Submission: map[string]any{"name": "Alice"}}
	resp, err := s.Dialog(ctx, req, mm.NavState{Action: mm.ActSetupAddNew, Players: []int64{alice.ID}, PostID: "setuppost1"})
	if err != nil {
		t.Fatalf("dialog: %v", err)
	}
	if !strings.Contains(resp.Errors["name"], "already added") {
		t.Errorf("expected already-added error, got %+v", resp)
	}
	if p.updates != 0 {
		t.Errorf("duplicate add should not re-render, got %d updates", p.updates)
	}
}

func TestRemovePlayerReRenders(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")
	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActPlayerRemove, PlayerID: a.ID, Players: []int64{a.ID, b.ID}})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if strings.Contains(resp.Update.Message, "Alice") {
		t.Errorf("Alice should be removed: %q", resp.Update.Message)
	}
	if !strings.Contains(resp.Update.Message, "Bob") {
		t.Errorf("Bob should remain: %q", resp.Update.Message)
	}
}

func TestKnownPickerExcludesAdded(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	seedPlayer(t, database, "Bob")
	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActSetupKnown, Players: []int64{a.ID}})
	if err != nil {
		t.Fatalf("known: %v", err)
	}
	if resp.Update == nil {
		t.Fatalf("expected picker update, got %+v", resp)
	}
	labels := buttonLabels(resp.Update)
	if hasLabel(labels, "Alice") {
		t.Errorf("added player should be excluded from picker: %v", labels)
	}
	if !hasLabel(labels, "Bob") {
		t.Errorf("available player Bob missing from picker: %v", labels)
	}
}

func TestKnownPickerEmptyEphemeral(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActSetupKnown, Players: []int64{a.ID}})
	if err != nil {
		t.Fatalf("known: %v", err)
	}
	if resp.EphemeralText != noOtherPlayers {
		t.Errorf("expected no-other-players ephemeral, got %+v", resp)
	}
}

func TestKnownPagination(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	// 10 available players → 2 pages of 8.
	for _, n := range []string{"p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9"} {
		seedPlayer(t, database, n)
	}
	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActSetupKnown})
	if err != nil {
		t.Fatalf("known: %v", err)
	}
	if !strings.Contains(resp.Update.Message, "page 1/2") {
		t.Errorf("expected page 1/2, got %q", resp.Update.Message)
	}
	labels := buttonLabels(resp.Update)
	if !hasLabel(labels, "Next →") {
		t.Errorf("page 1 should have Next, got %v", labels)
	}
	if hasLabel(labels, "← Prev") {
		t.Errorf("page 1 should not have Prev, got %v", labels)
	}
	// Page 2.
	resp2, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActKnownPage, Page: 1})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if !strings.Contains(resp2.Update.Message, "page 2/2") {
		t.Errorf("expected page 2/2, got %q", resp2.Update.Message)
	}
}

func TestKnownAddReturnsToSetupWhenExhausted(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")
	// Roster already has Alice; adding Bob exhausts the known list → setup screen.
	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActKnownAdd, PlayerID: b.ID, Players: []int64{a.ID}})
	if err != nil {
		t.Fatalf("known add: %v", err)
	}
	if !strings.HasPrefix(resp.Update.Message, "New game setup:") {
		t.Errorf("expected setup screen after exhausting known list, got %q", resp.Update.Message)
	}
	if !strings.Contains(resp.Update.Message, "Alice") || !strings.Contains(resp.Update.Message, "Bob") {
		t.Errorf("setup should list both players, got %q", resp.Update.Message)
	}
}

func TestStartGameNeedsTwoPlayers(t *testing.T) {
	s, p, _ := newTestScreen(t)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActSetupStart, Players: []int64{1}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if resp.EphemeralText != needTwoPlayers {
		t.Errorf("expected need-two ephemeral, got %+v", resp)
	}
	if p.creates != 0 {
		t.Errorf("no game should be created, got %d posts", p.creates)
	}
}

func TestStartGameBlockedByUnfinished(t *testing.T) {
	s, p, database := newTestScreen(t)
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")
	if _, err := db.CreateGame(ctx, database, testChannel); err != nil {
		t.Fatalf("seed game: %v", err)
	}
	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActSetupStart, Players: []int64{a.ID, b.ID}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if resp.EphemeralText != unfinishedGame {
		t.Errorf("expected unfinished-game ephemeral, got %+v", resp)
	}
	if p.creates != 0 {
		t.Errorf("no game should be created, got %d posts", p.creates)
	}
}

func TestStartGameCreatesRootPostAndRetiresSetup(t *testing.T) {
	var renderedGameID int64
	renderer := func(gameID int64, scores map[int64]int64, players []db.Player) (string, []*model.SlackAttachment) {
		renderedGameID = gameID
		return "ROOT", []*model.SlackAttachment{{}}
	}
	s, p, database := newTestScreen(t, WithStartRenderer(renderer))
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")

	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActSetupStart, Players: []int64{a.ID, b.ID}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Root post created from the renderer output.
	if p.creates != 1 || p.createdMsg != "ROOT" {
		t.Fatalf("expected one root post 'ROOT', got creates=%d msg=%q", p.creates, p.createdMsg)
	}
	if renderedGameID == 0 {
		t.Error("renderer did not receive a game id")
	}
	// Originating setup post retired into a non-interactive stub.
	if resp.Update == nil || resp.Update.Message != startedStub {
		t.Fatalf("expected setup post retired to stub, got %+v", resp)
	}
	if atts := resp.Update.Attachments(); len(atts) != 0 {
		t.Errorf("retired stub must carry no buttons, got %d attachments", len(atts))
	}
	// Game persisted: post id stored, players linked, last-players saved.
	games, err := db.GetUnfinishedGames(ctx, database, testChannel)
	if err != nil || len(games) != 1 {
		t.Fatalf("expected 1 unfinished game, got %d (err=%v)", len(games), err)
	}
	if !games[0].PostID.Valid || games[0].PostID.String != "rootpost1" {
		t.Errorf("game post id not stored: %+v", games[0].PostID)
	}
	gp, _ := db.GetGamePlayers(ctx, database, games[0].ID)
	if len(gp) != 2 {
		t.Errorf("expected 2 game players, got %d", len(gp))
	}
	last, _ := db.GetLastPlayers(ctx, database, testChannel)
	if len(last) != 2 {
		t.Errorf("expected last-players saved, got %d", len(last))
	}
}

func TestKnownStartSharesStartPath(t *testing.T) {
	s, p, database := newTestScreen(t, WithStartRenderer(func(int64, map[int64]int64, []db.Player) (string, []*model.SlackAttachment) {
		return "ROOT", nil
	}))
	ctx := context.Background()
	a := seedPlayer(t, database, "Alice")
	b := seedPlayer(t, database, "Bob")
	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActKnownStart, Players: []int64{a.ID, b.ID}})
	if err != nil {
		t.Fatalf("known start: %v", err)
	}
	if p.creates != 1 || resp.Update.Message != startedStub {
		t.Errorf("known:start should share the start path, got creates=%d resp=%+v", p.creates, resp)
	}
}

func TestSetupBackUsesMainMenuRenderer(t *testing.T) {
	called := false
	s, _, _ := newTestScreen(t, WithMainMenuRenderer(func() *model.PostActionIntegrationResponse {
		called = true
		return nav.UpdateResponse("MENU", nil)
	}))
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActSetupBack})
	if err != nil {
		t.Fatalf("back: %v", err)
	}
	if !called || resp.Update == nil || resp.Update.Message != "MENU" {
		t.Errorf("setup:back should render the main menu, got %+v", resp)
	}
}

func TestSetupBackWithoutRendererExpires(t *testing.T) {
	s, _, _ := newTestScreen(t)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActSetupBack})
	if err != nil {
		t.Fatalf("back: %v", err)
	}
	if resp.EphemeralText != nav.ExpiredMessage {
		t.Errorf("expected expired fallback, got %+v", resp)
	}
}

func TestKnownNoopInert(t *testing.T) {
	s, _, _ := newTestScreen(t)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActKnownNoop, Players: []int64{1, 2}})
	if err != nil {
		t.Fatalf("noop: %v", err)
	}
	if resp.Update != nil || resp.EphemeralText != "" {
		t.Errorf("noop should be inert, got %+v", resp)
	}
}

func TestDisabledStartEphemeral(t *testing.T) {
	s, _, _ := newTestScreen(t)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActSetupDisabled, Players: []int64{1}})
	if err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if resp.EphemeralText != needTwoPlayers {
		t.Errorf("expected need-two ephemeral, got %+v", resp)
	}
}

// buttonLabels extracts the button labels from an Update post's attachments.
func buttonLabels(post *model.Post) []string {
	var labels []string
	for _, a := range post.Attachments() {
		for _, act := range a.Actions {
			labels = append(labels, act.Name)
		}
	}
	return labels
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
