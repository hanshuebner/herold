// Package chat wires the JMAP chat datatype handlers (Conversation,
// Message, Membership, Block) into the shared CapabilityRegistry owned
// by the protojmap Core dispatcher (REQ-CHAT-01..06).
//
// Capability id is `https://netzhansa.com/jmap/chat`
// (protojmap.CapabilityJMAPChat).
//
// Standardisation deviation. Unlike the mail / contacts / calendars
// datatypes -- each anchored to a stable IETF spec or a published draft
// -- the chat datatypes are NOT standardised in any IETF JMAP RFC nor a
// public IETF draft. The herold suite ships one paired suite; per JMAP
// convention for vendor extensions (e.g. Fastmail's
// `https://www.fastmail.com/dev/maskedemail`) the capability URI is an
// http(s) URL in the vendor namespace that owns the consumer wire
// contract. Per docs/design/00-scope.md, herold is the substrate beneath the
// the suite, so the URI is `https://netzhansa.com/jmap/chat`. The suite's
// chat client knows how to consume this; future IETF standardisation
// is out of scope.
//
// Three transports model. Per architecture/08-chat.md, chat data flows
// through three transports:
//
//   - JMAP over HTTPS (this package): durable state. Conversation/*,
//     Message/*, Membership/*, Block/* method families. Carries reads,
//     mutations, search.
//   - EventSource (/jmap/eventsource): durable state-change push.
//     Already plumbed through the entity-kind-agnostic state-change
//     feed; chat datatypes flow through additively.
//   - WebSocket (/chat/ws, owned by internal/protochat): ephemeral
//     signals. Typing indicators, presence, WebRTC call signaling. Out
//     of scope for this package.
//
// Package layout. We use the simpler single-package layout (one Go
// package, multiple files) following the contacts/ and calendars/
// pattern:
//
//   - doc.go              — this file: overview + capability namespace
//     deviation note.
//   - types.go            — wire-shape Conversation, Message, Membership,
//     Block; id helpers.
//   - state.go            — JMAP state-string encoding for the four
//     datatype state counters.
//   - helpers.go          — principal/account/serverFail helpers,
//     backend lookup glue (email-by-principal, etc).
//   - backend.go          — Backend interface (the chat-store surface
//     this package depends on; satisfied by a real-store adapter in
//     production wiring and by an in-memory fake in tests).
//   - conversation.go     — Conversation/get|changes|set|query|queryChanges.
//   - message.go          — Message/get|changes|set|query|queryChanges
//     plus the custom Message/react method.
//   - membership.go       — Membership/get|changes|set|query|queryChanges
//     plus the custom Membership/setLastRead method.
//   - block.go            — Block/get|set.
//   - register.go         — Register installs every handler + the
//     per-account capability descriptor.
//   - test_helpers.go     — fixtures for the *_test.go files.
//
// Per-account capability descriptor advertises chat limits per the
// REQ-CHAT capacity envelope: maxConversationsPerAccount (default
// 10000), maxMembersPerSpace (default 1000), maxMessageBodyBytes
// (default 64 KiB), maxAttachmentsPerMessage (default 10),
// maxReactionsPerMessage (default 100).
package chat
