// Package store defines the typed repository interface used by every
// protocol handler and scheduler to read and mutate persistent state.
// Backends (SQLite, Postgres) implement this interface; the blob store and
// FTS index are separate sub-interfaces composed in.
//
// Ownership: storage-implementor. See docs/architecture/02-storage-architecture.md.
package store
