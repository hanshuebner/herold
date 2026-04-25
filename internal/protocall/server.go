package protocall

import (
	"context"
	"encoding/json"
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
// zero. Five minutes (REQ-CALL-22) keeps the credential-leak window
// short while staying comfortably above the worst-case round-trip
// time for a refresh; clients refresh ~30s before expiry.
const DefaultCredentialTTL = 5 * time.Minute

// callRateLimitWindow / callRateLimitBurst bound the credential mint
// endpoint. Clients are expected to reuse the credential across an
// entire call, refreshing only when expiry approaches; ten requests
// per minute is comfortably above that pattern and well below abuse.
const (
	callRateLimitBurst  = 10
	callRateLimitWindow = time.Minute
)

// callSessionTTL bounds an in-flight call session in the in-process
// map (REQ-CALL-07). A session whose last activity is older than this
// window is reaped, with the corresponding call.ended system message
// written under disposition="timeout". Five minutes matches the
// upper-bound the requirements ask for: longer than any signaling
// hand-off needs (an answered call's last activity refreshes on every
// ICE-candidate frame) but short enough that a stuck session does not
// leak state for hours.
const callSessionTTL = 5 * time.Minute

// callReapInterval is how often the reaper sweep runs against the
// in-flight session map. One minute is a coarse-but-cheap cadence
// matched to the 5 min callSessionTTL: a stale session is observed by
// the reaper within at most one minute past its TTL.
const callReapInterval = time.Minute

// DefaultRingTimeout is the per-call window the offerer waits for an
// answer before the server emits a synthetic kind="timeout" signal
// and writes a missed-call sysmsg (REQ-CALL-06). Thirty seconds is
// the operator default; cancellable via Server.Call.RingTimeoutSeconds.
const DefaultRingTimeout = 30 * time.Second

// MaxRingTimeout caps how long the operator can configure the ring
// window for. Five minutes mirrors callSessionTTL and prevents a
// misconfigured deployment from leaving a "ringing" state up forever.
const MaxRingTimeout = 5 * time.Minute

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
	// RingTimeout overrides the per-call ring window (REQ-CALL-06).
	// Zero falls back to DefaultRingTimeout; values above
	// MaxRingTimeout are clamped.
	RingTimeout time.Duration
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
	ringTimeout time.Duration

	// rl gates the credential mint endpoint per principal.
	rl *rateLimiter

	// sessionsMu guards both sessions and inflightByPrincipal. Holding
	// the mutex while invoking Broadcaster.Emit is permitted because
	// production broadcasters are non-blocking (REQ-CALL-OPS); tests
	// inject fakes that complete synchronously.
	sessionsMu sync.Mutex
	// sessions tracks in-flight calls keyed by CallID. The reaper
	// goroutine drops sessions older than callSessionTTL and writes a
	// disposition="timeout" call.ended sysmsg as it goes.
	sessions map[string]*CallSession
	// inflightByPrincipal maps a principal to the CallID of the one
	// active call they are participating in (REQ-CALL-43). The map is
	// populated for both caller and recipient on a successful offer
	// and pruned on hangup / decline / reaper / ring-timeout.
	inflightByPrincipal map[store.PrincipalID]string

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
	ring := opts.RingTimeout
	if ring <= 0 {
		ring = DefaultRingTimeout
	}
	if ring > MaxRingTimeout {
		ring = MaxRingTimeout
	}
	s := &Server{
		logger:              logger.With("subsystem", "protocall"),
		clk:                 clk,
		broadcaster:         opts.Broadcaster,
		members:             opts.Members,
		sysmsgs:             opts.SystemMessages,
		presence:            opts.Presence,
		turn:                opts.TURN,
		authn:               opts.Authn,
		ringTimeout:         ring,
		rl:                  newRateLimiter(clk, callRateLimitBurst, callRateLimitWindow),
		sessions:            make(map[string]*CallSession),
		inflightByPrincipal: make(map[store.PrincipalID]string),
		reaperStop:          make(chan struct{}),
		reaperDone:          make(chan struct{}),
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
// drive a deterministic reap without waiting on the clock. For each
// session whose last activity precedes the TTL cutoff the reaper:
//
//   - removes it from the in-flight maps,
//   - cancels its (now-irrelevant) ring timer if still pending,
//   - writes a call.ended system message with disposition="timeout"
//     and reason="stale" so the conversation history reflects the
//     dropped session (REQ-CALL-07).
//
// Sysmsg writes happen outside the mutex so a slow inserter does not
// stall concurrent signaling.
func (s *Server) reapOnce() {
	now := s.clk.Now()
	cutoff := now.Add(-callSessionTTL)
	type reaped struct {
		sess *CallSession
	}
	var dropped []reaped
	s.sessionsMu.Lock()
	for id, sess := range s.sessions {
		if sess.LastActivity.Before(cutoff) {
			if sess.ringTimer != nil {
				sess.ringTimer.Stop()
				sess.ringTimer = nil
			}
			delete(s.sessions, id)
			if s.inflightByPrincipal[sess.Caller] == id {
				delete(s.inflightByPrincipal, sess.Caller)
			}
			if s.inflightByPrincipal[sess.Recipient] == id {
				delete(s.inflightByPrincipal, sess.Recipient)
			}
			dropped = append(dropped, reaped{sess: sess})
		}
	}
	s.sessionsMu.Unlock()
	for _, r := range dropped {
		sess := r.sess
		s.logger.LogAttrs(context.Background(), slog.LevelInfo,
			"protocall: reaped stale call session",
			slog.String("call_id", sess.CallID),
			slog.Time("last_activity", sess.LastActivity),
		)
		if s.sysmsgs == nil {
			continue
		}
		duration := int64(now.Sub(sess.StartedAt) / time.Second)
		if duration < 0 {
			duration = 0
		}
		ended := SystemMessagePayload{
			Kind:            SystemMessageCallEnded,
			CallID:          sess.CallID,
			CallerPrincipal: uint64(sess.Caller),
			StartedAt:       sess.StartedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:         now.UTC().Format(time.RFC3339Nano),
			DurationSeconds: duration,
			HangupReason:    "stale",
			Disposition:     DispositionTimeout,
		}
		buf, err := json.Marshal(ended)
		if err != nil {
			continue
		}
		if err := s.sysmsgs.InsertChatSystemMessage(context.Background(), sess.ConversationID, sess.Caller, buf); err != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelWarn,
				"protocall: insert reaped call.ended system message failed",
				slog.String("call_id", sess.CallID),
				slog.String("err", err.Error()))
		}
	}
}
