package mm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/mattermost/mattermost/server/public/model"
)

// NewAPIClient builds a Mattermost Client4 bound to mmURL and authenticated
// with the bot token. The token is the flip7 bot account's personal access
// token (MM_BOT_TOKEN).
func NewAPIClient(mmURL, botToken string) *model.Client4 {
	c := model.NewAPIv4Client(mmURL)
	c.SetToken(botToken)
	return c
}

// AdminAPI is the subset of *model.Client4 used during startup resolution and
// the membership / owner-only checks. Abstracting it lets the pure resolution
// logic be unit-tested against a fake without a live Mattermost server.
type AdminAPI interface {
	GetMe(ctx context.Context, etag string) (*model.User, *model.Response, error)
	GetTeamByName(ctx context.Context, name, etag string) (*model.Team, *model.Response, error)
	GetChannelByName(ctx context.Context, channelName, teamID, etag string) (*model.Channel, *model.Response, error)
	GetUserByUsername(ctx context.Context, username, etag string) (*model.User, *model.Response, error)
	GetUser(ctx context.Context, userID, etag string) (*model.User, *model.Response, error)
	GetTeamMember(ctx context.Context, teamID, userID, etag string) (*model.TeamMember, *model.Response, error)
	AddTeamMember(ctx context.Context, teamID, userID string) (*model.TeamMember, *model.Response, error)
	GetChannelMember(ctx context.Context, channelID, userID, etag string) (*model.ChannelMember, *model.Response, error)
	AddChannelMember(ctx context.Context, channelID, userID string) (*model.ChannelMember, *model.Response, error)
	GetChannelMembers(ctx context.Context, channelID string, page, perPage int, etag string) (model.ChannelMembers, *model.Response, error)
}

var _ AdminAPI = (*model.Client4)(nil)

// Resolution errors with actionable messages.
var (
	ErrBadCredentials = errors.New("mm: bot token rejected (check MM_BOT_TOKEN — see System Console → Integrations → Bot Accounts)")
	ErrTeamNotFound   = errors.New("mm: team not found (check MM_TEAM)")
	ErrChannelNotFnd  = errors.New("mm: channel not found (check MM_CHANNEL; the bot must be able to see it)")
	ErrOwnerNotFound  = errors.New("mm: owner not found (check OWNER_USERNAME / OWNER_USER_ID)")
)

// Resolved holds the cached ids resolved at startup.
type Resolved struct {
	BotUserID   string
	TeamID      string
	ChannelID   string
	OwnerUserID string
}

// channelMemberPage is the page size for enumerating channel members during the
// owner-only check. The flip7 channel is expected to hold only the bot and the
// owner, so one page is plenty; we still page defensively.
const channelMemberPage = 200

// Resolve looks up and caches the team, channel, owner, and bot ids; ensures the
// bot is a member of both the team and channel (adding it when missing); and
// verifies the channel is owner-only. ownerUserID may be empty when only an
// owner username is configured. It returns the resolved ids; owner-only
// violations are logged loudly (warn) rather than fatal, since the upstream
// guards (HMAC + user_id check) still hold.
func Resolve(ctx context.Context, api AdminAPI, log *slog.Logger, teamName, channelName, ownerUsername, ownerUserID string) (*Resolved, error) {
	// Bot identity — also the first check that the token is valid.
	me, resp, err := api.GetMe(ctx, "")
	if err != nil {
		if isUnauthorized(resp, err) {
			return nil, ErrBadCredentials
		}
		return nil, fmt.Errorf("mm: resolve bot identity: %w", err)
	}

	team, resp, err := api.GetTeamByName(ctx, teamName, "")
	if err != nil {
		if isNotFound(resp, err) {
			return nil, fmt.Errorf("%w: %q", ErrTeamNotFound, teamName)
		}
		return nil, fmt.Errorf("mm: resolve team %q: %w", teamName, err)
	}

	// Ensure team membership before channel resolution (a non-member bot can't
	// see team channels).
	if err := ensureTeamMember(ctx, api, log, team.Id, me.Id); err != nil {
		return nil, err
	}

	channel, resp, err := api.GetChannelByName(ctx, channelName, team.Id, "")
	if err != nil {
		if isNotFound(resp, err) {
			return nil, fmt.Errorf("%w: %q", ErrChannelNotFnd, channelName)
		}
		return nil, fmt.Errorf("mm: resolve channel %q: %w", channelName, err)
	}

	if err := ensureChannelMember(ctx, api, log, channel.Id, me.Id); err != nil {
		return nil, err
	}

	// Owner id: prefer the configured id; otherwise resolve the username.
	resolvedOwnerID := ownerUserID
	if resolvedOwnerID == "" {
		owner, resp, err := api.GetUserByUsername(ctx, ownerUsername, "")
		if err != nil {
			if isNotFound(resp, err) {
				return nil, fmt.Errorf("%w: %q", ErrOwnerNotFound, ownerUsername)
			}
			return nil, fmt.Errorf("mm: resolve owner %q: %w", ownerUsername, err)
		}
		resolvedOwnerID = owner.Id
	}

	// Owner-only channel verification (warn, not fatal).
	verifyOwnerOnly(ctx, api, log, channel.Id, me.Id, resolvedOwnerID)

	log.Info("mattermost resolution complete",
		"bot_user_id", me.Id,
		"team_id", team.Id,
		"channel_id", channel.Id,
		"owner_user_id", resolvedOwnerID)

	return &Resolved{
		BotUserID:   me.Id,
		TeamID:      team.Id,
		ChannelID:   channel.Id,
		OwnerUserID: resolvedOwnerID,
	}, nil
}

// ensureTeamMember adds the bot to the team when it is not already a member.
func ensureTeamMember(ctx context.Context, api AdminAPI, log *slog.Logger, teamID, botID string) error {
	_, resp, err := api.GetTeamMember(ctx, teamID, botID, "")
	if err == nil {
		return nil
	}
	if !isNotFound(resp, err) {
		return fmt.Errorf("mm: check team membership: %w", err)
	}
	log.Warn("bot not a team member; adding", "team_id", teamID)
	if _, _, err := api.AddTeamMember(ctx, teamID, botID); err != nil {
		return fmt.Errorf("mm: bot is not a team member and could not be added "+
			"(add it manually in System Console → User Management, or grant the bot team access): %w", err)
	}
	return nil
}

// ensureChannelMember adds the bot to the channel when it is not already a member.
func ensureChannelMember(ctx context.Context, api AdminAPI, log *slog.Logger, channelID, botID string) error {
	_, resp, err := api.GetChannelMember(ctx, channelID, botID, "")
	if err == nil {
		return nil
	}
	if !isNotFound(resp, err) {
		return fmt.Errorf("mm: check channel membership: %w", err)
	}
	log.Warn("bot not a channel member; adding", "channel_id", channelID)
	if _, _, err := api.AddChannelMember(ctx, channelID, botID); err != nil {
		return fmt.Errorf("mm: bot is not a channel member and could not be added "+
			"(add the bot to the flip7 channel, or invite it via /invite @<bot>): %w", err)
	}
	return nil
}

// verifyOwnerOnly checks that the only human member of the channel is the owner.
// Bots (e.g. the flip7 bot itself) are ignored. A violation is logged loudly
// because the user_id==owner guard on every /slash, /action, and /dialog assumes
// no other human can click buttons; it is not fatal because the HMAC + network
// scoping remain in force.
func verifyOwnerOnly(ctx context.Context, api AdminAPI, log *slog.Logger, channelID, botID, ownerID string) {
	var extras []string
	for page := 0; ; page++ {
		members, _, err := api.GetChannelMembers(ctx, channelID, page, channelMemberPage, "")
		if err != nil {
			log.Warn("could not verify owner-only channel (membership lookup failed)",
				"channel_id", channelID, "error", err.Error())
			return
		}
		if len(members) == 0 {
			break
		}
		for _, m := range members {
			if m.UserId == botID || m.UserId == ownerID {
				continue
			}
			// Skip other bots; flag any human.
			if u, _, err := api.GetUser(ctx, m.UserId, ""); err == nil && u.IsBot {
				continue
			}
			extras = append(extras, m.UserId)
		}
		if len(members) < channelMemberPage {
			break
		}
	}
	if len(extras) > 0 {
		log.Warn("CHANNEL IS NOT OWNER-ONLY — extra human members can click buttons; "+
			"the user_id==owner guard assumes an owner-only channel",
			"channel_id", channelID, "extra_member_ids", extras)
	}
}

// isUnauthorized reports whether a call failed due to a bad/expired token.
func isUnauthorized(resp *model.Response, err error) bool {
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return true
	}
	var appErr *model.AppError
	if errors.As(err, &appErr) && appErr.StatusCode == http.StatusUnauthorized {
		return true
	}
	return false
}
