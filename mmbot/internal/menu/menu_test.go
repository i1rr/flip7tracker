package menu

import (
	"context"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
)

const testKey = "0123456789abcdef0123456789abcdef" // 32 bytes

// fakePoster records the posts it is asked to create. Only the methods menu
// exercises do anything; the rest satisfy the mm.Poster interface.
type fakePoster struct {
	lastChannel string
	lastMessage string
	lastAtt     []*model.SlackAttachment
	postID      string
	posts       int
}

func (f *fakePoster) PostMessage(_ context.Context, channelID, message string) (string, error) {
	return "", nil
}
func (f *fakePoster) PostInThread(_ context.Context, channelID, rootID, message string) (string, error) {
	return "", nil
}
func (f *fakePoster) PostAttachment(_ context.Context, channelID, message string, att []*model.SlackAttachment) (string, error) {
	f.lastChannel = channelID
	f.lastMessage = message
	f.lastAtt = att
	f.posts++
	return f.postID, nil
}
func (f *fakePoster) PostAttachmentInThread(_ context.Context, channelID, rootID, message string, att []*model.SlackAttachment) (string, error) {
	return "", nil
}
func (f *fakePoster) UpdatePost(_ context.Context, postID, message string) error { return nil }
func (f *fakePoster) UpdateAttachment(_ context.Context, postID, message string, att []*model.SlackAttachment) error {
	return nil
}
func (f *fakePoster) DeletePost(_ context.Context, postID string) error { return nil }
func (f *fakePoster) OpenDialog(_ context.Context, triggerID, url string, dialog model.Dialog) error {
	return nil
}

func newTestMenu(t *testing.T, opts ...Option) (*Menu, *fakePoster) {
	t.Helper()
	signer, err := mm.NewSigner(testKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	p := &fakePoster{postID: "menupost1"}
	b := nav.NewBuilder(signer, "http://bot.example:8068", nil)
	return New(p, b, nil, opts...), p
}

func TestSlashEmptyPostsMainMenu(t *testing.T) {
	m, p := newTestMenu(t)
	resp, err := m.Slash(context.Background(), "chan1", "")
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}
	if p.posts != 1 {
		t.Fatalf("expected 1 post, got %d", p.posts)
	}
	if p.lastChannel != "chan1" {
		t.Errorf("channel = %q", p.lastChannel)
	}
	if p.lastMessage != mainMenuText {
		t.Errorf("message = %q", p.lastMessage)
	}
	if len(p.lastAtt) != 1 || len(p.lastAtt[0].Actions) != 4 {
		t.Fatalf("expected 1 attachment with 4 buttons, got %+v", p.lastAtt)
	}
	if resp == nil || resp.Text != "" {
		t.Errorf("empty-arg response should be silent, got %+v", resp)
	}
}

func TestSlashHelp(t *testing.T) {
	m, p := newTestMenu(t)
	resp, err := m.Slash(context.Background(), "chan1", "help")
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}
	if p.posts != 0 {
		t.Errorf("help should not post, got %d posts", p.posts)
	}
	if resp.ResponseType != model.CommandResponseTypeEphemeral || resp.Text != helpText {
		t.Errorf("help response = %+v", resp)
	}
}

func TestSlashScoreboardNoReposter(t *testing.T) {
	m, _ := newTestMenu(t)
	resp, err := m.Slash(context.Background(), "chan1", "scoreboard")
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}
	if resp.Text != noActiveGame {
		t.Errorf("got %q, want %q", resp.Text, noActiveGame)
	}
}

func TestSlashScoreboardWithReposter(t *testing.T) {
	var gotChannel string
	hook := func(_ context.Context, channelID string) (*model.CommandResponse, error) {
		gotChannel = channelID
		return &model.CommandResponse{Text: "reposted"}, nil
	}
	m, _ := newTestMenu(t, WithScoreboardReposter(hook))
	resp, err := m.Slash(context.Background(), "chanX", "scoreboard")
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}
	if gotChannel != "chanX" {
		t.Errorf("reposter got channel %q", gotChannel)
	}
	if resp.Text != "reposted" {
		t.Errorf("got %q", resp.Text)
	}
}

func TestSlashUnknownArgShowsHelp(t *testing.T) {
	m, _ := newTestMenu(t)
	resp, err := m.Slash(context.Background(), "chan1", "frobnicate")
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}
	if resp.Text != helpText {
		t.Errorf("unknown arg should show help, got %q", resp.Text)
	}
}

func TestOwns(t *testing.T) {
	m, _ := newTestMenu(t)
	owned := []string{
		mm.ActMenuNewGame, mm.ActMenuLoadGame, mm.ActMenuStats, mm.ActMenuPlayers,
		mm.ActGameHome, mm.ActGameNew, mm.ActGameStats,
	}
	for _, a := range owned {
		if !m.Owns(a) {
			t.Errorf("menu should own %q", a)
		}
	}
	notOwned := []string{mm.ActScorePlayer, mm.ActSetupStart, mm.ActMgmtRename, "", "garbage"}
	for _, a := range notOwned {
		if m.Owns(a) {
			t.Errorf("menu should not own %q", a)
		}
	}
}

func TestActionRendersMainMenu(t *testing.T) {
	m, _ := newTestMenu(t)
	for _, a := range []string{mm.ActGameHome, mm.ActMenuNewGame, mm.ActGameStats} {
		resp, err := m.Action(context.Background(), nil, mm.NavState{Action: a})
		if err != nil {
			t.Fatalf("Action(%q): %v", a, err)
		}
		if resp.Update == nil || resp.Update.Message != mainMenuText {
			t.Errorf("Action(%q) did not render main menu: %+v", a, resp)
		}
	}
}

func TestActionDefaultExpires(t *testing.T) {
	m, _ := newTestMenu(t)
	resp, err := m.Action(context.Background(), nil, mm.NavState{Action: "unrouted"})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if resp.EphemeralText != nav.ExpiredMessage {
		t.Errorf("default should expire, got %+v", resp)
	}
}
