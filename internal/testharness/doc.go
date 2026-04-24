// Package testharness spins up an in-process herold server with a tempdir data directory, scripted clients, fake DNS, injected SMTP peers, and injected time and randomness. Every wire test uses this harness; no test dials net directly.
//
// Ownership: conformance-fuzz-engineer.
package testharness
