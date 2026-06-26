package nav

import (
	"log/slog"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
)

// navContextKey is the key under which the HMAC-signed nav-state token travels in
// an action `context` map. It MUST match server.NavContextKey ("nav"); the two
// are kept in sync deliberately rather than importing server (which would invert
// the dependency direction — server depends on mm, screens depend on nav).
const navContextKey = "nav"

// ExpiredMessage is the ephemeral shown when an action carries stale or
// otherwise unroutable nav-state (the post outlived the flow it belonged to).
const ExpiredMessage = "This view has expired — run /flip7 to start again."

// Builder mints signed interactive elements. It holds the HMAC Signer plus the
// fully-qualified action and dialog endpoints derived from INTEGRATION_BASE_URL.
// The signing key never leaves the Signer; only the signed token is embedded in
// a post.
type Builder struct {
	signer    *mm.Signer
	actionURL string
	dialogURL string
	log       *slog.Logger
}

// NewBuilder constructs a Builder. integrationBaseURL is the externally
// reachable base (e.g. http://172.28.0.10:8068); the /action and /dialog
// endpoints are derived from it. A nil logger falls back to slog.Default.
func NewBuilder(signer *mm.Signer, integrationBaseURL string, log *slog.Logger) *Builder {
	base := strings.TrimRight(integrationBaseURL, "/")
	if log == nil {
		log = slog.Default()
	}
	return &Builder{
		signer:    signer,
		actionURL: base + "/action",
		dialogURL: base + "/dialog",
		log:       log,
	}
}

// ActionURL returns the /action endpoint buttons post back to.
func (b *Builder) ActionURL() string { return b.actionURL }

// DialogURL returns the /dialog endpoint an OpenDialog submission posts back to.
func (b *Builder) DialogURL() string { return b.dialogURL }

// SignState signs nav-state for use as a dialog `state` (browser-visible, but
// HMAC-authenticated). Screens that OpenDialog use this to seal the target
// reference into the dialog.
func (b *Builder) SignState(ns mm.NavState) (string, error) { return b.signer.SignState(ns) }

// Button signs ns into an action `context` and returns a button PostAction that
// posts back to /action. A signing failure is logged and yields a nil action so
// the caller can drop just that one button while the rest of the screen still
// renders; callers should run the assembled slice through CompactActions.
func (b *Builder) Button(label string, ns mm.NavState) *model.PostAction {
	token, err := b.signer.SignContext(ns)
	if err != nil {
		b.log.Error("nav: failed to sign button; skipping", "action", ns.Action, "err", err.Error())
		return nil
	}
	return &model.PostAction{
		Name: label,
		Integration: &model.PostActionIntegration{
			URL:     b.actionURL,
			Context: map[string]any{navContextKey: token},
		},
	}
}

// CompactActions drops nil entries (buttons that failed to sign) in place,
// preserving order, so a single signing failure cannot leave a hole in a row.
func CompactActions(actions []*model.PostAction) []*model.PostAction {
	out := actions[:0]
	for _, a := range actions {
		if a != nil {
			out = append(out, a)
		}
	}
	return out
}

// UpdateResponse builds an action response that re-renders the originating post
// in place with the given message and Slack-style attachments (interactive
// buttons). This is the in-place re-render used by every screen transition.
func UpdateResponse(message string, attachments []*model.SlackAttachment) *model.PostActionIntegrationResponse {
	post := &model.Post{Message: message}
	model.ParseSlackAttachment(post, attachments)
	return &model.PostActionIntegrationResponse{Update: post}
}

// Ephemeral builds an action response that shows only an ephemeral message to
// the clicking user, leaving the post unchanged.
func Ephemeral(text string) *model.PostActionIntegrationResponse {
	return &model.PostActionIntegrationResponse{EphemeralText: text}
}

// ExpiredResponse is the canonical ephemeral for stale/unroutable nav-state.
func ExpiredResponse() *model.PostActionIntegrationResponse {
	return Ephemeral(ExpiredMessage)
}
