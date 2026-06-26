package mm

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// notFound is a *model.Response carrying a 404, paired with a generic error.
func notFound() (*model.Response, error) {
	return &model.Response{StatusCode: 404}, errors.New("not found")
}

// fakeAdmin implements AdminAPI with injectable behavior.
type fakeAdmin struct {
	me     *model.User
	meErr  error
	meResp *model.Response

	team    *model.Team
	teamErr bool // return 404
	channel *model.Channel
	chanErr bool // return 404
	owner   *model.User

	teamMemberMissing bool
	chanMemberMissing bool
	addedTeamMember   bool
	addedChanMember   bool

	members []model.ChannelMember // extra members for owner-only check
	bots    map[string]bool       // userID -> isBot
}

func (f *fakeAdmin) GetMe(context.Context, string) (*model.User, *model.Response, error) {
	if f.meErr != nil {
		return nil, f.meResp, f.meErr
	}
	return f.me, nil, nil
}
func (f *fakeAdmin) GetTeamByName(context.Context, string, string) (*model.Team, *model.Response, error) {
	if f.teamErr {
		r, e := notFound()
		return nil, r, e
	}
	return f.team, nil, nil
}
func (f *fakeAdmin) GetChannelByName(context.Context, string, string, string) (*model.Channel, *model.Response, error) {
	if f.chanErr {
		r, e := notFound()
		return nil, r, e
	}
	return f.channel, nil, nil
}
func (f *fakeAdmin) GetUserByUsername(context.Context, string, string) (*model.User, *model.Response, error) {
	if f.owner == nil {
		r, e := notFound()
		return nil, r, e
	}
	return f.owner, nil, nil
}
func (f *fakeAdmin) GetUser(_ context.Context, userID, _ string) (*model.User, *model.Response, error) {
	return &model.User{Id: userID, IsBot: f.bots[userID]}, nil, nil
}
func (f *fakeAdmin) GetTeamMember(context.Context, string, string, string) (*model.TeamMember, *model.Response, error) {
	if f.teamMemberMissing {
		r, e := notFound()
		return nil, r, e
	}
	return &model.TeamMember{}, nil, nil
}
func (f *fakeAdmin) AddTeamMember(context.Context, string, string) (*model.TeamMember, *model.Response, error) {
	f.addedTeamMember = true
	return &model.TeamMember{}, nil, nil
}
func (f *fakeAdmin) GetChannelMember(context.Context, string, string, string) (*model.ChannelMember, *model.Response, error) {
	if f.chanMemberMissing {
		r, e := notFound()
		return nil, r, e
	}
	return &model.ChannelMember{}, nil, nil
}
func (f *fakeAdmin) AddChannelMember(context.Context, string, string) (*model.ChannelMember, *model.Response, error) {
	f.addedChanMember = true
	return &model.ChannelMember{}, nil, nil
}
func (f *fakeAdmin) GetChannelMembers(_ context.Context, _ string, page, _ int, _ string) (model.ChannelMembers, *model.Response, error) {
	if page > 0 {
		return model.ChannelMembers{}, nil, nil
	}
	return model.ChannelMembers(f.members), nil, nil
}

var _ AdminAPI = (*fakeAdmin)(nil)

// warnCounter is a slog.Handler that counts WARN records.
type warnCounter struct {
	mu sync.Mutex
	n  int
}

func (w *warnCounter) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		w.mu.Lock()
		w.n++
		w.mu.Unlock()
	}
	return nil
}
func (w *warnCounter) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnCounter) WithGroup(string) slog.Handler      { return w }

func baseAdmin() *fakeAdmin {
	return &fakeAdmin{
		me:      &model.User{Id: "bot1", IsBot: true},
		team:    &model.Team{Id: "team1"},
		channel: &model.Channel{Id: "chan1"},
		owner:   &model.User{Id: "owner1"},
		members: []model.ChannelMember{{UserId: "bot1"}, {UserId: "owner1"}},
		bots:    map[string]bool{"bot1": true},
	}
}

func TestResolve_HappyPath(t *testing.T) {
	wc := &warnCounter{}
	log := slog.New(wc)
	f := baseAdmin()
	r, err := Resolve(context.Background(), f, log, "team", "chan", "owneruser", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.BotUserID != "bot1" || r.TeamID != "team1" || r.ChannelID != "chan1" || r.OwnerUserID != "owner1" {
		t.Fatalf("unexpected resolution: %+v", r)
	}
	if wc.n != 0 {
		t.Fatalf("expected no warnings on clean resolve, got %d", wc.n)
	}
}

func TestResolve_OwnerIDOverridesUsername(t *testing.T) {
	f := baseAdmin()
	f.members = []model.ChannelMember{{UserId: "bot1"}, {UserId: "ownerX"}}
	r, err := Resolve(context.Background(), f, discardLog(), "team", "chan", "", "ownerX")
	if err != nil {
		t.Fatal(err)
	}
	if r.OwnerUserID != "ownerX" {
		t.Fatalf("expected owner id passthrough, got %q", r.OwnerUserID)
	}
}

func TestResolve_BadToken(t *testing.T) {
	f := baseAdmin()
	f.meErr = errors.New("unauthorized")
	f.meResp = &model.Response{StatusCode: 401}
	if _, err := Resolve(context.Background(), f, discardLog(), "team", "chan", "u", ""); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("expected ErrBadCredentials, got %v", err)
	}
}

func TestResolve_MissingTeamAndChannel(t *testing.T) {
	f := baseAdmin()
	f.teamErr = true
	if _, err := Resolve(context.Background(), f, discardLog(), "team", "chan", "u", ""); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("expected ErrTeamNotFound, got %v", err)
	}

	f2 := baseAdmin()
	f2.chanErr = true
	if _, err := Resolve(context.Background(), f2, discardLog(), "team", "chan", "u", ""); !errors.Is(err, ErrChannelNotFnd) {
		t.Fatalf("expected ErrChannelNotFnd, got %v", err)
	}
}

func TestResolve_OwnerNotFound(t *testing.T) {
	f := baseAdmin()
	f.owner = nil // GetUserByUsername returns 404
	if _, err := Resolve(context.Background(), f, discardLog(), "team", "chan", "ghost", ""); !errors.Is(err, ErrOwnerNotFound) {
		t.Fatalf("expected ErrOwnerNotFound, got %v", err)
	}
}

func TestResolve_AddsMissingMemberships(t *testing.T) {
	f := baseAdmin()
	f.teamMemberMissing = true
	f.chanMemberMissing = true
	if _, err := Resolve(context.Background(), f, discardLog(), "team", "chan", "u", ""); err != nil {
		t.Fatal(err)
	}
	if !f.addedTeamMember || !f.addedChanMember {
		t.Fatalf("expected bot to be added to team and channel: team=%v chan=%v", f.addedTeamMember, f.addedChanMember)
	}
}

func TestResolve_OwnerOnlyViolationWarns(t *testing.T) {
	wc := &warnCounter{}
	log := slog.New(wc)
	f := baseAdmin()
	// Add a third, human member.
	f.members = append(f.members, model.ChannelMember{UserId: "intruder"})
	if _, err := Resolve(context.Background(), f, log, "team", "chan", "owneruser", ""); err != nil {
		t.Fatal(err)
	}
	if wc.n == 0 {
		t.Fatal("expected a loud warning for a non-owner-only channel")
	}
}

func TestResolve_OtherBotsIgnoredInOwnerOnly(t *testing.T) {
	wc := &warnCounter{}
	log := slog.New(wc)
	f := baseAdmin()
	f.members = append(f.members, model.ChannelMember{UserId: "otherbot"})
	f.bots["otherbot"] = true
	if _, err := Resolve(context.Background(), f, log, "team", "chan", "owneruser", ""); err != nil {
		t.Fatal(err)
	}
	if wc.n != 0 {
		t.Fatalf("other bots should not trigger owner-only warning, got %d warns", wc.n)
	}
}

func discardLog() *slog.Logger {
	return slog.New(&warnCounter{})
}
