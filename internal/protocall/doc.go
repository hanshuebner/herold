// Package protocall implements 1:1 WebRTC video-call signaling and
// short-lived TURN credential minting (REQ-CALL-*).
//
// Scope. This package owns two narrow surfaces:
//
//   - HTTP. POST /api/v1/call/credentials hands an authenticated caller
//     a fresh, short-lived TURN credential pair. The credentials are
//     coturn REST-API shaped: username = "<unix-expiry>:<principal-id>",
//     password = base64(HMAC-SHA1(turn_shared_secret, username)). coturn
//     validates them inline against its `static-auth-secret` without
//     per-credential server state. See RFC 5389 §10.2 / RFC 8489 §9.2
//     for the long-term-credential mechanism.
//
//   - Signaling. WebRTC offer / answer / ICE candidate / hangup frames
//     ride the chat WebSocket envelope owned by internal/protochat as
//     "call.signal" frames. protochat dispatches each call.signal frame
//     to this package's HandleSignal; we validate membership, mint a
//     call_id on the first offer, persist a call.started system message
//     to the conversation, and forward the envelope to the other
//     party's connections through the protochat broadcaster. hangup
//     emits a call.ended system message.
//
// Group calls are explicitly out of scope: REQ-CALL-OPS Phase 2 is 1:1
// only (group calls would need an SFU). Any signaling frame whose
// conversation has more than two members is rejected with
// error{code:"group_calls_unsupported"}.
//
// Dependencies. The package consumes the chat broadcaster, system-
// message inserter, conversation-member resolver, and presence lookup
// through small interfaces — production wiring (internal/admin) adapts
// the real internal/protochat broadcaster and the metadata store; tests
// substitute in-memory fakes. See server.go for the interfaces.
//
// State. In-flight call sessions are tracked in an in-process map
// (CallID → CallSession). The map is reaped every 5 minutes; sessions
// older than 4 hours without a hangup are dropped. The system messages
// (call.started, call.ended) are the only persistent record of a call —
// per REQ-CALL-30, no media is ever stored.
package protocall
