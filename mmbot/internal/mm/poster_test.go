package mm

import (
	"context"
	"errors"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// fakeAPI implements PostUpdater for unit tests.
type fakeAPI struct {
	createdPost  *model.Post
	openedDialog *model.OpenDialogRequest

	updateErr  error
	updateResp *model.Response
	deleteErr  error
	deleteResp *model.Response
}

func (f *fakeAPI) CreatePost(_ context.Context, post *model.Post) (*model.Post, *model.Response, error) {
	post.Id = "newpost"
	f.createdPost = post
	return post, nil, nil
}

func (f *fakeAPI) UpdatePost(_ context.Context, _ string, post *model.Post) (*model.Post, *model.Response, error) {
	if f.updateErr != nil {
		return nil, f.updateResp, f.updateErr
	}
	return post, nil, nil
}

func (f *fakeAPI) DeletePost(_ context.Context, _ string) (*model.Response, error) {
	if f.deleteErr != nil {
		return f.deleteResp, f.deleteErr
	}
	return nil, nil
}

func (f *fakeAPI) OpenInteractiveDialog(_ context.Context, request model.OpenDialogRequest) (*model.Response, error) {
	f.openedDialog = &request
	return nil, nil
}

var _ PostUpdater = (*fakeAPI)(nil)

func TestClient_PostMessage(t *testing.T) {
	f := &fakeAPI{}
	c := NewPoster(f, "chan1")
	id, err := c.PostMessage(context.Background(), "chan1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if id != "newpost" || f.createdPost.Message != "hello" || f.createdPost.ChannelId != "chan1" {
		t.Fatalf("unexpected post: id=%q %+v", id, f.createdPost)
	}
	if f.createdPost.RootId != "" {
		t.Fatalf("plain message must not set RootId, got %q", f.createdPost.RootId)
	}
}

func TestClient_PostInThread_SetsRoot(t *testing.T) {
	f := &fakeAPI{}
	c := NewPoster(f, "chan1")
	if _, err := c.PostInThread(context.Background(), "chan1", "root1", "✏️ Ann: 12"); err != nil {
		t.Fatal(err)
	}
	if f.createdPost.RootId != "root1" {
		t.Fatalf("expected RootId=root1, got %q", f.createdPost.RootId)
	}
}

func TestClient_PostAttachment_BuildsProp(t *testing.T) {
	f := &fakeAPI{}
	c := NewPoster(f, "chan1")
	att := []*model.SlackAttachment{{
		Text:    "🎮 Flip 7",
		Actions: []*model.PostAction{{Name: "New Game"}},
	}}
	if _, err := c.PostAttachment(context.Background(), "chan1", "board", att); err != nil {
		t.Fatal(err)
	}
	if f.createdPost.RootId != "" {
		t.Fatalf("root post must not set RootId, got %q", f.createdPost.RootId)
	}
	if _, ok := f.createdPost.GetProps()["attachments"]; !ok {
		t.Fatal("expected attachments prop to be set on the post")
	}
}

func TestClient_PostAttachmentInThread_SetsRootAndProp(t *testing.T) {
	f := &fakeAPI{}
	c := NewPoster(f, "chan1")
	att := []*model.SlackAttachment{{Text: "win"}}
	if _, err := c.PostAttachmentInThread(context.Background(), "chan1", "root9", "winner", att); err != nil {
		t.Fatal(err)
	}
	if f.createdPost.RootId != "root9" {
		t.Fatalf("expected RootId=root9, got %q", f.createdPost.RootId)
	}
	if _, ok := f.createdPost.GetProps()["attachments"]; !ok {
		t.Fatal("expected attachments prop on the thread reply")
	}
}

func TestClient_UpdatePost_NoOpOnMissing(t *testing.T) {
	f := &fakeAPI{
		updateErr:  &model.AppError{StatusCode: 404, Message: "post not found"},
		updateResp: &model.Response{StatusCode: 404},
	}
	c := NewPoster(f, "chan1")
	if err := c.UpdatePost(context.Background(), "gone", "x"); err != nil {
		t.Fatalf("expected benign no-op on 404, got %v", err)
	}

	// A non-404 error must propagate.
	f2 := &fakeAPI{updateErr: errors.New("boom"), updateResp: &model.Response{StatusCode: 500}}
	c2 := NewPoster(f2, "chan1")
	if err := c2.UpdatePost(context.Background(), "p", "x"); err == nil {
		t.Fatal("expected non-404 update error to propagate")
	}
}

func TestClient_UpdateAttachment_NoOpOnMissing(t *testing.T) {
	f := &fakeAPI{
		updateErr:  &model.AppError{StatusCode: 404},
		updateResp: &model.Response{StatusCode: 404},
	}
	c := NewPoster(f, "chan1")
	att := []*model.SlackAttachment{{Text: "board"}}
	if err := c.UpdateAttachment(context.Background(), "gone", "x", att); err != nil {
		t.Fatalf("expected benign no-op on 404 attachment update, got %v", err)
	}
}

func TestClient_DeletePost_NoOpOnMissing(t *testing.T) {
	f := &fakeAPI{deleteResp: &model.Response{StatusCode: 404}, deleteErr: errors.New("not found")}
	c := NewPoster(f, "chan1")
	if err := c.DeletePost(context.Background(), "gone"); err != nil {
		t.Fatalf("expected benign no-op on 404 delete, got %v", err)
	}

	// A non-404 delete error must propagate.
	f2 := &fakeAPI{deleteResp: &model.Response{StatusCode: 500}, deleteErr: errors.New("boom")}
	c2 := NewPoster(f2, "chan1")
	if err := c2.DeletePost(context.Background(), "p"); err == nil {
		t.Fatal("expected non-404 delete error to propagate")
	}
}

func TestClient_OpenDialog(t *testing.T) {
	f := &fakeAPI{}
	c := NewPoster(f, "chan1")
	d := model.Dialog{CallbackId: "cb", Title: "T"}
	if err := c.OpenDialog(context.Background(), "trig", "http://x/dialog", d); err != nil {
		t.Fatal(err)
	}
	if f.openedDialog.TriggerId != "trig" || f.openedDialog.URL != "http://x/dialog" || f.openedDialog.Dialog.CallbackId != "cb" {
		t.Fatalf("unexpected dialog request: %+v", f.openedDialog)
	}
}
