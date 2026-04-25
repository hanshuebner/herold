// Package backup writes a consistent backup bundle of a herold store
// to a directory the operator owns. The bundle is JSON-LINES per table
// plus a content-addressed blobs/ tree mirroring the storeblobfs
// fan-out, plus a manifest.json carrying the bundle version, the
// schema version of the source store, and per-table row counts.
//
// The bundle layout is a stable contract operators may inspect or
// hand-edit. BackupVersion in the manifest tags the format so a future
// incompatible bump can be detected at restore time.
//
// See docs/requirements/09-operations.md §Backup and Phase 2 of
// docs/implementation/02-phasing.md for the operator-facing
// requirements driving this package.
package backup
