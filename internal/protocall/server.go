package protocall

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// MaxCredentialTTL caps the lifetime of any TURN credential the server
// hands out (REQ-CALL-22 leaves the upper bound to operator policy;
// twelve hours is the conservative ceiling herold enforces against
// misconfiguration). Larger configured TTLs are clamped to this value.
const MaxCredentialTTL = 12 * time.Hour

// DefaultCredentialTTL is applied when TURNConfig.CredentialTTL is
// zero. One hour balances refresh load on the credential endpoint
// against coturn's interest in short-lived secrets.
const DefaultCredentialTTL = time.Hour

// callRateLimitWindow / callRateLimitBurst bound the credential mint
// endpoint. Clients are expected to reuse the credential across an
// entire call, refreshing only when expiry approaches; ten requests
// per minute is comfortably above that pattern and well below abuse.
const (
	callRateLimitBurst  = 10
	callRateLimitWindow = time.Minute
)

// callSessionTTL bounds an in-flight call session in the in-process
// map. A session that has not received a hangup within this window is
// reaped on the next sweep and treated as terminated. coturn-relayed
// calls in the wild rarely exceed this; we err on the side of letting
// long calls keep state alive without leaking forever.
const callSessionTTL = 4 * time.Hour

// callReapInterval is how often the reaper sweep runs against the
// in-flight session map. Five minutes is a coarse-but-cheap cadence
// matched to callSessionTTL.
const callReapInterval = 5 * time.Minute

// Broadcaster is the outbound side-channel protocall uses to forward
// signaling frames to the recipient principal. The chat WebSocket
// (internal/protochat) implements this; tests inject a fake.
//
// Emit delivers env to every active session of the given principal.
// Implementations MUST be safe for concurrent use and SHOULD be
// non-blocking (a slow recipient must not stall the caller's signaling
// thread).
type Broadcaster interface {
	Emit(ctx context.Context, to store.PrincipalID, env ServerEnvelope) error
}

// PresenceLookup reports whether a principal currently holds any chat
// WebSocket session. The video-call invite path rejects a call to an
// offline recipient up front (REQ-CALL-OPS) so the caller's UI does
// not ring at no one.
type PresenceLookup interface {
	IsOnline(principal store.PrincipalID) bool
}

// ConversationMembers resolves the principals belonging to a given
// conversation. protocall uses this to validate the caller is a member
// and (for 1:1 calls) to discover the recipient. Implementations MUST
// return only the *current* members (left_at IS NULL) in any order.
type ConversationMembers interface {
	ConversationMembers(ctx context.Context, conversationID string) ([]store.PrincipalID, error)
}

// SystemMessageInserter persists call lifecycle markers (call.started,
// call.ended) as system messages on the conversation. The metadata
// store satisfies this in production; the call payload is opaque JSON.
type SystemMessageInserter interface {
	InsertChatSystemMessage(ctx context.Context, conversationID string, sender store.PrincipalID, payload []byte) error
}

// TURNConfig carries the operator-supplied TURN configuration. Empty
// URIs disables credential minting (the HTTP endpoint returns 503).
// SharedSecret MUST be non-empty when URIs is set, or New panics; this
// is a programmer-bug guard, not a runtime decision.
type TURNConfig struct {
	// URIs is the list of "turn:" / "turns:" URIs herold advertises
	// in mint responses. Forwarded to clients verbatim; herold does
	// not parse them.
	URIs []string
	// SharedSecret is the coturn `static-auth-secret`, used as the
	// HMAC key over username = "<expiry>:<principal>". Loaded from
	// env via sysconfig.ResolveSecret; never logged.
	SharedSecret []byte
	// CredentialTTL is the requested credential lifetime; values
	// above MaxCredentialTTL are clamped, zero falls back to
	// DefaultCredentialTTL.
	CredentialTTL time.Duration
}

// Options bundles the dependencies New consumes. Logger and Clock
// fall back to slog.Default and clock.NewReal respectively when nil.
type Options struct {
	// Logger is the structured logger; nil falls back to slog.Default.
	Logger *slog.Logger
	// Clock injects time; nil falls back to clock.NewReal.
	Clock clock.Clock
	// Broadcaster forwards signaling frames to recipients.
	Broadcaster Broadcaster
	// Members resolves conversation membership.
	Members ConversationMembers
	// SystemMessages persists call.started / call.ended markers.
	SystemMessages SystemMessageInserter
	// Presence reports whether a recipient is currently reachable.
	Presence PresenceLookup
	// TURN configures credential minting.
	TURN TURNConfig
	// Authn resolves the principal for an HTTP request. The HTTP
	// handler accepts either Bearer API keys or session cookies, both
	// of which surface here as a single resolver: returning ok=false
	// triggers a 401.
	Authn func(r *http.Request) (store.PrincipalID, bool)
}

// Server is the protocall surface. Construct with New and either
// install the HTTP handler at /api/v1/call/credentials or hand
// HandleSignal to the chat protocol as the call.signal handler.
//
// Server is safe for concurrent use.
type Server struct {
	logger      *slog.Logger
	clk         clock.Clock
	broadcaster Broadcaster
	members     ConversationMembers
	sysmsgs     SystemMessageInserter
	presence    PresenceLookup
	turn        TURNConfig
	authn       func(r *http.Request) (store.PrincipalID, bool)

	// rl gates the credential mint endpoint per principal.
	rl *rateLimiter

	// sessions tracks in-flight calls keyed by CallID. The reaper
	// goroutine drops sessions older than callSessionTTL.
	sessionsMu sync.Mutex
	sessions   map[string]*CallSession

	// reaperStop closes when Close is called; the reaper goroutine
	// observes it and returns.
	reaperOnce sync.Once
	reaperStop chan struct{}
	reaperDone chan struct{}
}

// New constructs a Server with the supplied dependencies. Missing
// optional fields (Logger, Clock) fall back to defaults; missing
// required fields (Broadcaster, Members, SystemMessages, Presence,
// Authn) cause New to return a server whose endpoints fail closed
// (the HTTP handler returns 503 / signaling rejects every frame). The
// caller is expected to assemble the full set in production wiring.
//
// New starts the reaper goroutine. Call Close to stop it.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	s := &Server{
		logger:      logger.With("subsystem", "protocall"),
		clk:         clk,
		broadcaster: opts.Broadcaster,
		members:     opts.Members,
		sysmsgs:     opts.SystemMessages,
		presence:    opts.Presence,
		turn:        opts.TURN,
		authn:       opts.Authn,
		rl:          newRateLimiter(clk, callRateLimitBurst, callRateLimitWindow),
		sessions:    make(map[string]*CallSession),
		reaperStop:  make(chan struct{}),
		reaperDone:  make(chan struct{}),
	}
	go s.reapLoop()
	return s
}

// Close stops the background reaper goroutine. Idempotent.
func (s *Server) Close() error {
	s.reaperOnce.Do(func() {
		close(s.reaperStop)
		<-s.reaperDone
	})
	return nil
}

// Logger returns the structured logger the server logs through. Test-
// only export so internal package tests can intercept events without
// reaching for slog's default sink.
func (s *Server) Logger() *slog.Logger { return s.logger }

// reapLoop drops in-flight call sessions whose last activity is older
// than callSessionTTL. Bounded by reaperStop so Close drains it.
func (s *Server) reapLoop() {
	defer close(s.reaperDone)
	for {
		select {
		case <-s.reaperStop:
			return
		case <-s.clk.After(callReapInterval):
			s.reapOnce()
		}
	}
}

// reapOnce performs a single sweep. Exposed (lower-case) so tests can
// drive a deterministic reap without waiting on the clock.
func (s *Server) reapOnce() {
	cutoff := s.clk.Now().Add(-callSessionTTL)
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	for id, sess := range s.sessions {
		if sess.LastActivity.Before(cutoff) {
			delete(s.sessions, id)
			s.logger.LogAttrs(context.Background(), slog.LevelInfo,
				"protocall: reaped stale call session",
				slog.String("call_id", id),
				slog.Time("last_activity", sess.LastActivity),
			)
		}
	}
}
