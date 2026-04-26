// Package auth carries the closed-enum scope vocabulary that gates
// every HTTP handler in the suite (REQ-AUTH-SCOPE-01..04).
//
// Cookies issued at the public-listener login flow carry the
// AllEndUserScopes set (minus webhook.publish, unless the principal is
// flagged as a webhook publisher). The admin-listener login flow
// requires a TOTP step-up for principals with 2FA enabled and issues
// a cookie carrying [ScopeAdmin] only — admin does NOT implicitly
// grant end-user (REQ-AUTH-SCOPE-02). API keys (Bearer hk_<...>) carry
// a scope set chosen at create time and immutable thereafter
// (REQ-AUTH-SCOPE-04); rotation is the only "change" path.
//
// Mechanically the scope set is enforced at handler entry via the
// RequireScope middleware. A mismatched set returns 403 with an
// RFC 7807 problem detail (NOT 401 — the caller IS authenticated,
// just not authorised for THIS scope). The boundary is defence in
// depth on top of the listener split (REQ-OPS-ADMIN-LISTENER-01..03);
// the public and admin handlers are mounted on disjoint listeners and
// the cookie names differ (herold_public_session vs
// herold_admin_session) so cookie reuse across listeners is
// mechanically impossible at the parser level.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Scope is the closed-enum auth scope. Every handler declares the
// scope it requires; cookies and API keys carry the scope set granted
// to the caller. The set is operator-extensible only via spec change
// (REQ-AUTH-SCOPE-01); drift between cookie issuance and handler
// enforcement creates auth bugs, so we keep the vocabulary small and
// deliberate.
type Scope string

const (
	// ScopeEndUser gates routes used by end users for non-mail-API
	// surfaces (call credentials, image proxy). Cookies issued at the
	// public login flow carry it; admin cookies do NOT (no implicit
	// grant per REQ-AUTH-SCOPE-02).
	ScopeEndUser Scope = "end-user"
	// ScopeAdmin gates protoadmin REST, /admin, /metrics. Issued only
	// after a TOTP step-up on the admin listener for principals with
	// 2FA enabled (REQ-AUTH-SCOPE-03).
	ScopeAdmin Scope = "admin"
	// ScopeMailSend gates the HTTP send API (/api/v1/mail/send-raw)
	// and JMAP Email/set + EmailSubmission/set.
	ScopeMailSend Scope = "mail.send"
	// ScopeMailReceive gates JMAP Email/get + Mailbox/* read paths.
	ScopeMailReceive Scope = "mail.receive"
	// ScopeChatRead gates the chat WebSocket upgrade and
	// Conversation/get + Message/query.
	ScopeChatRead Scope = "chat.read"
	// ScopeChatWrite gates chat send paths (Message/set,
	// Conversation/set typing/read marks).
	ScopeChatWrite Scope = "chat.write"
	// ScopeCalRead gates CalendarEvent/get, Calendar/get.
	ScopeCalRead Scope = "cal.read"
	// ScopeCalWrite gates CalendarEvent/set, Calendar/set.
	ScopeCalWrite Scope = "cal.write"
	// ScopeContactsRead gates Contact/get, Addressbook/get.
	ScopeContactsRead Scope = "contacts.read"
	// ScopeContactsWrite gates Contact/set, Addressbook/set.
	ScopeContactsWrite Scope = "contacts.write"
	// ScopeWebhookPublish gates inbound-webhook publishing surfaces
	// (operator-issued API keys for transactional senders that POST
	// from external services).
	ScopeWebhookPublish Scope = "webhook.publish"
)

// AllScopes is the canonical ordered slice for serialisation +
// validation. Order matters for JSON round-trip determinism (test
// fixtures compare exact strings).
var AllScopes = []Scope{
	ScopeEndUser,
	ScopeAdmin,
	ScopeMailSend,
	ScopeMailReceive,
	ScopeChatRead,
	ScopeChatWrite,
	ScopeCalRead,
	ScopeCalWrite,
	ScopeContactsRead,
	ScopeContactsWrite,
	ScopeWebhookPublish,
}

// AllEndUserScopes is the default set for human-issued cookies on the
// public listener. Excludes ScopeAdmin (admin requires TOTP step-up
// on the admin listener) and ScopeWebhookPublish (operator-issued API
// keys for transactional services hold this; humans don't).
var AllEndUserScopes = []Scope{
	ScopeEndUser,
	ScopeMailSend,
	ScopeMailReceive,
	ScopeChatRead,
	ScopeChatWrite,
	ScopeCalRead,
	ScopeCalWrite,
	ScopeContactsRead,
	ScopeContactsWrite,
}

// allScopesIndex precomputes the membership lookup map so ParseScope
// runs O(1).
var allScopesIndex = func() map[Scope]struct{} {
	m := make(map[Scope]struct{}, len(AllScopes))
	for _, s := range AllScopes {
		m[s] = struct{}{}
	}
	return m
}()

// ParseScope validates s against AllScopes and returns the matching
// canonical value. Unknown values yield ErrUnknownScope.
func ParseScope(s string) (Scope, error) {
	candidate := Scope(s)
	if _, ok := allScopesIndex[candidate]; !ok {
		return "", fmt.Errorf("auth: %w: %q", ErrUnknownScope, s)
	}
	return candidate, nil
}

// ErrUnknownScope is returned by ParseScope when the input does not
// match any value in AllScopes.
var ErrUnknownScope = errors.New("unknown scope")

// ScopeSet is a small set with O(1) membership check. The zero value
// is a usable empty set.
type ScopeSet map[Scope]struct{}

// NewScopeSet returns a set populated with scs.
func NewScopeSet(scs ...Scope) ScopeSet {
	s := make(ScopeSet, len(scs))
	for _, sc := range scs {
		s[sc] = struct{}{}
	}
	return s
}

// Has reports whether sc is in the set.
func (s ScopeSet) Has(sc Scope) bool {
	_, ok := s[sc]
	return ok
}

// HasAll reports whether every scs is in the set.
func (s ScopeSet) HasAll(scs ...Scope) bool {
	for _, sc := range scs {
		if _, ok := s[sc]; !ok {
			return false
		}
	}
	return true
}

// HasAny reports whether at least one of scs is in the set.
func (s ScopeSet) HasAny(scs ...Scope) bool {
	for _, sc := range scs {
		if _, ok := s[sc]; ok {
			return true
		}
	}
	return false
}

// Slice returns the set as a slice ordered by AllScopes (canonical
// order). Used for JSON serialisation determinism.
func (s ScopeSet) Slice() []Scope {
	out := make([]Scope, 0, len(s))
	for _, sc := range AllScopes {
		if _, ok := s[sc]; ok {
			out = append(out, sc)
		}
	}
	return out
}

// MarshalJSON emits the canonical-order slice form so cookies and
// stored rows round-trip stably.
func (s ScopeSet) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Slice())
}

// UnmarshalJSON parses a JSON array of scope strings and validates
// each entry against AllScopes.
func (s *ScopeSet) UnmarshalJSON(b []byte) error {
	var raw []string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("auth: scope set: %w", err)
	}
	out := make(ScopeSet, len(raw))
	for _, r := range raw {
		sc, err := ParseScope(r)
		if err != nil {
			return err
		}
		out[sc] = struct{}{}
	}
	*s = out
	return nil
}

// ParseScopeList parses a comma-separated list ("admin,mail.send").
// Whitespace around entries is trimmed; empty entries are an error so
// a trailing comma typo isn't silently dropped.
func ParseScopeList(spec string) ([]Scope, error) {
	if spec == "" {
		return nil, nil
	}
	parts := splitAndTrim(spec, ',')
	out := make([]Scope, 0, len(parts))
	seen := make(map[Scope]struct{}, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("auth: scope list: empty entry in %q", spec)
		}
		sc, err := ParseScope(p)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[sc]; dup {
			continue
		}
		seen[sc] = struct{}{}
		out = append(out, sc)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return canonicalIndex(out[i]) < canonicalIndex(out[j])
	})
	return out, nil
}

func canonicalIndex(s Scope) int {
	for i, c := range AllScopes {
		if c == s {
			return i
		}
	}
	return len(AllScopes)
}

// splitAndTrim splits s on sep and trims spaces around each element.
// Inlined here to avoid a strings import from a tiny call site.
func splitAndTrim(s string, sep byte) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, trimSpaces(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, trimSpaces(s[start:]))
	return out
}

func trimSpaces(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// ContextKey is the value type used to attach an AuthContext to a
// request context. Keep it package-local + an unexported struct so
// other packages can't mint a forged value.
type contextKey struct{}

// AuthContext is the per-request authentication state. The handler
// reads it via FromContext at entry and the RequireScope middleware
// asserts membership.
type AuthContext struct {
	// PrincipalID is the authenticated principal. Zero means
	// "anonymous": the handler MUST handle this case (most do not, in
	// which case the auth middleware refused to attach the value at
	// all).
	PrincipalID uint64
	// Scopes is the closed-enum scope set granted to the caller. Set
	// at cookie issuance or API key creation; immutable for the
	// request's lifetime.
	Scopes ScopeSet
	// Listener is the kind of listener that accepted the request
	// ("public" or "admin"). Carried so handlers can refuse cross-
	// listener traffic that bypassed mux routing (defence in depth).
	Listener string
}

// WithContext attaches actx to ctx. Subsequent FromContext calls
// against the returned ctx return actx.
func WithContext(ctx context.Context, actx *AuthContext) context.Context {
	return context.WithValue(ctx, contextKey{}, actx)
}

// FromContext returns the AuthContext attached to ctx, or nil if no
// auth middleware has run.
func FromContext(ctx context.Context) *AuthContext {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(contextKey{}).(*AuthContext)
	return v
}

// RequireScope returns nil iff the AuthContext attached to ctx holds
// every scope in scs. Missing AuthContext yields ErrUnauthenticated;
// missing scope yields ErrInsufficientScope. Handlers call this at
// entry and translate the error into the correct HTTP shape (401 for
// no auth, 403 for insufficient scope).
func RequireScope(ctx context.Context, scs ...Scope) error {
	actx := FromContext(ctx)
	if actx == nil {
		return ErrUnauthenticated
	}
	if !actx.Scopes.HasAll(scs...) {
		return fmt.Errorf("%w: required %v, have %v", ErrInsufficientScope, scs, actx.Scopes.Slice())
	}
	return nil
}

// Sentinel errors so callers can errors.Is() differentiate between
// "no auth at all" (401) and "wrong scope" (403).
var (
	ErrUnauthenticated   = errors.New("auth: unauthenticated")
	ErrInsufficientScope = errors.New("auth: insufficient scope")
)
