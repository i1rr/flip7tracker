package nav

import (
	"context"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
)

// fakeActionScreen claims a fixed set of actions and records the call.
type fakeActionScreen struct {
	owns   map[string]bool
	called string
	resp   *model.PostActionIntegrationResponse
}

func (f *fakeActionScreen) Owns(a string) bool { return f.owns[a] }
func (f *fakeActionScreen) Action(_ context.Context, _ *model.PostActionIntegrationRequest, ns mm.NavState) (*model.PostActionIntegrationResponse, error) {
	f.called = ns.Action
	return f.resp, nil
}

func TestActionRouterDispatch(t *testing.T) {
	a := &fakeActionScreen{owns: map[string]bool{"a:1": true}, resp: Ephemeral("a")}
	b := &fakeActionScreen{owns: map[string]bool{"b:1": true}, resp: Ephemeral("b")}
	fb := &fakeActionScreen{owns: map[string]bool{}, resp: Ephemeral("fallback")}
	r := NewActionRouter(fb, a, b)

	resp, err := r.Action(context.Background(), nil, mm.NavState{Action: "b:1"})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if b.called != "b:1" {
		t.Errorf("screen b not called, called=%q", b.called)
	}
	if a.called != "" {
		t.Errorf("screen a should not be called, called=%q", a.called)
	}
	if resp.EphemeralText != "b" {
		t.Errorf("got resp %q, want b", resp.EphemeralText)
	}
}

func TestActionRouterFirstClaimWins(t *testing.T) {
	first := &fakeActionScreen{owns: map[string]bool{"x": true}, resp: Ephemeral("first")}
	second := &fakeActionScreen{owns: map[string]bool{"x": true}, resp: Ephemeral("second")}
	r := NewActionRouter(nil, first, second)
	resp, _ := r.Action(context.Background(), nil, mm.NavState{Action: "x"})
	if resp.EphemeralText != "first" {
		t.Errorf("first claim should win, got %q", resp.EphemeralText)
	}
	if second.called != "" {
		t.Errorf("second should not be called")
	}
}

func TestActionRouterFallback(t *testing.T) {
	fb := &fakeActionScreen{owns: map[string]bool{}, resp: Ephemeral("fallback")}
	r := NewActionRouter(fb)
	resp, _ := r.Action(context.Background(), nil, mm.NavState{Action: "unclaimed"})
	if fb.called != "unclaimed" {
		t.Errorf("fallback not called")
	}
	if resp.EphemeralText != "fallback" {
		t.Errorf("got %q, want fallback", resp.EphemeralText)
	}
}

func TestActionRouterNoFallbackExpires(t *testing.T) {
	r := NewActionRouter(nil)
	resp, err := r.Action(context.Background(), nil, mm.NavState{Action: "nobody"})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if resp.EphemeralText != ExpiredMessage {
		t.Errorf("got %q, want expired message", resp.EphemeralText)
	}
}

// fakeDialogScreen claims a fixed set of actions and records the call.
type fakeDialogScreen struct {
	owns   map[string]bool
	called string
}

func (f *fakeDialogScreen) Owns(a string) bool { return f.owns[a] }
func (f *fakeDialogScreen) Dialog(_ context.Context, _ *model.SubmitDialogRequest, ns mm.NavState) (*model.SubmitDialogResponse, error) {
	f.called = ns.Action
	return &model.SubmitDialogResponse{Error: "handled"}, nil
}

func TestDialogRouterDispatch(t *testing.T) {
	d := &fakeDialogScreen{owns: map[string]bool{"d:1": true}}
	r := NewDialogRouter(d)
	resp, err := r.Dialog(context.Background(), &model.SubmitDialogRequest{}, mm.NavState{Action: "d:1"})
	if err != nil {
		t.Fatalf("Dialog: %v", err)
	}
	if d.called != "d:1" {
		t.Errorf("dialog screen not called")
	}
	if resp.Error != "handled" {
		t.Errorf("got %q", resp.Error)
	}
}

func TestDialogRouterCancelled(t *testing.T) {
	d := &fakeDialogScreen{owns: map[string]bool{"d:1": true}}
	r := NewDialogRouter(d)
	resp, err := r.Dialog(context.Background(), &model.SubmitDialogRequest{Cancelled: true}, mm.NavState{Action: "d:1"})
	if err != nil {
		t.Fatalf("Dialog: %v", err)
	}
	if d.called != "" {
		t.Errorf("cancelled dialog should not reach a screen")
	}
	if resp.Error != "" {
		t.Errorf("cancelled dialog should be empty success, got %q", resp.Error)
	}
}

func TestDialogRouterUnclaimed(t *testing.T) {
	d := &fakeDialogScreen{owns: map[string]bool{"d:1": true}}
	r := NewDialogRouter(d)
	resp, err := r.Dialog(context.Background(), &model.SubmitDialogRequest{}, mm.NavState{Action: "other"})
	if err != nil {
		t.Fatalf("Dialog: %v", err)
	}
	if d.called != "" {
		t.Errorf("unclaimed dialog should not reach the screen")
	}
	if resp.Error != "" {
		t.Errorf("unclaimed dialog should close benignly, got %q", resp.Error)
	}
}

func TestUpdateResponseCarriesAttachments(t *testing.T) {
	att := []*model.SlackAttachment{{Text: "hi"}}
	resp := UpdateResponse("msg", att)
	if resp.Update == nil {
		t.Fatal("Update post is nil")
	}
	if resp.Update.Message != "msg" {
		t.Errorf("message = %q", resp.Update.Message)
	}
	if resp.Update.GetProp("attachments") == nil {
		t.Error("attachments prop not set on update post")
	}
}
