// Package managedrule implements the JMAP ManagedRule datatype handlers
// (Wave 3.15 / REQ-FLT-01..31). ManagedRule is a multi-row, per-principal
// datatype: each row represents one structured filter rule that the server
// compiles into a Sieve preamble before the user's hand-written script.
//
// Capability URI: https://netzhansa.com/jmap/managed-rules
//
// Methods: ManagedRule/get, ManagedRule/query, ManagedRule/set,
// ManagedRule/changes, Thread/mute, Thread/unmute, BlockedSender/set.
//
// Two-source composition: when the set of managed rules or the user-written
// script changes, this package recompiles and persists the effective Sieve
// script via sieve.EffectiveScript. The user-written source and the compiled
// preamble are stored separately so neither side can corrupt the other.
package managedrule
