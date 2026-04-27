package protocall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Signaling-frame kinds carried inside SignalPayload.Kind. The values
// match the wire vocabulary documented in
// docs/design/server/architecture/08-chat.md and docs/design/server/requirements/15-video-calls.md.
const (
	SignalKindOffer        = "offer"
	SignalKindAnswer       = "answer"
	SignalKindIceCandidate = "ice-candidate"
	SignalKindHangup       = "hangup"
	// SignalKindDecline is emitted by the recipient to refuse an
	// inbound call without ever answering it. The dispatcher tears
	// down the session and writes a call.ended sysmsg with
	// disposition="declined" (REQ-CALL-30).
	SignalKindDecline = "decline"
	// SignalKindBusy is emitted by the SERVER to the offerer when the
	// recipient is already in another call (REQ-CALL-43). Clients do
	// not send this kind; if they do, the dispatcher rejects it as
	// an invalid payload.
	SignalKindBusy = "busy"
	// SignalKindTimeout is emitted by the SERVER to the offerer when
	// the ring timer fires before the recipient answers (REQ-CALL-06).
	// Clients do not send this kind.
	SignalKindTimeout = "timeout"
)

// System-message kinds emitted by the lifecycle path. Persisted on
// the conversation as a system chat message; the JMAP message-state
// path propagates them.
const (
	SystemMessageCallStarted = "call.started"
	SystemMessageCallEnded   = "call.ended"
)

// Disposition values for the call.ended system message payload
// (REQ-CALL-30). The disposition tags the conversation history with
// the outcome of the call so clients can render "answered N s",
// "missed", "declined" without re-deriving from frame counts.
const (
	// DispositionCompleted is the "rang, was answered, was hung up"
	// path: a call that progressed past answer to hangup.
	DispositionCompleted = "completed"
	// DispositionMissed covers calls that rang out (timeout) or hit a
	// recipient already on another call (busy).
	DispositionMissed = "missed"
	// DispositionDeclined is set when the recipient explicitly
	// rejected an inbound call before answering.
	DispositionDeclined = "declined"
	// DispositionTimeout is set by the reaper when an in-flight
	// session is dropped because its last activity is older than
	// callSessionTTL. Distinct from the ring-timeout missed-call path:
	// this is the "we lost track of the session" case.
	DispositionTimeout = "timeout"
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
// metadata_json column on the system message row. The "started"
// payload omits Disposition (the call has not ended yet); every
// "ended" payload — completed, declined, missed, timeout — sets it
// to one of the Disposition* constants (REQ-CALL-30).
type SystemMessagePayload struct {
	Kind            string `json:"kind"`
	CallID          string `json:"call_id"`
	CallerPrincipal uint64 `json:"caller_principal_id,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	EndedAt         string `json:"ended_at,omitempty"`
	DurationSeconds int64  `json:"duration_seconds,omitempty"`
	HangupReason    string `json:"hangup_reason,omitempty"`
	HangupPrincipal uint64 `json:"hangup_principal_id,omitempty"`
	// Disposition tags the outcome of an "ended" call.ended row.
	// Empty on call.started; one of DispositionCompleted /
	// DispositionMissed / DispositionDeclined / DispositionTimeout
	// otherwise (REQ-CALL-30).
	Disposition string `json:"disposition,omitempty"`
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
	// answered flips to true on the first SignalKindAnswer the server
	// observes for this session (REQ-CALL-06). The ring timer checks
	// this flag before firing: an answered call must not be flagged
	// missed by a late timer.
	answered bool
	// ringTimer is the cancel handle for the ring-timeout timer
	// scheduled at offer time. Cancelled on answer / decline / hangup
	// / busy / reaper / explicit dropSession. Nil after cancel so
	// double-cancel is a no-op.
	ringTimer clock.Timer
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
	case SignalKindAnswer:
		s.handleAnswer(ctx, fromPrincipal, payload)
	case SignalKindIceCandidate:
		s.handleRelay(ctx, fromPrincipal, payload)
	case SignalKindHangup:
		s.handleHangup(ctx, fromPrincipal, payload)
	case SignalKindDecline:
		s.handleDecline(ctx, fromPrincipal, payload)
	case SignalKindBusy, SignalKindTimeout:
		// Server-emitted kinds. A client sending one is a protocol
		// error: refuse rather than silently forwarding state we did
		// not produce.
		s.emitError(ctx, fromPrincipal, payload.CallID, ErrCodeInvalidPayload,
			fmt.Sprintf("call.signal kind %q is server-emitted only", payload.Kind))
	default:
		s.emitError(ctx, fromPrincipal, payload.CallID, ErrCodeInvalidPayload,
			fmt.Sprintf("unsupported call.signal kind %q", payload.Kind))
	}
}

// handleOffer processes the leading offer of a call: validates 1:1,
// mints a CallID if absent, persists the call.started system
// message, schedules the ring-timeout timer, and forwards the signal
// to the recipient. REQ-CALL-43: rejects a fresh offer when either
// participant is already in another call by emitting a synthetic
// kind="busy" signal to the offerer.
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
	// Concurrent-call check (REQ-CALL-43). Probe both sides; if
	// either is already in flight on a DIFFERENT call, refuse and
	// emit kind="busy" to the offerer. Re-offer of the same callID
	// (idempotent retransmit) is handled below via registerSession's
	// fresh=false branch.
	if other, busy := s.principalInOtherCall(from, callID); busy {
		s.emitBusy(ctx, from, callID, p.ConversationID,
			fmt.Sprintf("caller already in call %q", other))
		observe.ProtocallBusyEmittedTotal.WithLabelValues("offerer_in_call").Inc()
		return
	}
	if other, busy := s.principalInOtherCall(recipient, callID); busy {
		s.emitBusy(ctx, from, callID, p.ConversationID,
			fmt.Sprintf("recipient already in call %q", other))
		observe.ProtocallBusyEmittedTotal.WithLabelValues("recipient_in_call").Inc()
		// Record this never-started call as missed in the
		// conversation history so the offerer's UI can render it
		// without a corresponding call.started.
		s.writeMissedSysmsg(ctx, p.ConversationID, from, callID, now, "busy")
		observe.ProtocallCallsEndedTotal.WithLabelValues(DispositionMissed).Inc()
		return
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
		observe.ProtocallCallsStartedTotal.Inc()
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
		// Schedule the ring-timeout timer (REQ-CALL-06). The closure
		// captures callID — never the *CallSession — so we look up
		// the live session under the mutex when the timer fires.
		// Detached context: HandleSignal's caller is gone by the
		// time the timer runs.
		timer := s.clk.AfterFunc(s.ringTimeout, func() {
			s.onRingTimeout(callID)
		})
		s.attachRingTimer(callID, timer)
	}
	s.forward(ctx, recipient, p)
}

// principalInOtherCall reports whether p is currently in a call other
// than skipCallID. Used by handleOffer to enforce REQ-CALL-43.
func (s *Server) principalInOtherCall(p store.PrincipalID, skipCallID string) (string, bool) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	id, ok := s.inflightByPrincipal[p]
	if !ok || id == skipCallID {
		return "", false
	}
	return id, true
}

// emitBusy sends a synthetic kind="busy" call.signal to to, carrying
// the callID and conversationID of the offer the server refused.
// reason rides as p.Reason for client diagnostics.
func (s *Server) emitBusy(ctx context.Context, to store.PrincipalID, callID, convID, reason string) {
	if s.broadcaster == nil {
		return
	}
	env := ServerEnvelope{
		Type: "call.signal",
		Payload: SignalPayload{
			Kind:           SignalKindBusy,
			CallID:         callID,
			ConversationID: convID,
			Reason:         reason,
		},
	}
	if err := s.broadcaster.Emit(ctx, to, env); err != nil {
		s.logger.LogAttrs(ctx, slog.LevelDebug,
			"protocall: broadcaster emit busy failed",
			slog.Uint64("to", uint64(to)),
			slog.String("call_id", callID),
			slog.String("err", err.Error()))
	}
}

// writeMissedSysmsg writes a call.ended sysmsg with
// disposition="missed" for a call that never produced a call.started
// (busy / refused-recipient paths). zero StartedAt + zero
// DurationSeconds tag the row as a never-rang call.
func (s *Server) writeMissedSysmsg(ctx context.Context, convID string, from store.PrincipalID, callID string, now time.Time, reason string) {
	if s.sysmsgs == nil {
		return
	}
	ended := SystemMessagePayload{
		Kind:            SystemMessageCallEnded,
		CallID:          callID,
		CallerPrincipal: uint64(from),
		EndedAt:         now.UTC().Format(time.RFC3339Nano),
		DurationSeconds: 0,
		HangupReason:    reason,
		Disposition:     DispositionMissed,
	}
	buf, err := json.Marshal(ended)
	if err != nil {
		return
	}
	if err := s.sysmsgs.InsertChatSystemMessage(ctx, convID, from, buf); err != nil {
		s.logger.LogAttrs(ctx, slog.LevelWarn,
			"protocall: insert missed-call system message failed",
			slog.String("call_id", callID),
			slog.String("err", err.Error()))
	}
}

// attachRingTimer stores the ring timer on the session under callID.
// Race protection: if the session has been removed (handleAnswer ran
// to completion before AfterFunc returned), the timer is stopped
// immediately and the timer slot is left nil.
func (s *Server) attachRingTimer(callID string, t clock.Timer) {
	s.sessionsMu.Lock()
	sess, ok := s.sessions[callID]
	if !ok {
		s.sessionsMu.Unlock()
		t.Stop()
		return
	}
	// Track inflight on the principal map — done here because we want
	// the offer-success path to be a single atomic update under the
	// mutex.
	s.inflightByPrincipal[sess.Caller] = callID
	s.inflightByPrincipal[sess.Recipient] = callID
	sess.ringTimer = t
	s.sessionsMu.Unlock()
}

// onRingTimeout is the ring-timer callback. If the session has been
// answered or torn down, it is a no-op; otherwise the server emits a
// synthetic kind="timeout" to the offerer, writes a missed-call
// sysmsg, and drops the session. REQ-CALL-06.
func (s *Server) onRingTimeout(callID string) {
	s.sessionsMu.Lock()
	sess, ok := s.sessions[callID]
	if !ok {
		s.sessionsMu.Unlock()
		return
	}
	if sess.answered {
		// Already answered; the timer fire raced with handleAnswer
		// and lost. Leave the session as-is.
		s.sessionsMu.Unlock()
		return
	}
	caller := sess.Caller
	convID := sess.ConversationID
	startedAt := sess.StartedAt
	delete(s.sessions, callID)
	observe.ProtocallCallsInflight.Dec()
	if s.inflightByPrincipal[sess.Caller] == callID {
		delete(s.inflightByPrincipal, sess.Caller)
	}
	if s.inflightByPrincipal[sess.Recipient] == callID {
		delete(s.inflightByPrincipal, sess.Recipient)
	}
	sess.ringTimer = nil
	s.sessionsMu.Unlock()
	observe.ProtocallRingTimeoutsTotal.Inc()
	observe.ProtocallCallsEndedTotal.WithLabelValues(DispositionMissed).Inc()

	now := s.clk.Now()
	// 1) Synthetic timeout signal to the offerer.
	if s.broadcaster != nil {
		env := ServerEnvelope{
			Type: "call.signal",
			Payload: SignalPayload{
				Kind:           SignalKindTimeout,
				CallID:         callID,
				ConversationID: convID,
				Reason:         "ring_timeout",
			},
		}
		if err := s.broadcaster.Emit(context.Background(), caller, env); err != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelDebug,
				"protocall: broadcaster emit timeout failed",
				slog.Uint64("to", uint64(caller)),
				slog.String("call_id", callID),
				slog.String("err", err.Error()))
		}
	}
	// 2) Missed-call sysmsg.
	if s.sysmsgs != nil {
		duration := int64(now.Sub(startedAt) / time.Second)
		if duration < 0 {
			duration = 0
		}
		ended := SystemMessagePayload{
			Kind:            SystemMessageCallEnded,
			CallID:          callID,
			CallerPrincipal: uint64(caller),
			StartedAt:       startedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:         now.UTC().Format(time.RFC3339Nano),
			DurationSeconds: duration,
			HangupReason:    "ring_timeout",
			Disposition:     DispositionMissed,
		}
		if buf, err := json.Marshal(ended); err == nil {
			if err := s.sysmsgs.InsertChatSystemMessage(context.Background(), convID, caller, buf); err != nil {
				s.logger.LogAttrs(context.Background(), slog.LevelWarn,
					"protocall: insert ring-timeout call.ended failed",
					slog.String("call_id", callID),
					slog.String("err", err.Error()))
			}
		}
	}
}

// handleRelay validates membership + that the call_id is known and
// forwards ice-candidate frames to the other party. Answer goes
// through handleAnswer instead so the ring timer can be cancelled and
// the session marked answered.
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

// handleAnswer is the same shape as handleRelay but cancels the ring
// timer and flips the session's answered flag (REQ-CALL-06) so a late
// timer fire is a no-op. Forwards to the offerer.
func (s *Server) handleAnswer(ctx context.Context, from store.PrincipalID, p SignalPayload) {
	if p.CallID == "" {
		s.emitError(ctx, from, "", ErrCodeInvalidPayload,
			"callId required")
		return
	}
	// Look up + mark answered + cancel ring timer in one critical
	// section so the timer cannot fire between a successful answer
	// and our cancel call.
	s.sessionsMu.Lock()
	sess, ok := s.sessions[p.CallID]
	if !ok {
		s.sessionsMu.Unlock()
		s.emitError(ctx, from, p.CallID, ErrCodeUnknownCall,
			"call is not in flight")
		return
	}
	if sess.ConversationID != p.ConversationID {
		s.sessionsMu.Unlock()
		s.emitError(ctx, from, p.CallID, ErrCodeInvalidPayload,
			"conversationId does not match call")
		return
	}
	if from != sess.Caller && from != sess.Recipient {
		s.sessionsMu.Unlock()
		s.emitError(ctx, from, p.CallID, ErrCodeNotMember,
			"caller is not a participant in this call")
		return
	}
	target := sess.Recipient
	if from == sess.Recipient {
		target = sess.Caller
	}
	if !sess.answered {
		sess.answered = true
		if sess.ringTimer != nil {
			sess.ringTimer.Stop()
			sess.ringTimer = nil
		}
	}
	sess.LastActivity = s.clk.Now()
	s.sessionsMu.Unlock()
	// Defence in depth: re-verify the conversation is still 1:1.
	members, err := s.members.ConversationMembers(ctx, p.ConversationID)
	if err == nil && len(members) > 2 {
		s.emitError(ctx, from, p.CallID, ErrCodeGroupCallsUnsupported,
			"conversation grew past 2 members; call dropped")
		s.dropSession(p.CallID)
		return
	}
	s.forward(ctx, target, p)
}

// handleHangup forwards the hangup, persists a call.ended system
// message with disposition="completed" (the call rang, was answered,
// and is now ending under the participants' control), and drops the
// in-flight session.
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
	// Disposition: a hangup that races the ring window (offerer
	// presses cancel before the recipient answers) is a missed
	// call from the recipient's POV; one that follows an answer
	// is a completed call.
	disposition := DispositionCompleted
	if !sess.answered {
		disposition = DispositionMissed
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
		Disposition:     disposition,
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
	observe.ProtocallCallsEndedTotal.WithLabelValues(disposition).Inc()
	s.forward(ctx, target, p)
	s.dropSession(p.CallID)
}

// handleDecline is the recipient-emitted call-rejection path
// (REQ-CALL-30). Cancels the ring timer, writes a call.ended sysmsg
// with disposition="declined", forwards the decline to the offerer,
// and drops the session.
func (s *Server) handleDecline(ctx context.Context, from store.PrincipalID, p SignalPayload) {
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
	if from != sess.Caller && from != sess.Recipient {
		s.emitError(ctx, from, p.CallID, ErrCodeNotMember,
			"caller is not a participant in this call")
		return
	}
	// A decline only makes sense from the recipient before answer.
	// Treat caller-side decline as an invalid payload — the offerer
	// uses hangup, not decline.
	if from == sess.Caller {
		s.emitError(ctx, from, p.CallID, ErrCodeInvalidPayload,
			"only the recipient may decline a call")
		return
	}
	now := s.clk.Now()
	endedPayload := SystemMessagePayload{
		Kind:            SystemMessageCallEnded,
		CallID:          sess.CallID,
		CallerPrincipal: uint64(sess.Caller),
		StartedAt:       sess.StartedAt.UTC().Format(time.RFC3339Nano),
		EndedAt:         now.UTC().Format(time.RFC3339Nano),
		DurationSeconds: 0,
		HangupReason:    p.Reason,
		HangupPrincipal: uint64(from),
		Disposition:     DispositionDeclined,
	}
	if buf, err := json.Marshal(endedPayload); err == nil {
		if err := s.sysmsgs.InsertChatSystemMessage(ctx, sess.ConversationID, from, buf); err != nil {
			s.logger.LogAttrs(ctx, slog.LevelWarn,
				"protocall: insert decline call.ended failed",
				slog.String("call_id", sess.CallID),
				slog.String("err", err.Error()))
		}
	}
	observe.ProtocallCallsEndedTotal.WithLabelValues(DispositionDeclined).Inc()
	s.forward(ctx, sess.Caller, p)
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
	observe.ProtocallCallsInflight.Inc()
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

// dropSession removes callID from the in-flight map, cancels the
// ring timer if still pending, and prunes the per-principal inflight
// index.
func (s *Server) dropSession(callID string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	sess, ok := s.sessions[callID]
	if !ok {
		return
	}
	if sess.ringTimer != nil {
		sess.ringTimer.Stop()
		sess.ringTimer = nil
	}
	delete(s.sessions, callID)
	observe.ProtocallCallsInflight.Dec()
	if s.inflightByPrincipal[sess.Caller] == callID {
		delete(s.inflightByPrincipal, sess.Caller)
	}
	if s.inflightByPrincipal[sess.Recipient] == callID {
		delete(s.inflightByPrincipal, sess.Recipient)
	}
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
