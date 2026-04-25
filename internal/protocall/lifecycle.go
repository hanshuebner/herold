package protocall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Signaling-frame kinds carried inside SignalPayload.Kind. The values
// match the wire vocabulary documented in
// docs/architecture/08-chat.md and docs/requirements/15-video-calls.md.
const (
	SignalKindOffer        = "offer"
	SignalKindAnswer       = "answer"
	SignalKindIceCandidate = "ice-candidate"
	SignalKindHangup       = "hangup"
)

// System-message kinds emitted by the lifecycle path. Persisted on
// the conversation as a system chat message; the JMAP message-state
// path propagates them.
const (
	SystemMessageCallStarted = "call.started"
	SystemMessageCallEnded   = "call.ended"
)

// Error codes returned to the caller via Broadcaster as a
// {kind:"error", ...} server envelope. Stable strings; clients match
// on them to surface specific messages ("recipient is offline",
// "group calls aren't supported yet").
const (
	ErrCodeRecipientOffline      = "recipient_offline"
	ErrCodeGroupCallsUnsupported = "group_calls_unsupported"
	ErrCodeNotMember             = "not_member"
	ErrCodeUnknownCall           = "unknown_call"
	ErrCodeInvalidPayload        = "invalid_payload"
	ErrCodeInternalError         = "internal_error"
)

// SignalPayload is the body of a chat call.signal frame. The chat
// envelope (track C) carries this nested under its Payload field.
type SignalPayload struct {
	// Kind selects the WebRTC signaling phase. One of SignalKind*.
	Kind string `json:"kind"`
	// CallID is the stable ID for the call. The server mints it on
	// the first offer if absent; clients echo it on every subsequent
	// frame for the same call.
	CallID string `json:"callId,omitempty"`
	// ConversationID is the DM the call belongs to. Required on every
	// frame so we never trust the in-memory session map alone for
	// authorization.
	ConversationID string `json:"conversationId"`
	// SDP carries the offer or answer SDP blob.
	SDP string `json:"sdp,omitempty"`
	// Candidate carries one ICE candidate string.
	Candidate string `json:"candidate,omitempty"`
	// SDPMid / SDPMLineIndex are the WebRTC-mandated ICE candidate
	// metadata fields; we forward them verbatim.
	SDPMid        string `json:"sdpMid,omitempty"`
	SDPMLineIndex int    `json:"sdpMLineIndex,omitempty"`
	// Reason is an optional free-text hangup reason.
	Reason string `json:"reason,omitempty"`
}

// ClientFrame is the chat-WebSocket envelope shape protocall
// consumes. The package keeps a local definition (rather than
// importing protochat's) so we depend only on a stable shape; the
// chat protocol's HandleSignal hook converts its concrete frame to
// this type before invoking us.
type ClientFrame struct {
	// Type is the envelope's discriminator; for protocall this is
	// always "call.signal". The chat dispatcher routes by Type.
	Type string `json:"type"`
	// Payload is the SignalPayload encoded as JSON.
	Payload json.RawMessage `json:"payload"`
}

// ServerEnvelope is the shape protocall hands back to the
// broadcaster. The chat-WebSocket write path serialises this as a
// JSON frame.
type ServerEnvelope struct {
	// Type is "call.signal" for forwarded frames, "call.error" for
	// reject responses to the originator.
	Type string `json:"type"`
	// Payload carries either a SignalPayload (forwarded) or an
	// ErrorPayload (rejected).
	Payload any `json:"payload"`
}

// ErrorPayload is the body of a "call.error" envelope.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	CallID  string `json:"callId,omitempty"`
}

// SystemMessagePayload is what we write to the conversation as the
// metadata_json column on the system message row.
type SystemMessagePayload struct {
	Kind            string `json:"kind"`
	CallID          string `json:"call_id"`
	CallerPrincipal uint64 `json:"caller_principal_id,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	EndedAt         string `json:"ended_at,omitempty"`
	DurationSeconds int64  `json:"duration_seconds,omitempty"`
	HangupReason    string `json:"hangup_reason,omitempty"`
	HangupPrincipal uint64 `json:"hangup_principal_id,omitempty"`
}

// CallSession is the in-process record protocall keeps for an
// in-flight call. It is NOT persisted; the call.started / call.ended
// system messages are the only durable record (REQ-CALL-30).
type CallSession struct {
	CallID         string
	ConversationID string
	Caller         store.PrincipalID
	Recipient      store.PrincipalID
	StartedAt      time.Time
	LastActivity   time.Time
}

// HandleSignal is the chat-protocol's call.signal handler. The chat
// dispatcher invokes it for every inbound call.signal frame, having
// already authenticated fromPrincipal off the WebSocket session
// cookie.
//
// Responses (forwarded frames or call.error replies) flow back
// through Broadcaster.Emit; HandleSignal does NOT return frames to
// the caller via the call return path. This lets the chat dispatcher
// stay synchronous: a signaling reject still reaches the caller's
// other connections without the dispatcher round-tripping through
// extra return paths.
//
// HandleSignal swallows its own errors after logging them: the chat
// session must not close on a malformed call.signal.
func (s *Server) HandleSignal(ctx context.Context, fromPrincipal store.PrincipalID, env ClientFrame) {
	if s.broadcaster == nil || s.members == nil || s.sysmsgs == nil || s.presence == nil {
		s.logger.LogAttrs(ctx, slog.LevelError,
			"protocall: HandleSignal called but server not fully wired",
			slog.Uint64("from_principal", uint64(fromPrincipal)))
		return
	}
	var payload SignalPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		s.emitError(ctx, fromPrincipal, "", ErrCodeInvalidPayload,
			"call.signal payload could not be parsed")
		return
	}
	if payload.ConversationID == "" {
		s.emitError(ctx, fromPrincipal, payload.CallID, ErrCodeInvalidPayload,
			"conversationId required")
		return
	}
	switch payload.Kind {
	case SignalKindOffer:
		s.handleOffer(ctx, fromPrincipal, payload)
	case SignalKindAnswer, SignalKindIceCandidate:
		s.handleRelay(ctx, fromPrincipal, payload)
	case SignalKindHangup:
		s.handleHangup(ctx, fromPrincipal, payload)
	default:
		s.emitError(ctx, fromPrincipal, payload.CallID, ErrCodeInvalidPayload,
			fmt.Sprintf("unsupported call.signal kind %q", payload.Kind))
	}
}

// handleOffer processes the leading offer of a call: validates 1:1,
// mints a CallID if absent, persists the call.started system
// message, and forwards the signal to the recipient.
func (s *Server) handleOffer(ctx context.Context, from store.PrincipalID, p SignalPayload) {
	members, err := s.members.ConversationMembers(ctx, p.ConversationID)
	if err != nil {
		s.logger.LogAttrs(ctx, slog.LevelWarn,
			"protocall: members lookup failed",
			slog.String("conversation", p.ConversationID),
			slog.String("err", err.Error()))
		s.emitError(ctx, from, p.CallID, ErrCodeInternalError, "")
		return
	}
	if !contains(members, from) {
		s.emitError(ctx, from, p.CallID, ErrCodeNotMember,
			"caller is not a member of the conversation")
		return
	}
	if len(members) > 2 {
		s.emitError(ctx, from, p.CallID, ErrCodeGroupCallsUnsupported,
			"group calls require an SFU; not supported in this release")
		return
	}
	if len(members) < 2 {
		s.emitError(ctx, from, p.CallID, ErrCodeInvalidPayload,
			"conversation has no recipient")
		return
	}
	recipient := otherMember(members, from)
	if !s.presence.IsOnline(recipient) {
		s.emitError(ctx, from, p.CallID, ErrCodeRecipientOffline,
			"recipient is not currently reachable")
		return
	}
	now := s.clk.Now()
	callID := p.CallID
	if callID == "" {
		callID = mintCallID(now)
		p.CallID = callID
	}
	sess := &CallSession{
		CallID:         callID,
		ConversationID: p.ConversationID,
		Caller:         from,
		Recipient:      recipient,
		StartedAt:      now,
		LastActivity:   now,
	}
	fresh := s.registerSession(sess)
	if !fresh {
		// Idempotency: client retransmitted the offer for an
		// existing call. Fresh-touch the session so the reaper
		// does not drop it mid-call, then forward — without
		// recording a duplicate call.started system message.
		s.touchSession(callID, now)
	} else {
		sysPayload := SystemMessagePayload{
			Kind:            SystemMessageCallStarted,
			CallID:          callID,
			CallerPrincipal: uint64(from),
			StartedAt:       now.UTC().Format(time.RFC3339Nano),
		}
		if buf, err := json.Marshal(sysPayload); err == nil {
			if err := s.sysmsgs.InsertChatSystemMessage(ctx, p.ConversationID, from, buf); err != nil {
				s.logger.LogAttrs(ctx, slog.LevelWarn,
					"protocall: insert call.started system message failed",
					slog.String("call_id", callID),
					slog.String("err", err.Error()))
				// Do not abort the call on a metadata failure: the
				// signaling path still works, the audit row is
				// best-effort.
			}
		}
	}
	s.forward(ctx, recipient, p)
}

// handleRelay validates membership + that the call_id is known and
// forwards answer / ice-candidate frames to the other party.
func (s *Server) handleRelay(ctx context.Context, from store.PrincipalID, p SignalPayload) {
	if p.CallID == "" {
		s.emitError(ctx, from, "", ErrCodeInvalidPayload,
			"callId required")
		return
	}
	sess, ok := s.lookupSession(p.CallID)
	if !ok {
		s.emitError(ctx, from, p.CallID, ErrCodeUnknownCall,
			"call is not in flight")
		return
	}
	if sess.ConversationID != p.ConversationID {
		s.emitError(ctx, from, p.CallID, ErrCodeInvalidPayload,
			"conversationId does not match call")
		return
	}
	if from != sess.Caller && from != sess.Recipient {
		s.emitError(ctx, from, p.CallID, ErrCodeNotMember,
			"caller is not a participant in this call")
		return
	}
	// Defence in depth: re-verify the conversation is still 1:1.
	members, err := s.members.ConversationMembers(ctx, sess.ConversationID)
	if err == nil && len(members) > 2 {
		s.emitError(ctx, from, p.CallID, ErrCodeGroupCallsUnsupported,
			"conversation grew past 2 members; call dropped")
		s.dropSession(p.CallID)
		return
	}
	target := sess.Recipient
	if from == sess.Recipient {
		target = sess.Caller
	}
	s.touchSession(p.CallID, s.clk.Now())
	s.forward(ctx, target, p)
}

// handleHangup forwards the hangup, persists a call.ended system
// message, and drops the in-flight session.
func (s *Server) handleHangup(ctx context.Context, from store.PrincipalID, p SignalPayload) {
	if p.CallID == "" {
		s.emitError(ctx, from, "", ErrCodeInvalidPayload,
			"callId required")
		return
	}
	sess, ok := s.lookupSession(p.CallID)
	if !ok {
		// Unknown call. Still emit an error so the client knows the
		// hangup was a no-op (could happen on a retransmit after we
		// already reaped the session); do not forward.
		s.emitError(ctx, from, p.CallID, ErrCodeUnknownCall,
			"call is not in flight")
		return
	}
	if from != sess.Caller && from != sess.Recipient {
		s.emitError(ctx, from, p.CallID, ErrCodeNotMember,
			"caller is not a participant in this call")
		return
	}
	now := s.clk.Now()
	target := sess.Recipient
	if from == sess.Recipient {
		target = sess.Caller
	}
	duration := int64(now.Sub(sess.StartedAt) / time.Second)
	if duration < 0 {
		duration = 0
	}
	endedPayload := SystemMessagePayload{
		Kind:            SystemMessageCallEnded,
		CallID:          sess.CallID,
		CallerPrincipal: uint64(sess.Caller),
		StartedAt:       sess.StartedAt.UTC().Format(time.RFC3339Nano),
		EndedAt:         now.UTC().Format(time.RFC3339Nano),
		DurationSeconds: duration,
		HangupReason:    p.Reason,
		HangupPrincipal: uint64(from),
	}
	if buf, err := json.Marshal(endedPayload); err == nil {
		// Append a fresh call.ended row rather than mutating
		// call.started. Append-only system messages give us an
		// immutable audit log per REQ-CALL-RECORD.
		if err := s.sysmsgs.InsertChatSystemMessage(ctx, sess.ConversationID, from, buf); err != nil {
			s.logger.LogAttrs(ctx, slog.LevelWarn,
				"protocall: insert call.ended system message failed",
				slog.String("call_id", sess.CallID),
				slog.String("err", err.Error()))
		}
	}
	s.forward(ctx, target, p)
	s.dropSession(p.CallID)
}

// emitError sends a call.error envelope back to principal via the
// broadcaster. Best-effort: a broadcaster failure is logged and
// swallowed (the client will time out client-side).
func (s *Server) emitError(ctx context.Context, principal store.PrincipalID, callID, code, message string) {
	if s.broadcaster == nil {
		return
	}
	env := ServerEnvelope{
		Type: "call.error",
		Payload: ErrorPayload{
			Code:    code,
			Message: message,
			CallID:  callID,
		},
	}
	if err := s.broadcaster.Emit(ctx, principal, env); err != nil {
		s.logger.LogAttrs(ctx, slog.LevelDebug,
			"protocall: broadcaster emit error failed",
			slog.Uint64("to", uint64(principal)),
			slog.String("code", code),
			slog.String("err", err.Error()))
	}
}

// forward emits the signaling envelope unchanged to the recipient
// principal. Returns the broadcaster's error so the caller can log
// at the appropriate site.
func (s *Server) forward(ctx context.Context, to store.PrincipalID, p SignalPayload) {
	env := ServerEnvelope{
		Type:    "call.signal",
		Payload: p,
	}
	if err := s.broadcaster.Emit(ctx, to, env); err != nil {
		s.logger.LogAttrs(ctx, slog.LevelWarn,
			"protocall: forward failed",
			slog.Uint64("to", uint64(to)),
			slog.String("call_id", p.CallID),
			slog.String("err", err.Error()))
	}
}

// registerSession stores sess under sess.CallID. Returns true if the
// insert took (a duplicate CallID returns false; the caller treats
// duplicate as an idempotent retransmit).
func (s *Server) registerSession(sess *CallSession) bool {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if _, ok := s.sessions[sess.CallID]; ok {
		return false
	}
	s.sessions[sess.CallID] = sess
	return true
}

// lookupSession returns the session for callID, or nil + false.
func (s *Server) lookupSession(callID string) (*CallSession, bool) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	sess, ok := s.sessions[callID]
	if !ok {
		return nil, false
	}
	// Return a copy so callers cannot mutate the live entry.
	cp := *sess
	return &cp, true
}

// touchSession refreshes the LastActivity timestamp on an existing
// session. No-op if callID is unknown.
func (s *Server) touchSession(callID string, t time.Time) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if sess, ok := s.sessions[callID]; ok {
		sess.LastActivity = t
	}
}

// dropSession removes callID from the in-flight map.
func (s *Server) dropSession(callID string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	delete(s.sessions, callID)
}

// SessionCount returns the number of in-flight call sessions. Test-
// only export.
func (s *Server) SessionCount() int {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	return len(s.sessions)
}

// mintCallID returns a fresh call id of the form "<unix-micros>-<hex8>".
// The microsecond timestamp orders calls; the random suffix prevents
// collisions when two callers start a call in the same microsecond.
func mintCallID(now time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a process-unique counter is overkill here:
		// rand.Read failure on a healthy system is exceptional.
		// Use a static suffix and rely on the timestamp.
		return fmt.Sprintf("%d-00000000", now.UnixMicro())
	}
	return fmt.Sprintf("%d-%s", now.UnixMicro(), hex.EncodeToString(b[:]))
}

func contains(members []store.PrincipalID, p store.PrincipalID) bool {
	for _, m := range members {
		if m == p {
			return true
		}
	}
	return false
}

func otherMember(members []store.PrincipalID, self store.PrincipalID) store.PrincipalID {
	for _, m := range members {
		if m != self {
			return m
		}
	}
	return 0
}
