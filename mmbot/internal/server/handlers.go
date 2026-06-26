package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
)

// SlashFunc handles an authenticated slash command.
type SlashFunc func(ctx context.Context, req *SlashRequest) (*model.CommandResponse, error)

// ActionFunc handles an authenticated interactive button click. nav is the
// verified nav-state decoded from the action `context`.
type ActionFunc func(ctx context.Context, req *model.PostActionIntegrationRequest, nav mm.NavState) (*model.PostActionIntegrationResponse, error)

// DialogFunc handles an authenticated dialog submission. nav is the verified
// nav-state decoded from the dialog `state`.
type DialogFunc func(ctx context.Context, req *model.SubmitDialogRequest, nav mm.NavState) (*model.SubmitDialogResponse, error)

// slashHandler builds the HandlerFunc for one slash route. expectedToken is the
// per-command bearer token; getFn fetches the (possibly nil) business handler at
// request time so later batches can wire it after construction.
func (s *Server) slashHandler(expectedToken string, getFn func() SlashFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			s.deny(w, "method")
			return
		}
		if err := r.ParseForm(); err != nil {
			// Oversized (MaxBytesReader) or malformed body.
			s.deny(w, "parse")
			return
		}
		req := parseSlash(r)

		// (a) Per-command bearer token, constant-time.
		if !tokenEqual(req.Token, expectedToken) {
			s.deny(w, "slash token mismatch")
			return
		}
		// (c) Owner guard.
		if !s.ownerOK(req.UserID) {
			s.deny(w, "non-owner")
			return
		}

		fn := getFn()
		if fn == nil {
			writeJSON(w, http.StatusOK, &model.CommandResponse{
				ResponseType: model.CommandResponseTypeEphemeral,
				Text:         "This command is not yet available.",
			})
			return
		}
		resp, err := fn(r.Context(), req)
		if err != nil {
			s.fail(w, "slash handler", err)
			return
		}
		if resp == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
}

// actionHandler authenticates and dispatches an interactive button click.
func (s *Server) actionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.deny(w, "method")
		return
	}
	var req model.PostActionIntegrationRequest
	if err := decodeJSON(r, &req); err != nil {
		// Includes the oversized-body (MaxBytesReader) case. Never log the body.
		s.deny(w, "decode")
		return
	}

	// (b) HMAC over the action `context`. The signed token lives under
	// NavContextKey; there is NO max-age (a game's scoreboard/end buttons must
	// verify for the game's whole life).
	token, _ := req.Context[NavContextKey].(string)
	nav, err := s.cfg.Signer.VerifyContext(token)
	if err != nil {
		s.deny(w, "action hmac")
		return
	}
	// (c) Owner guard.
	if !s.ownerOK(req.UserId) {
		s.deny(w, "non-owner")
		return
	}

	if s.handlers.Action == nil {
		writeJSON(w, http.StatusOK, &model.PostActionIntegrationResponse{
			EphemeralText: "This action is not yet available.",
		})
		return
	}
	resp, herr := s.handlers.Action(r.Context(), &req, nav)
	if herr != nil {
		s.fail(w, "action handler", herr)
		return
	}
	if resp == nil {
		writeJSON(w, http.StatusOK, &model.PostActionIntegrationResponse{})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// dialogHandler authenticates and dispatches a dialog submission.
func (s *Server) dialogHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.deny(w, "method")
		return
	}
	var req model.SubmitDialogRequest
	if err := decodeJSON(r, &req); err != nil {
		s.deny(w, "decode")
		return
	}

	// (b) HMAC over the dialog `state`. An optional freshness window may apply
	// to dialog state alone (DialogStateMaxAge; 0 disables). Business "expired"
	// messages are returned by the handler as ephemerals, not here.
	nav, err := s.cfg.Signer.VerifyState(req.State, s.cfg.DialogStateMaxAge)
	if err != nil {
		s.deny(w, "dialog hmac")
		return
	}
	// (c) Owner guard.
	if !s.ownerOK(req.UserId) {
		s.deny(w, "non-owner")
		return
	}

	if s.handlers.Dialog == nil {
		writeJSON(w, http.StatusOK, &model.SubmitDialogResponse{
			Error: "This form is not yet available.",
		})
		return
	}
	resp, herr := s.handlers.Dialog(r.Context(), &req, nav)
	if herr != nil {
		s.fail(w, "dialog handler", herr)
		return
	}
	if resp == nil {
		writeJSON(w, http.StatusOK, &model.SubmitDialogResponse{})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ownerOK reports whether userID is the configured owner. When OwnerUserID is
// unset (id resolved later from a username) the check cannot pass — callers must
// have resolved the owner id before serving.
func (s *Server) ownerOK(userID string) bool {
	if s.cfg.OwnerUserID == "" || userID == "" {
		return false
	}
	return userID == s.cfg.OwnerUserID
}

// tokenEqual compares a provided bearer token to the expected one in constant
// time. An empty expected token never matches.
func tokenEqual(provided, expected string) bool {
	if expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// deny writes a generic, information-free rejection. The reason is logged at
// debug level WITHOUT any request body, context, or state — only a short label.
func (s *Server) deny(w http.ResponseWriter, reason string) {
	s.log.Debug("inbound request denied", "reason", reason)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// fail reports a handler-side error generically to the client and logs the error
// (never the request body/context/state).
func (s *Server) fail(w http.ResponseWriter, where string, err error) {
	s.log.Error("inbound handler error", "where", where, "err", err.Error())
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// parseSlash extracts the slash form fields from a parsed request.
func parseSlash(r *http.Request) *SlashRequest {
	return &SlashRequest{
		Token:       r.PostFormValue("token"),
		TeamID:      r.PostFormValue("team_id"),
		TeamDomain:  r.PostFormValue("team_domain"),
		ChannelID:   r.PostFormValue("channel_id"),
		ChannelName: r.PostFormValue("channel_name"),
		UserID:      r.PostFormValue("user_id"),
		UserName:    r.PostFormValue("user_name"),
		Command:     r.PostFormValue("command"),
		Text:        r.PostFormValue("text"),
		ResponseURL: r.PostFormValue("response_url"),
		TriggerID:   r.PostFormValue("trigger_id"),
	}
}

// decodeJSON decodes a single JSON object from the (already size-capped) request
// body.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

// writeJSON marshals v and writes it with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
