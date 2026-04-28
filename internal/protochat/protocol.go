package protochat

import (
	"encoding/json"

	"github.com/hanshuebner/herold/internal/store"
)

// ClientFrame is the envelope every client→server message decodes
// into. The Type field selects the handler; Payload carries the
// type-specific shape (decoded lazily by the handler) and ClientID
// is the optional client-supplied correlation token the server
// echoes back in the matching ack.
type ClientFrame struct {
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	ClientID string          `json:"clientId,omitempty"`
}

// ServerFrame is the envelope every server→client message encodes
// into. Either Payload is set (typed content) or Error is set
// (failure path). Ack carries the ClientID from the originating
// ClientFrame so callers can correlate.
type ServerFrame struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Ack     string          `json:"ack,omitempty"`
	Error   *FrameError     `json:"error,omitempty"`
}

// FrameError is the error payload nested inside a ServerFrame's Error
// field. Code is a stable machine-readable token; Message is a human
// hint and may be empty.
type FrameError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Stable type tokens for the inbound (client→server) frame Types.
// The strings are part of the wire contract; reordering or renaming
// is a wire-protocol break.
const (
	clientTypeTypingStart = "typing.start"
	clientTypeTypingStop  = "typing.stop"
	clientTypePresenceSet = "presence.set"
	clientTypeSubscribe   = "subscribe"
	clientTypeUnsubscribe = "unsubscribe"
	clientTypeCallSignal  = "call.signal"
	clientTypePing        = "ping"
)

// Stable type tokens for the outbound (server→client) frame Types.
const (
	ServerTypeTyping     = "typing"
	ServerTypePresence   = "presence"
	ServerTypeRead       = "read"
	ServerTypeCallSignal = "call.signal"
	ServerTypeError      = "error"
	ServerTypeAck        = "ack"
	ServerTypePong       = "pong"
)

// Error code tokens carried in FrameError.Code. Stable wire surface;
// new codes are additive.
const (
	ErrCodeBadFrame    = "bad_frame"
	ErrCodeRateLimited = "rate_limited"
	ErrCodeNotMember   = "not_a_member"
	ErrCodeBlocked     = "blocked"
	ErrCodeUnknownType = "unknown_type"
	ErrCodeInvalid     = "invalid"
)

// typingPayload is the shape decoded from a typing.start /
// typing.stop client frame.
type typingPayload struct {
	ConversationID string `json:"conversationId"`
}

// presencePayload is the shape decoded from a presence.set frame.
type presencePayload struct {
	State string `json:"state"`
}

// subscribePayload is the shape decoded from a subscribe / unsubscribe
// frame.
type subscribePayload struct {
	ConversationIDs []string `json:"conversationIds"`
}

// callSignalPayload is the shape decoded from a call.signal frame.
// Kind selects the WebRTC verb (offer / answer / ice-candidate /
// hangup); Payload is the verb-specific body the receiver forwards
// to its peer's RTCPeerConnection unchanged.
type callSignalPayload struct {
	ConversationID string            `json:"conversationId"`
	TargetID       store.PrincipalID `json:"targetPrincipalId,omitempty"`
	Kind           string            `json:"kind"`
	Payload        json.RawMessage   `json:"payload"`
}

// outboundTyping is the shape encoded into a server→client typing
// frame. State is "start" or "stop". PrincipalID identifies the
// originating principal; it is included so that clients who receive a
// fan-out frame know who is typing without maintaining a separate
// session-ID→principal-ID mapping on their side.
type outboundTyping struct {
	ConversationID string            `json:"conversationId"`
	PrincipalID    store.PrincipalID `json:"principalId"`
	State          string            `json:"state"`
}

// outboundPresence is the shape encoded into a server→client
// presence frame.
type outboundPresence struct {
	PrincipalID store.PrincipalID `json:"principalId"`
	State       string            `json:"state"`
	LastSeenAt  int64             `json:"lastSeenAt"`
}

// outboundCallSignal mirrors the inbound call.signal but adds the
// originator's principal id so the receiver knows whom to reply to.
type outboundCallSignal struct {
	ConversationID  string            `json:"conversationId"`
	Kind            string            `json:"kind"`
	Payload         json.RawMessage   `json:"payload"`
	FromPrincipalID store.PrincipalID `json:"fromPrincipalId"`
}

// validPresenceStates is the closed set of presence states the
// protocol admits. presence.set with anything else is rejected with
// an "invalid" error frame.
var validPresenceStates = map[string]struct{}{
	"online":         {},
	"away":           {},
	"do_not_disturb": {},
	"offline":        {},
}

// makeError constructs a ServerFrame carrying just the error path.
// Helper so handlers don't repeat the boilerplate.
func makeError(code, message string, ack string) ServerFrame {
	return ServerFrame{
		Type:  ServerTypeError,
		Ack:   ack,
		Error: &FrameError{Code: code, Message: message},
	}
}

// makeAck constructs a bare ack frame.
func makeAck(ack string) ServerFrame {
	return ServerFrame{Type: ServerTypeAck, Ack: ack}
}
