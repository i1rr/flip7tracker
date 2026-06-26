package game

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
	"github.com/rivan/flip7bot/mmbot/internal/rating"
)

const testKey = "0123456789abcdef0123456789abcdef" // 32 bytes
const testChannel = "chan1"

// fakePoster records the surface the game flow exercises.
type fakePoster struct {
	openedDialog model.Dialog
	dialogs      int

	createdMsg string
	createdAtt []*model.SlackAttachment
	createID   string
	creates    int

	updatedPost string
	updatedMsg  string
	updatedAtt  []*model.SlackAttachment
	updates     int

	threadReplies []string
	threadRoots   []string

	deleted []string
}

func (f *fakePoster) PostMessage(context.Context, string, string) (string, error) { return "", nil }
func (f *fakePoster) PostInThread(_ context.Context, _ string, rootID, message string) (string, error) {
	f.threadRoots = append(f.threadRoots, rootID)
	f.threadReplies = append(f.threadReplies, message)
	return "reply1", nil
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
func (f *fakePoster) DeletePost(_ context.Context, postID string) error {
	f.deleted = append(f.deleted, postID)
	return nil
}
func (f *fakePoster) OpenDialog(_ context.Context, _, _ string, dialog model.Dialog) error {
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
	p := &fakePoster{createID: "newroot1"}
	b := nav.NewBuilder(signer, "http://bot.example:8068", nil)
	return New(database, p, b, nil, opts...), p, database
}

// seedGame creates a game in testChannel with the named players and links them,
// returning the game id and the created players.
func seedGame(t *testing.T, database *sql.DB, names ...string) (int64, []db.Player) {
	t.Helper()
	ctx := context.Background()
	g, err := db.CreateGame(ctx, database, testChannel)
	if err != nil {
		t.Fatalf("create game: %v", err)
	}
	var players []db.Player
	for _, n := range names {
		p, err := db.CreateOrGetPlayer(ctx, database, n)
		if err != nil {
			t.Fatalf("create player %q: %v", n, err)
		}
		if err := db.AddPlayerToGame(ctx, database, g.ID, p.ID); err != nil {
			t.Fatalf("add player: %v", err)
		}
		players = append(players, p)
	}
	if err := db.UpdateGamePostID(ctx, database, g.ID, "root1"); err != nil {
		t.Fatalf("set post id: %v", err)
	}
	return g.ID, players
}

func addScore(t *testing.T, database *sql.DB, gameID, playerID, pts int64) {
	t.Helper()
	if _, err := db.AddScoreEntry(context.Background(), database, gameID, playerID, pts); err != nil {
		t.Fatalf("add score: %v", err)
	}
}

func actReq() *model.PostActionIntegrationRequest {
	return &model.PostActionIntegrationRequest{ChannelId: testChannel, PostId: "root1", TriggerId: "trig1"}
}

// navOf decodes the signed nav-state from a button's action context.
func navOf(t *testing.T, a *model.PostAction) mm.NavState {
	t.Helper()
	signer, _ := mm.NewSigner(testKey)
	tok, _ := a.Integration.Context["nav"].(string)
	ns, err := signer.VerifyContext(tok)
	if err != nil {
		t.Fatalf("verify button context: %v", err)
	}
	return ns
}

func buttons(post *model.Post) []*model.PostAction {
	var out []*model.PostAction
	for _, a := range post.Attachments() {
		out = append(out, a.Actions...)
	}
	return out
}

func labels(post *model.Post) []string {
	var out []string
	for _, a := range buttons(post) {
		out = append(out, a.Name)
	}
	return out
}

func has(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestOwns(t *testing.T) {
	s, _, _ := newTestScreen(t)
	owned := []string{
		mm.ActMenuLoadGame, mm.ActGameLoad, mm.ActLoadBack,
		mm.ActScorePlayer,
		mm.ActGameEdit, mm.ActEditConfirm, mm.ActEditCancel,
		mm.ActGameEnd, mm.ActGameEndConfirm, mm.ActGameEndCancel,
	}
	for _, a := range owned {
		if !s.Owns(a) {
			t.Errorf("should own %q", a)
		}
	}
	for _, a := range []string{mm.ActMenuNewGame, mm.ActMenuStats, mm.ActGameHome, "", "x"} {
		if s.Owns(a) {
			t.Errorf("should not own %q", a)
		}
	}
}

func TestRenderRootButtons(t *testing.T) {
	s, _, database := newTestScreen(t)
	gid, players := seedGame(t, database, "Alice", "Bob")
	msg, att := s.RenderRoot(gid, map[int64]int64{}, players)
	if !strings.Contains(msg, "Game #") {
		t.Errorf("missing scoreboard header: %q", msg)
	}
	post := &model.Post{}
	model.ParseSlackAttachment(post, att)
	ls := labels(post)
	if !has(ls, "Alice") || !has(ls, "Bob") {
		t.Errorf("missing per-player buttons: %v", ls)
	}
	if !has(ls, "✏️ Edit Last") || !has(ls, "🚩 End Game") {
		t.Errorf("missing controls: %v", ls)
	}
}

func TestScoreDialogOpensWithSignedState(t *testing.T) {
	s, p, database := newTestScreen(t)
	gid, players := seedGame(t, database, "Alice", "Bob")
	resp, err := s.Action(context.Background(), actReq(),
		mm.NavState{Action: mm.ActScorePlayer, GameID: gid, PlayerID: players[0].ID})
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if p.dialogs != 1 {
		t.Fatalf("expected one dialog, got %d", p.dialogs)
	}
	if resp.Update != nil || resp.EphemeralText != "" {
		t.Errorf("score tap should be an empty no-op, got %+v", resp)
	}
	signer, _ := mm.NewSigner(testKey)
	ns, err := signer.VerifyState(p.openedDialog.State, 0)
	if err != nil {
		t.Fatalf("verify state: %v", err)
	}
	if ns.Action != mm.ActScorePlayer || ns.GameID != gid || ns.PlayerID != players[0].ID || ns.PostID != "root1" {
		t.Errorf("dialog state mismatch: %+v", ns)
	}
}

func TestScoreEntrySubmit(t *testing.T) {
	s, p, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	req := &model.SubmitDialogRequest{ChannelId: testChannel, Submission: map[string]any{"points": "42"}}
	ns := mm.NavState{Action: mm.ActScorePlayer, GameID: gid, PlayerID: players[0].ID, PostID: "root1"}
	resp, err := s.Dialog(ctx, req, ns)
	if err != nil {
		t.Fatalf("dialog: %v", err)
	}
	if len(resp.Errors) != 0 || resp.Error != "" {
		t.Fatalf("unexpected dialog error: %+v", resp)
	}
	scores, _ := db.GetGameScores(ctx, database, gid)
	if scores[players[0].ID] != 42 {
		t.Errorf("expected 42 recorded, got %d", scores[players[0].ID])
	}
	if p.updates != 1 || p.updatedPost != "root1" {
		t.Errorf("expected root re-render, got updates=%d post=%q", p.updates, p.updatedPost)
	}
	if len(p.threadReplies) != 1 || !strings.Contains(p.threadReplies[0], "Alice") || !strings.Contains(p.threadReplies[0], "42") {
		t.Errorf("expected confirmation reply, got %v", p.threadReplies)
	}
}

func TestScoreEntryInvalidPoints(t *testing.T) {
	s, _, database := newTestScreen(t)
	gid, players := seedGame(t, database, "Alice", "Bob")
	for _, bad := range []string{"", "abc", "-1", "1000", "1.5"} {
		req := &model.SubmitDialogRequest{ChannelId: testChannel, Submission: map[string]any{"points": bad}}
		ns := mm.NavState{Action: mm.ActScorePlayer, GameID: gid, PlayerID: players[0].ID, PostID: "root1"}
		resp, err := s.Dialog(context.Background(), req, ns)
		if err != nil {
			t.Fatalf("dialog(%q): %v", bad, err)
		}
		if resp.Errors["points"] == "" {
			t.Errorf("expected points error for %q", bad)
		}
	}
}

func TestScoreEntryGameOver(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	// Finish the game so it is no longer in the unfinished set.
	if _, err := db.FinishGameWithWinners(ctx, database, gid, []int64{players[0].ID}, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	req := &model.SubmitDialogRequest{ChannelId: testChannel, Submission: map[string]any{"points": "10"}}
	ns := mm.NavState{Action: mm.ActScorePlayer, GameID: gid, PlayerID: players[0].ID, PostID: "root1"}
	resp, err := s.Dialog(ctx, req, ns)
	if err != nil {
		t.Fatalf("dialog: %v", err)
	}
	if resp.Error != gameOverMsg {
		t.Errorf("expected game-over error, got %+v", resp)
	}
}

func TestScoreEntryPlayerGone(t *testing.T) {
	s, _, database := newTestScreen(t)
	gid, _ := seedGame(t, database, "Alice", "Bob")
	req := &model.SubmitDialogRequest{ChannelId: testChannel, Submission: map[string]any{"points": "10"}}
	ns := mm.NavState{Action: mm.ActScorePlayer, GameID: gid, PlayerID: 99999, PostID: "root1"}
	resp, err := s.Dialog(context.Background(), req, ns)
	if err != nil {
		t.Fatalf("dialog: %v", err)
	}
	if resp.Error != playerGoneMsg {
		t.Errorf("expected player-gone error, got %+v", resp)
	}
}

func TestEditLastNone(t *testing.T) {
	s, _, database := newTestScreen(t)
	gid, _ := seedGame(t, database, "Alice", "Bob")
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActGameEdit, GameID: gid})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if resp.EphemeralText != noScoresMsg {
		t.Errorf("expected no-scores ephemeral, got %+v", resp)
	}
}

func TestEditLastConfirmCarriesEntryID(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	addScore(t, database, gid, players[0].ID, 10)
	entry, _ := db.GetLastScoreEntry(ctx, database, gid)

	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEdit, GameID: gid})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(resp.Update.Message, "Alice") || !strings.Contains(resp.Update.Message, "10 pts") {
		t.Errorf("confirm copy wrong: %q", resp.Update.Message)
	}
	var del *model.PostAction
	for _, b := range buttons(resp.Update) {
		if b.Name == "🗑 Delete Entry" {
			del = b
		}
	}
	if del == nil {
		t.Fatal("missing delete button")
	}
	ns := navOf(t, del)
	if ns.Action != mm.ActEditConfirm || ns.EntryID != entry.ID || ns.GameID != gid {
		t.Errorf("delete button nav wrong: %+v (want entry %d)", ns, entry.ID)
	}
}

func TestEditConfirmDeletesEntry(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	addScore(t, database, gid, players[0].ID, 10)
	addScore(t, database, gid, players[1].ID, 20)
	entry, _ := db.GetLastScoreEntry(ctx, database, gid)

	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActEditConfirm, GameID: gid, EntryID: entry.ID})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if resp.Update == nil {
		t.Fatal("expected re-render")
	}
	scores, _ := db.GetGameScores(ctx, database, gid)
	if scores[players[1].ID] != 0 {
		t.Errorf("entry not deleted, Bob still %d", scores[players[1].ID])
	}
	// Idempotent replay: deleting the same id again is a harmless no-op.
	if _, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActEditConfirm, GameID: gid, EntryID: entry.ID}); err != nil {
		t.Fatalf("replay confirm: %v", err)
	}
}

func TestEndPromptCopy(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")

	// Below threshold → discard warning.
	addScore(t, database, gid, players[0].ID, 50)
	resp, _ := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEnd, GameID: gid})
	if !strings.Contains(resp.Update.Message, "discarded") {
		t.Errorf("expected discard warning, got %q", resp.Update.Message)
	}

	// Single winner ≥ threshold.
	addScore(t, database, gid, players[0].ID, 200)
	resp, _ = s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEnd, GameID: gid})
	if !strings.Contains(resp.Update.Message, "Alice wins with 250 pts") {
		t.Errorf("expected single-winner copy, got %q", resp.Update.Message)
	}

	// Joint winners.
	addScore(t, database, gid, players[1].ID, 250)
	resp, _ = s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEnd, GameID: gid})
	if !strings.Contains(resp.Update.Message, "Joint winners at 250 pts") {
		t.Errorf("expected joint-winner copy, got %q", resp.Update.Message)
	}
}

func TestEndConfirmDiscard(t *testing.T) {
	s, p, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	addScore(t, database, gid, players[0].ID, 50)

	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEndConfirm, GameID: gid})
	if err != nil {
		t.Fatalf("end confirm: %v", err)
	}
	if resp.Update.Message != discardStub || len(resp.Update.Attachments()) != 0 {
		t.Errorf("expected control-less discard stub, got %+v", resp.Update)
	}
	if len(p.threadReplies) != 1 {
		t.Errorf("expected one discard reply, got %v", p.threadReplies)
	}
	if games, _ := db.GetUnfinishedGames(ctx, database, testChannel); len(games) != 0 {
		t.Errorf("game should be discarded, %d remain", len(games))
	}
	// Replay: rows are gone — same stub, no second reply.
	resp2, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEndConfirm, GameID: gid})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if resp2.Update.Message != discardStub {
		t.Errorf("replay should render the same stub, got %q", resp2.Update.Message)
	}
	if len(p.threadReplies) != 1 {
		t.Errorf("replay must not post a second reply, got %v", p.threadReplies)
	}
}

func TestEndConfirmFinishWithElo(t *testing.T) {
	s, p, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	addScore(t, database, gid, players[0].ID, 220) // Alice wins
	addScore(t, database, gid, players[1].ID, 100)

	// Expected Elo from the same inputs the handler uses.
	want := rating.ComputeUpdates([]rating.EloEntry{
		{PlayerID: players[0].ID, Score: 220, Rating: players[0].Rating},
		{PlayerID: players[1].ID, Score: 100, Rating: players[1].Rating},
	})

	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEndConfirm, GameID: gid})
	if err != nil {
		t.Fatalf("end confirm: %v", err)
	}
	if !strings.Contains(resp.Update.Message, "WINNER: Alice with 220 pts") {
		t.Errorf("expected win screen, got %q", resp.Update.Message)
	}
	if !has(labels(resp.Update), "🏠 Main Menu") {
		t.Errorf("expected post-game nav buttons, got %v", labels(resp.Update))
	}
	if len(p.threadReplies) != 1 || !strings.Contains(p.threadReplies[0], "WINNER: Alice") {
		t.Errorf("expected winner announcement reply, got %v", p.threadReplies)
	}

	// Ratings persisted to the just-computed values.
	gp, _ := db.GetGamePlayers(ctx, database, gid)
	for _, pl := range gp {
		for _, d := range want {
			if d.PlayerID == pl.ID && pl.Rating != d.RatingAfter {
				t.Errorf("player %d rating = %v, want %v", pl.ID, pl.Rating, d.RatingAfter)
			}
		}
	}

	// Replay: already finalized → win screen from persisted values, no recompute,
	// no second reply.
	resp2, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEndConfirm, GameID: gid})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !strings.Contains(resp2.Update.Message, "WINNER: Alice with 220 pts") {
		t.Errorf("replay win screen wrong: %q", resp2.Update.Message)
	}
	if len(p.threadReplies) != 1 {
		t.Errorf("replay must not duplicate the announcement, got %v", p.threadReplies)
	}
	gp2, _ := db.GetGamePlayers(ctx, database, gid)
	for i := range gp2 {
		if gp2[i].Rating != gp[i].Rating {
			t.Errorf("replay changed rating for %d: %v -> %v", gp2[i].ID, gp[i].Rating, gp2[i].Rating)
		}
	}
}

func TestEndConfirmJointWinners(t *testing.T) {
	s, _, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	addScore(t, database, gid, players[0].ID, 210)
	addScore(t, database, gid, players[1].ID, 210)

	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameEndConfirm, GameID: gid})
	if err != nil {
		t.Fatalf("end confirm: %v", err)
	}
	if !strings.Contains(resp.Update.Message, "JOINT WINNERS") {
		t.Errorf("expected joint winners, got %q", resp.Update.Message)
	}
	res, _ := db.GetFinishedGameResult(ctx, database, gid)
	if len(res.WinnerIDs) != 2 {
		t.Errorf("expected 2 winners persisted, got %d", len(res.WinnerIDs))
	}
}

func TestEndCancelReRendersBoard(t *testing.T) {
	s, _, database := newTestScreen(t)
	gid, players := seedGame(t, database, "Alice", "Bob")
	addScore(t, database, gid, players[0].ID, 30)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActGameEndCancel, GameID: gid})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !has(labels(resp.Update), "🚩 End Game") {
		t.Errorf("cancel should restore the active board, got %v", labels(resp.Update))
	}
}
