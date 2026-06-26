package nav

import (
	"reflect"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rivan/flip7bot/mmbot/internal/mm"
)

const testKey = "0123456789abcdef0123456789abcdef" // 32 bytes

func newTestBuilder(t *testing.T) (*Builder, *mm.Signer) {
	t.Helper()
	signer, err := mm.NewSigner(testKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return NewBuilder(signer, "http://bot.example:8068/", nil), signer
}

func TestBuilderURLs(t *testing.T) {
	b, _ := newTestBuilder(t)
	if got := b.ActionURL(); got != "http://bot.example:8068/action" {
		t.Errorf("ActionURL = %q", got)
	}
	if got := b.DialogURL(); got != "http://bot.example:8068/dialog" {
		t.Errorf("DialogURL = %q", got)
	}
}

func TestButtonRoundTrip(t *testing.T) {
	b, signer := newTestBuilder(t)
	cases := []mm.NavState{
		{Action: mm.ActMenuNewGame},
		{Action: mm.ActScorePlayer, GameID: 7, PlayerID: 42},
		{Action: mm.ActEditConfirm, GameID: 3, EntryID: 99},
		{Action: mm.ActKnownPage, Page: 2, Players: []int64{1, 2, 3}},
		{Action: mm.ActGameLoad, GameID: 5, PostID: "post123"},
	}
	for _, ns := range cases {
		act := b.Button("label", ns)
		if act == nil {
			t.Fatalf("Button(%+v) returned nil", ns)
		}
		if act.Name != "label" {
			t.Errorf("Name = %q, want label", act.Name)
		}
		if act.Integration == nil || act.Integration.URL != b.ActionURL() {
			t.Fatalf("Integration URL not set correctly: %+v", act.Integration)
		}
		token, ok := act.Integration.Context[navContextKey].(string)
		if !ok || token == "" {
			t.Fatalf("nav token missing from context: %+v", act.Integration.Context)
		}
		got, err := signer.VerifyContext(token)
		if err != nil {
			t.Fatalf("VerifyContext: %v", err)
		}
		// IssuedAt is stamped at sign time; ignore it for the equality check.
		want := ns
		want.IssuedAt = got.IssuedAt
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
		}
	}
}

func TestCompactActions(t *testing.T) {
	b, _ := newTestBuilder(t)
	a := b.Button("a", mm.NavState{Action: mm.ActGameHome})
	c := b.Button("c", mm.NavState{Action: mm.ActMenuStats})
	in := []*model.PostAction{nil, a, nil, c, nil}
	out := CompactActions(in)
	if len(out) != 2 {
		t.Fatalf("CompactActions kept %d, want 2", len(out))
	}
	if out[0].Name != "a" || out[1].Name != "c" {
		t.Errorf("CompactActions did not preserve order: %q, %q", out[0].Name, out[1].Name)
	}
}
