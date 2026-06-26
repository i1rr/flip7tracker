package game

import (
	"context"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
)

func TestLoadListEmpty(t *testing.T) {
	s, _, _ := newTestScreen(t)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActMenuLoadGame})
	if err != nil {
		t.Fatalf("load list: %v", err)
	}
	if resp.Update.Message != noUnfinishedMsg {
		t.Errorf("expected empty notice, got %q", resp.Update.Message)
	}
	if !has(labels(resp.Update), "← Back") {
		t.Errorf("expected back button, got %v", labels(resp.Update))
	}
}

func TestLoadListShowsGames(t *testing.T) {
	s, _, database := newTestScreen(t)
	gid, _ := seedGame(t, database, "Alice", "Bob")
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActMenuLoadGame})
	if err != nil {
		t.Fatalf("load list: %v", err)
	}
	var loadBtn *model.PostAction
	for _, b := range buttons(resp.Update) {
		if strings.HasPrefix(b.Name, "Game #") {
			loadBtn = b
		}
	}
	if loadBtn == nil {
		t.Fatalf("missing game button, got %v", labels(resp.Update))
	}
	if !strings.Contains(loadBtn.Name, "Alice, Bob") {
		t.Errorf("label missing player names: %q", loadBtn.Name)
	}
	ns := navOf(t, loadBtn)
	if ns.Action != mm.ActGameLoad || ns.GameID != gid {
		t.Errorf("load button nav wrong: %+v", ns)
	}
}

func TestLoadGameResumesFreshThread(t *testing.T) {
	s, p, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	addScore(t, database, gid, players[0].ID, 40)

	resp, err := s.Action(ctx, actReq(), mm.NavState{Action: mm.ActGameLoad, GameID: gid})
	if err != nil {
		t.Fatalf("load game: %v", err)
	}
	// Old root deleted, fresh root posted.
	if len(p.deleted) != 1 || p.deleted[0] != "root1" {
		t.Errorf("expected old root deleted, got %v", p.deleted)
	}
	if p.creates != 1 {
		t.Errorf("expected one fresh root post, got %d", p.creates)
	}
	// Game post id updated to the new post.
	games, _ := db.GetUnfinishedGames(ctx, database, testChannel)
	if len(games) != 1 || games[0].PostID.String != "newroot1" {
		t.Errorf("post id not updated: %+v", games)
	}
	// Originating load-list post retired into a control-less stub.
	if resp.Update.Message != resumedStub || len(resp.Update.Attachments()) != 0 {
		t.Errorf("expected resumed stub, got %+v", resp.Update)
	}
}

func TestLoadGameMissing(t *testing.T) {
	s, _, _ := newTestScreen(t)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActGameLoad, GameID: 4242})
	if err != nil {
		t.Fatalf("load game: %v", err)
	}
	if resp.EphemeralText != gameMissingMsg {
		t.Errorf("expected missing-game ephemeral, got %+v", resp)
	}
}

func TestLoadBackUsesMainMenu(t *testing.T) {
	called := false
	s, _, _ := newTestScreen(t, WithMainMenuRenderer(func() *model.PostActionIntegrationResponse {
		called = true
		return nav.UpdateResponse("MENU", nil)
	}))
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActLoadBack})
	if err != nil {
		t.Fatalf("back: %v", err)
	}
	if !called || resp.Update.Message != "MENU" {
		t.Errorf("load:back should render main menu, got %+v", resp)
	}
}

func TestLoadBackWithoutRendererExpires(t *testing.T) {
	s, _, _ := newTestScreen(t)
	resp, err := s.Action(context.Background(), actReq(), mm.NavState{Action: mm.ActLoadBack})
	if err != nil {
		t.Fatalf("back: %v", err)
	}
	if resp.EphemeralText != nav.ExpiredMessage {
		t.Errorf("expected expired fallback, got %+v", resp)
	}
}

func TestRepostScoreboardNoGame(t *testing.T) {
	s, _, _ := newTestScreen(t)
	resp, err := s.RepostScoreboard(context.Background(), testChannel)
	if err != nil {
		t.Fatalf("repost: %v", err)
	}
	if resp.Text != "No active game." {
		t.Errorf("expected no-active-game, got %+v", resp)
	}
}

func TestRepostScoreboardReplacesPost(t *testing.T) {
	s, p, database := newTestScreen(t)
	ctx := context.Background()
	gid, players := seedGame(t, database, "Alice", "Bob")
	addScore(t, database, gid, players[0].ID, 15)

	resp, err := s.RepostScoreboard(ctx, testChannel)
	if err != nil {
		t.Fatalf("repost: %v", err)
	}
	if len(p.deleted) != 1 || p.deleted[0] != "root1" {
		t.Errorf("expected old root deleted, got %v", p.deleted)
	}
	if p.creates != 1 {
		t.Errorf("expected fresh post, got %d", p.creates)
	}
	games, _ := db.GetUnfinishedGames(ctx, database, testChannel)
	if games[0].PostID.String != "newroot1" {
		t.Errorf("post id not updated: %q", games[0].PostID.String)
	}
	// Posted directly; the slash command stays silent.
	if resp.Text != "" {
		t.Errorf("expected silent response, got %q", resp.Text)
	}
}
