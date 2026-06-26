package mm

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mattermost/mattermost/server/public/model"
)

// Poster is the posting / dialog surface the handlers depend on. It is an
// interface so handlers can be unit-tested against a fake implementation.
//
// Thread-per-game: a new/resumed game CreatePosts a root scoreboard post
// (PostMessage / PostAttachment); per-entry confirmations post as replies in
// that thread (PostInThread / PostAttachmentInThread, RootId = scoreboard post
// id); the scoreboard root is re-rendered in place via UpdateAttachment.
type Poster interface {
	// PostMessage creates a plain message post and returns the new post id.
	PostMessage(ctx context.Context, channelID, message string) (string, error)
	// PostInThread creates a plain reply post under rootID and returns its id.
	PostInThread(ctx context.Context, channelID, rootID, message string) (string, error)
	// PostAttachment creates a message post carrying Slack-style attachments
	// (interactive buttons) and returns the new post id.
	PostAttachment(ctx context.Context, channelID, message string, attachments []*model.SlackAttachment) (string, error)
	// PostAttachmentInThread creates an attachment reply post under rootID.
	PostAttachmentInThread(ctx context.Context, channelID, rootID, message string, attachments []*model.SlackAttachment) (string, error)
	// UpdatePost replaces the message of an existing post. Updating a
	// missing/deleted post is a benign no-op (returns nil).
	UpdatePost(ctx context.Context, postID, message string) error
	// UpdateAttachment replaces a post's message and Slack-style attachments in
	// place (used to re-render the scoreboard root post, including after a dialog
	// submission, since a SubmitDialogResponse cannot itself update a post).
	// Updating a missing/deleted post is a benign no-op (returns nil).
	UpdateAttachment(ctx context.Context, postID, message string, attachments []*model.SlackAttachment) error
	// DeletePost deletes a post. Deleting a missing/deleted post is a benign
	// no-op (returns nil).
	DeletePost(ctx context.Context, postID string) error
	// OpenDialog opens an interactive dialog within the trigger_id window.
	OpenDialog(ctx context.Context, triggerID, url string, dialog model.Dialog) error
}

// PostUpdater is the (small) subset of *model.Client4 the Client uses. It lets
// the Client be constructed over a fake in tests without a live server.
type PostUpdater interface {
	CreatePost(ctx context.Context, post *model.Post) (*model.Post, *model.Response, error)
	UpdatePost(ctx context.Context, postID string, post *model.Post) (*model.Post, *model.Response, error)
	DeletePost(ctx context.Context, postID string) (*model.Response, error)
	OpenInteractiveDialog(ctx context.Context, request model.OpenDialogRequest) (*model.Response, error)
}

// *model.Client4 already satisfies PostUpdater; this assertion documents it.
var _ PostUpdater = (*model.Client4)(nil)

// Client is the concrete Poster, wrapping a Mattermost Client4 (or any
// PostUpdater) plus the resolved channel id.
type Client struct {
	api       PostUpdater
	channelID string
}

// NewPoster builds a Client over the given API and resolved channel id.
func NewPoster(api PostUpdater, channelID string) *Client {
	return &Client{api: api, channelID: channelID}
}

var _ Poster = (*Client)(nil)

// ChannelID returns the resolved owner-only channel id.
func (c *Client) ChannelID() string { return c.channelID }

func (c *Client) PostMessage(ctx context.Context, channelID, message string) (string, error) {
	post := &model.Post{ChannelId: channelID, Message: message}
	created, _, err := c.api.CreatePost(ctx, post)
	if err != nil {
		return "", fmt.Errorf("mm: create post: %w", err)
	}
	return created.Id, nil
}

func (c *Client) PostInThread(ctx context.Context, channelID, rootID, message string) (string, error) {
	post := &model.Post{ChannelId: channelID, RootId: rootID, Message: message}
	created, _, err := c.api.CreatePost(ctx, post)
	if err != nil {
		return "", fmt.Errorf("mm: create thread reply: %w", err)
	}
	return created.Id, nil
}

func (c *Client) PostAttachment(ctx context.Context, channelID, message string, attachments []*model.SlackAttachment) (string, error) {
	post := &model.Post{ChannelId: channelID, Message: message}
	// ParseSlackAttachment stores the attachments under the post's "attachments"
	// prop (the shape the Mattermost UI renders into interactive buttons).
	model.ParseSlackAttachment(post, attachments)
	created, _, err := c.api.CreatePost(ctx, post)
	if err != nil {
		return "", fmt.Errorf("mm: create attachment post: %w", err)
	}
	return created.Id, nil
}

func (c *Client) PostAttachmentInThread(ctx context.Context, channelID, rootID, message string, attachments []*model.SlackAttachment) (string, error) {
	post := &model.Post{ChannelId: channelID, RootId: rootID, Message: message}
	model.ParseSlackAttachment(post, attachments)
	created, _, err := c.api.CreatePost(ctx, post)
	if err != nil {
		return "", fmt.Errorf("mm: create attachment thread reply: %w", err)
	}
	return created.Id, nil
}

func (c *Client) UpdatePost(ctx context.Context, postID, message string) error {
	post := &model.Post{Id: postID, Message: message}
	_, resp, err := c.api.UpdatePost(ctx, postID, post)
	if err != nil {
		// A tap on an already-deleted/closed post must not error the caller.
		if isNotFound(resp, err) {
			return nil
		}
		return fmt.Errorf("mm: update post: %w", err)
	}
	return nil
}

func (c *Client) UpdateAttachment(ctx context.Context, postID, message string, attachments []*model.SlackAttachment) error {
	post := &model.Post{Id: postID, Message: message}
	model.ParseSlackAttachment(post, attachments)
	_, resp, err := c.api.UpdatePost(ctx, postID, post)
	if err != nil {
		// A re-render targeting an already-deleted/closed post must not error.
		if isNotFound(resp, err) {
			return nil
		}
		return fmt.Errorf("mm: update attachment post: %w", err)
	}
	return nil
}

func (c *Client) DeletePost(ctx context.Context, postID string) error {
	resp, err := c.api.DeletePost(ctx, postID)
	if err != nil {
		if isNotFound(resp, err) {
			return nil
		}
		return fmt.Errorf("mm: delete post: %w", err)
	}
	return nil
}

func (c *Client) OpenDialog(ctx context.Context, triggerID, url string, dialog model.Dialog) error {
	req := model.OpenDialogRequest{TriggerId: triggerID, URL: url, Dialog: dialog}
	if _, err := c.api.OpenInteractiveDialog(ctx, req); err != nil {
		return fmt.Errorf("mm: open dialog: %w", err)
	}
	return nil
}

// isNotFound reports whether a Client4 call failed because the target post no
// longer exists (so UpdatePost/UpdateAttachment/DeletePost can treat it as a
// benign no-op). It is also the shared not-found predicate for client.go.
func isNotFound(resp *model.Response, err error) bool {
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return true
	}
	var appErr *model.AppError
	if errors.As(err, &appErr) && appErr.StatusCode == http.StatusNotFound {
		return true
	}
	return false
}
