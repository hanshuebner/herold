// Package protochat implements the chat ephemeral channel
// (REQ-CHAT-40..46), a WebSocket surface served at /chat/ws.
//
// Scope. This is the third chat transport — JMAP carries durable
// state, EventSource pushes durable state-change notifications, and
// the WebSocket here carries ephemeral signals: typing indicators,
// presence, and WebRTC call signaling. These signals never persist;
// they have no JMAP backing and never appear in /Foo/changes.
//
// First-party WebSocket framing. This is the first WebSocket surface
// herold ships. Per STANDARDS §3 (dependency budget) we implement RFC
// 6455 framing ourselves rather than pull a library. The codec is
// roughly 300 lines and handles the opcodes we use (text, binary,
// close, ping, pong) plus the mandatory client-mask validation.
//
// Auth. The suite session cookie authenticates the upgrade. Anonymous
// upgrades are rejected with 401 — there is no separate token
// negotiation.
//
// Concurrency. Each accepted connection runs two bounded goroutines:
// a read-pump consuming inbound frames and a write-pump that drains a
// per-connection bounded queue. A slow client triggers a close with
// code 1011 ("server condition prevented fulfilling request") rather
// than blocking the broadcaster. Connection caps and per-principal
// caps bound the surface against resource exhaustion.
//
// Fanout. The Broadcaster is a small in-process pub-sub keyed on
// principal id. Track D's video-call signaling shares it: when a
// caller posts a call.signal frame, the broadcaster forwards it to
// the targeted principal's connections. Membership validation is
// pluggable so this package compiles and tests against the in-memory
// fakes the JMAP chat store (track B) lands later.
package protochat
