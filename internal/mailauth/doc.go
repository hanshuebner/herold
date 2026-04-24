// Package mailauth defines the shared AuthResults contract and the DNS
// resolver abstraction used by maildkim, mailspf, maildmarc, and mailarc.
//
// The package is the only surface downstream consumers (sieve, spam,
// protosmtp) import to receive an inbound authentication verdict. Every
// field on AuthResults is a typed enum: callers never parse the
// Authentication-Results wire form.
//
// Ownership: mail-auth-implementor.
package mailauth
