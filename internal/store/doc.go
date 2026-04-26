// Package store defines the typed repository interfaces every protocol
// handler, scheduler, and admin path uses to read and mutate persistent
// state. Backends (SQLite, Postgres for the metadata repository; local
// filesystem for blobs; Bleve for FTS) live in sibling packages and
// implement these interfaces; this package holds the contract only.
//
// The surface is split into three sub-interfaces — Metadata, Blobs,
// FTS — composed under a single Store handle. See
// docs/design/architecture/02-storage-architecture.md for the architectural
// context, and STANDARDS.md §1 rule 7 for the rule that keeps the
// metadata repository typed (no Get/Put/Scan(key) surface).
//
// Ownership: storage-implementor. Backends land in Wave 1; this
// package is contract-only in Wave 0.
//
// Usage pattern (see ExampleStore):
//
//	s := storesqlite.Open(ctx, cfg)     // or storepg.Open(...)
//	defer s.Close()
//
//	p, err := s.Meta().GetPrincipalByEmail(ctx, "alice@example.com")
//	if err != nil { return err }
//
//	ref, err := s.Blobs().Put(ctx, body)
//	if err != nil { return err }
//
//	msg := store.Message{MailboxID: inboxID, Blob: ref, /* ... */}
//	uid, modseq, err := s.Meta().InsertMessage(ctx, msg)
//
// The FTS indexer runs as a background worker that polls
// FTS.ReadChangeFeedForFTS with its durable cursor and calls
// IndexMessage / RemoveMessage, then Commit on a size-or-time cadence.
package store
