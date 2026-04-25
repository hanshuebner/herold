// Package thread implements the JMAP Thread datatype handlers per
// RFC 8621 §8: Thread/get and Thread/changes.
//
// Threads are derived from Email rows; there is no first-class Thread
// table. The threading algorithm is JWZ as described in RFC 5256 §A
// (REFERENCES) plus the Subject-collapse rules in RFC 5256 §2.1: two
// messages are placed in the same thread when one's References /
// In-Reply-To chain reaches the other's Message-ID, or when their
// normalised Subject (Subject minus leading "Re:" / "Fwd:" runs)
// matches and one is in the other's reply path.
//
// The store carries a per-message ThreadID slot (see store.Message);
// when present it is authoritative. When absent, this package computes
// a thread id deterministically from the message envelope: the lexically
// smallest Message-ID across the message and any reachable parents
// hashed into a uint64 — JWZ guarantees that two messages in the same
// thread agree on this value, so the derived id is stable across
// independent calls.
package thread
