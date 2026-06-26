package mm

import (
	"strings"
	"testing"
	"time"
)

const testKey = "0123456789abcdef0123456789abcdef" // exactly 32 bytes

func newTestSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(testKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestNewSigner_KeyLength(t *testing.T) {
	if _, err := NewSigner("tooshort"); err == nil {
		t.Fatal("expected error for short key")
	}
	if _, err := NewSigner(strings.Repeat("x", MinHMACKeyBytes-1)); err == nil {
		t.Fatal("expected error for 31-byte key")
	}
	if _, err := NewSigner(strings.Repeat("x", MinHMACKeyBytes)); err != nil {
		t.Fatalf("expected ok for exactly-min key, got %v", err)
	}
}

func TestSignVerifyContext_RoundTrip(t *testing.T) {
	s := newTestSigner(t)
	in := NavState{
		Action:   ActScorePlayer,
		GameID:   42,
		PlayerID: 7,
		EntryID:  99,
		Page:     2,
		Players:  []int64{3, 5, 8},
		PostID:   "abc123",
	}
	tok, err := s.SignContext(in)
	if err != nil {
		t.Fatalf("SignContext: %v", err)
	}
	out, err := s.VerifyContext(tok)
	if err != nil {
		t.Fatalf("VerifyContext: %v", err)
	}
	if out.Action != in.Action || out.GameID != in.GameID || out.PlayerID != in.PlayerID ||
		out.EntryID != in.EntryID || out.Page != in.Page || out.PostID != in.PostID {
		t.Fatalf("round-trip mismatch: in=%+v out=%+v", in, out)
	}
	if len(out.Players) != len(in.Players) {
		t.Fatalf("players list mismatch: in=%v out=%v", in.Players, out.Players)
	}
	for i := range in.Players {
		if out.Players[i] != in.Players[i] {
			t.Fatalf("players[%d] mismatch: in=%v out=%v", i, in.Players, out.Players)
		}
	}
	if out.IssuedAt == 0 {
		t.Fatal("IssuedAt should be auto-populated")
	}
}

func TestSignVerifyState_RoundTrip(t *testing.T) {
	s := newTestSigner(t)
	in := NavState{Action: ActScorePlayer, GameID: 12, PlayerID: 4, PostID: "post99"}
	tok, err := s.SignState(in)
	if err != nil {
		t.Fatalf("SignState: %v", err)
	}
	out, err := s.VerifyState(tok, 0)
	if err != nil {
		t.Fatalf("VerifyState: %v", err)
	}
	if out.PostID != in.PostID || out.Action != in.Action || out.GameID != in.GameID || out.PlayerID != in.PlayerID {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestVerify_TamperRejected(t *testing.T) {
	s := newTestSigner(t)
	tok, err := s.SignContext(NavState{Action: ActGameEnd, GameID: 9})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected token shape: %q", tok)
	}

	// Flip a character in the payload — without the key the MAC must fail.
	bad := flipFirstChar(parts[0]) + "." + parts[1]
	if _, err := s.VerifyContext(bad); err == nil {
		t.Fatal("expected tampered payload to be rejected")
	}

	// Flip a character in the signature.
	badSig := parts[0] + "." + flipFirstChar(parts[1])
	if _, err := s.VerifyContext(badSig); err == nil {
		t.Fatal("expected tampered signature to be rejected")
	}

	// Wrong key entirely (constant-time hmac.Equal path).
	other, _ := NewSigner("ffffffffffffffffffffffffffffffff")
	if _, err := other.VerifyContext(tok); err == nil {
		t.Fatal("expected verification under a different key to fail")
	}

	// Malformed tokens.
	for _, bad := range []string{"", "noseparator", "a.b.c", "!!!.???"} {
		if _, err := s.VerifyContext(bad); err == nil {
			t.Fatalf("expected malformed token %q to be rejected", bad)
		}
	}
}

func flipFirstChar(s string) string {
	if s == "" {
		return "x"
	}
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

func TestVerifyState_FreshnessWindow(t *testing.T) {
	s := newTestSigner(t)
	// Stale token: issued an hour ago.
	stale := NavState{Action: ActScorePlayer, IssuedAt: time.Now().Add(-time.Hour).Unix()}
	tok, err := s.SignState(stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyState(tok, 5*time.Minute); err == nil {
		t.Fatal("expected stale state to be rejected within a 5m window")
	}
	// Same token verifies fine with no window (0) — context-style.
	if _, err := s.VerifyState(tok, 0); err != nil {
		t.Fatalf("no-window verify should pass: %v", err)
	}
	// And via the context path, which must never enforce max-age.
	if _, err := s.VerifyContext(tok); err != nil {
		t.Fatalf("context verify must ignore age: %v", err)
	}
	// Fresh token within the window.
	fresh, _ := s.SignState(NavState{Action: ActScorePlayer})
	if _, err := s.VerifyState(fresh, 5*time.Minute); err != nil {
		t.Fatalf("fresh state should verify: %v", err)
	}
}
