package mm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MinHMACKeyBytes is the floor for the signing key. The key authenticates every
// inbound nav-state (action context + dialog state); a 256-bit (32-byte) key is
// the minimum. Generate one with: openssl rand -hex 32.
const MinHMACKeyBytes = 32

// Signing errors.
var (
	ErrBadToken     = errors.New("mm: malformed signed token")
	ErrBadSignature = errors.New("mm: HMAC signature mismatch")
	ErrStateExpired = errors.New("mm: signed dialog state expired")
	ErrShortHMACKey = errors.New("mm: HMAC_KEY too short")
)

const (
	tokenFieldSep    = "."
	tokenPartsExpect = 2
)

var tokenBase64 = base64.RawURLEncoding

// Action codes. Each former teloxide callback prefix maps to one discriminator
// stored in NavState.Action; the typed payload (game id, player id, entry id,
// page, players list) travels in the dedicated NavState fields. The central
// /action dispatcher routes a verified NavState by these codes (Step 14).
const (
	// Main menu.
	ActMenuNewGame  = "menu:new_game"
	ActMenuLoadGame = "menu:load_game"
	ActMenuStats    = "menu:stats"
	ActMenuPlayers  = "menu:players"

	// New-game setup.
	ActSetupAddNew   = "setup:add_new"
	ActSetupKnown    = "setup:known"
	ActSetupBack     = "setup:back"
	ActSetupStart    = "setup:start"
	ActSetupDisabled = "setup:disabled"
	ActPlayerRemove  = "player:remove"

	// Known-player picker.
	ActKnownAdd   = "known:add"
	ActKnownPage  = "known:page"
	ActKnownBack  = "known:back"
	ActKnownStart = "known:start"
	ActKnownNoop  = "known:noop"

	// Active game.
	ActScorePlayer    = "score:player"
	ActGameEdit       = "game:edit"
	ActGameEnd        = "game:end"
	ActGameEndConfirm = "game:end_confirm"
	ActGameEndCancel  = "game:end_cancel"

	// Edit-last confirmation.
	ActEditConfirm = "edit:confirm"
	ActEditCancel  = "edit:cancel"

	// Post-game / win-screen navigation.
	ActGameStats = "game:stats"
	ActGameNew   = "game:new"
	ActGameHome  = "game:home"

	// Load game.
	ActGameLoad = "game:load"
	ActLoadBack = "load:back"

	// Player management.
	ActMgmtRename        = "mgmt:rename"
	ActMgmtDelete        = "mgmt:delete"
	ActMgmtPage          = "mgmt:page"
	ActMgmtNoop          = "mgmt:noop"
	ActMgmtBack          = "mgmt:back"
	ActMgmtDeleteConfirm = "mgmt:delete_confirm"
	ActMgmtDeleteCancel  = "mgmt:delete_cancel"

	// Statistics.
	ActStatsPlayer     = "stats:player"
	ActStatsPage       = "stats:page"
	ActStatsBack       = "stats:back"
	ActStatsBackToList = "stats:back_to_list"
)

// NavState is the navigation/authorization state carried (signed) in an action
// `context` or a dialog `state`. JSON tags are short to keep posts lean; every
// field is optional (omitempty) so unused fields are omitted from the
// transmitted bytes.
//
// IssuedAt is audit-only for action contexts: a game's scoreboard/end buttons
// must verify for the game's whole life, so there is NO max-age on action
// contexts. Any optional freshness window applies to dialog state alone.
type NavState struct {
	Action   string  `json:"a,omitempty"`    // action discriminator (see Act* codes)
	GameID   int64   `json:"g,omitempty"`    // game id
	PlayerID int64   `json:"pl,omitempty"`   // player id
	EntryID  int64   `json:"e,omitempty"`    // score-entry id (idempotent edit-last)
	Page     int     `json:"pg,omitempty"`   // pagination page (0-based)
	Players  []int64 `json:"ps,omitempty"`   // new-game setup selection
	PostID   string  `json:"post,omitempty"` // originating post id (dialog re-render)
	IssuedAt int64   `json:"iat,omitempty"`  // unix seconds; audit-only for contexts
}

// Signer produces and verifies HMAC-SHA256 signed nav-state tokens. The key is
// held server-side only and is NEVER embedded in any post.
type Signer struct {
	key []byte
}

// NewSigner constructs a Signer, asserting the key length at construction time
// and copying the key so the caller cannot mutate it out from under us.
func NewSigner(key string) (*Signer, error) {
	if len([]byte(key)) < MinHMACKeyBytes {
		return nil, fmt.Errorf("%w: need >= %d bytes, got %d (generate one with: openssl rand -hex 32)",
			ErrShortHMACKey, MinHMACKeyBytes, len([]byte(key)))
	}
	k := make([]byte, len(key))
	copy(k, key)
	return &Signer{key: k}, nil
}

// mac computes HMAC-SHA256 over the exact payload bytes.
func (s *Signer) mac(payload []byte) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write(payload)
	return h.Sum(nil)
}

// sign marshals ns and returns "<base64(json)>.<base64(hmac)>". The HMAC covers
// the EXACT JSON bytes that are transmitted (carried base64-encoded in the
// token), so verification recomputes the MAC over the same transmitted bytes —
// there is no re-marshal and therefore no JSON-canonicalization ambiguity.
func (s *Signer) sign(ns NavState) (string, error) {
	if ns.IssuedAt == 0 {
		ns.IssuedAt = time.Now().Unix()
	}
	payload, err := json.Marshal(ns)
	if err != nil {
		return "", fmt.Errorf("mm: marshal nav-state: %w", err)
	}
	sig := s.mac(payload)
	return tokenBase64.EncodeToString(payload) + tokenFieldSep + tokenBase64.EncodeToString(sig), nil
}

// verify splits a token, recomputes the HMAC over the EXACT transmitted payload
// bytes, compares constant-time, then unmarshals. It deliberately does not
// re-encode the decoded struct (which would reintroduce canonicalization
// ambiguity); the authenticated artifact is the transmitted byte string.
func (s *Signer) verify(token string) (NavState, error) {
	var ns NavState
	parts := strings.Split(token, tokenFieldSep)
	if len(parts) != tokenPartsExpect {
		return ns, ErrBadToken
	}
	payload, err := tokenBase64.DecodeString(parts[0])
	if err != nil {
		return ns, ErrBadToken
	}
	sig, err := tokenBase64.DecodeString(parts[1])
	if err != nil {
		return ns, ErrBadToken
	}
	expected := s.mac(payload)
	if !hmac.Equal(sig, expected) {
		return ns, ErrBadSignature
	}
	if err := json.Unmarshal(payload, &ns); err != nil {
		return ns, ErrBadToken
	}
	return ns, nil
}

// SignContext signs nav-state for use in an action `context` (long-lived).
func (s *Signer) SignContext(ns NavState) (string, error) { return s.sign(ns) }

// VerifyContext verifies an action `context` token. There is deliberately NO
// max-age: a game's scoreboard/end buttons must verify for the game's life.
func (s *Signer) VerifyContext(token string) (NavState, error) { return s.verify(token) }

// SignState signs nav-state for use in a dialog `state` (browser-visible). It
// carries only the HMAC-signed target reference, never a raw secret.
func (s *Signer) SignState(ns NavState) (string, error) { return s.sign(ns) }

// VerifyState verifies a dialog `state` token. When maxAge > 0 an optional
// freshness window is enforced against IssuedAt; pass 0 to disable.
func (s *Signer) VerifyState(token string, maxAge time.Duration) (NavState, error) {
	ns, err := s.verify(token)
	if err != nil {
		return ns, err
	}
	if maxAge > 0 {
		age := time.Since(time.Unix(ns.IssuedAt, 0))
		if age > maxAge || age < -maxAge {
			return ns, ErrStateExpired
		}
	}
	return ns, nil
}
