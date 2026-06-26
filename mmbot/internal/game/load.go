package game

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/db"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
	"github.com/rivan/flip7bot/mmbot/internal/nav"
)

// showLoadList renders the channel's unfinished games as resume buttons (one per
// game), or a "no unfinished games" notice. Both carry a "← Back" to the menu.
func (s *Screen) showLoadList(ctx context.Context, channelID string) (*model.PostActionIntegrationResponse, error) {
	games, err := db.GetUnfinishedGames(ctx, s.db, channelID)
	if err != nil {
		return nil, err
	}
	back := s.nav.Button("← Back", mm.NavState{Action: mm.ActLoadBack})
	if len(games) == 0 {
		return nav.UpdateResponse(noUnfinishedMsg, []*model.SlackAttachment{{
			Actions: nav.CompactActions([]*model.PostAction{back}),
		}}), nil
	}

	actions := make([]*model.PostAction, 0, len(games)+1)
	for _, g := range games {
		players, err := db.GetGamePlayers(ctx, s.db, g.ID)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(players))
		for _, p := range players {
			names = append(names, p.Name)
		}
		label := fmt.Sprintf("Game #%d · %s · %s", g.ID, strings.Join(names, ", "), formatDate(g.StartedAt))
		actions = append(actions, s.nav.Button(label, mm.NavState{Action: mm.ActGameLoad, GameID: g.ID}))
	}
	actions = append(actions, back)
	return nav.UpdateResponse(loadPrompt, []*model.SlackAttachment{{Actions: nav.CompactActions(actions)}}), nil
}

// loadGame resumes an unfinished game into a fresh thread: the old scoreboard
// post (if any) is best-effort deleted so two live boards can't both drive one
// game, a fresh root post is created at the channel bottom, and the originating
// load-list post is retired into a non-interactive stub.
func (s *Screen) loadGame(ctx context.Context, channelID string, gameID int64) (*model.PostActionIntegrationResponse, error) {
	games, err := db.GetUnfinishedGames(ctx, s.db, channelID)
	if err != nil {
		return nil, err
	}
	var target *db.Game
	for i := range games {
		if games[i].ID == gameID {
			target = &games[i]
			break
		}
	}
	if target == nil {
		// Finished/discarded since the list was rendered.
		return nav.Ephemeral(gameMissingMsg), nil
	}

	if target.PostID.Valid && target.PostID.String != "" {
		_ = s.poster.DeletePost(ctx, target.PostID.String)
	}

	players, scores, err := s.loadState(ctx, gameID)
	if err != nil {
		return nil, err
	}
	message, att := s.RenderRoot(gameID, scores, players)
	postID, err := s.poster.PostAttachment(ctx, channelID, message, att)
	if err != nil {
		return nil, err
	}
	if err := db.UpdateGamePostID(ctx, s.db, gameID, postID); err != nil {
		return nil, err
	}
	// Retire the originating load-list post so only the fresh root post carries
	// the live controls.
	return nav.UpdateResponse(resumedStub, nil), nil
}

// formatDate renders a stored ISO timestamp as "Jan 2" (matching the Rust
// "%b %-d"). On a parse failure it falls back to the raw date portion.
func formatDate(stored string) string {
	for _, layout := range []string{
		"2006-01-02T15:04:05.000Z",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, stored); err == nil {
			return t.Format("Jan 2")
		}
	}
	if i := strings.IndexAny(stored, "T "); i > 0 {
		return stored[:i]
	}
	return stored
}
