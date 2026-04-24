// Package sdk is the Go plugin SDK used by first-party plugins. It
// implements the JSON-RPC 2.0 stdio contract documented in
// docs/architecture/07-plugin-architecture.md: handshake, configure, health,
// shutdown, plus typed helpers for DNS, spam, directory, delivery, and
// event-publisher plugin kinds.
//
// Plugins written in languages other than Go consume the JSON-RPC contract
// directly; this SDK is not a prerequisite.
//
// Ownership: plugin-platform-implementor.
package sdk
