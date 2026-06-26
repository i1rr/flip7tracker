package nav

import (
	"context"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
)

// ActionScreen is implemented by every screen package that handles interactive
// button clicks. Owns reports whether the screen claims a given action code; the
// router routes a verified NavState to the first screen that claims it. This keeps
// nav free of any screen-package import (nav <- screens <- main).
type ActionScreen interface {
	Owns(action string) bool
	Action(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error)
}

// ActionRouter dispatches a verified action to the first registered screen whose
// Owns(action) returns true, falling back to a default owner (typically the
// menu) for any action no screen claims — so the dispatch partition has no
// undefined fallthrough. If no screen claims it and there is no fallback, the
// router returns the benign "expired" ephemeral.
type ActionRouter struct {
	screens  []ActionScreen
	fallback ActionScreen
}

// NewActionRouter builds a router. fallback is the explicit default owner (may be
// nil); screens are consulted in order, so a screen earlier in the slice wins a
// contested action code.
func NewActionRouter(fallback ActionScreen, screens ...ActionScreen) *ActionRouter {
	return &ActionRouter{screens: screens, fallback: fallback}
}

// Action routes ns to its owning screen. It implements ActionScreen.Action so it
// can itself be composed.
func (r *ActionRouter) Action(ctx context.Context, req *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	for _, s := range r.screens {
		if s.Owns(ns.Action) {
			return s.Action(ctx, req, ns)
		}
	}
	if r.fallback != nil {
		return r.fallback.Action(ctx, req, ns)
	}
	return ExpiredResponse(), nil
}

// DialogScreen is implemented by screen packages that handle dialog submissions.
type DialogScreen interface {
	Owns(action string) bool
	Dialog(ctx context.Context, req *model.SubmitDialogRequest, ns mm.NavState) (*model.SubmitDialogResponse, error)
}

// DialogRouter dispatches a verified dialog submission to the first registered
// screen whose Owns(action) returns true. A cancelled submission (req.Cancelled)
// short-circuits to an empty success before any screen is consulted. An action no
// screen claims yields an empty success too (the originating post is unchanged;
// the stale dialog simply closes).
type DialogRouter struct {
	screens []DialogScreen
}

// NewDialogRouter builds a dialog router over the given screens (consulted in
// order).
func NewDialogRouter(screens ...DialogScreen) *DialogRouter {
	return &DialogRouter{screens: screens}
}

// Dialog routes a dialog submission to its owning screen.
func (r *DialogRouter) Dialog(ctx context.Context, req *model.SubmitDialogRequest, ns mm.NavState) (*model.SubmitDialogResponse, error) {
	if req != nil && req.Cancelled {
		// A cancelled dialog is a benign no-op success.
		return &model.SubmitDialogResponse{}, nil
	}
	for _, s := range r.screens {
		if s.Owns(ns.Action) {
			return s.Dialog(ctx, req, ns)
		}
	}
	// Nothing claims it: close the (stale) dialog without error.
	return &model.SubmitDialogResponse{}, nil
}
