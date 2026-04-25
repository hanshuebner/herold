// Package migrate copies every row + blob from one herold store
// backend into another (typically sqlite ↔ postgres).
//
// Conceptually: backup.Snapshot on the source, restore.Sink on the
// target. The high-level Migrate function packages that orchestration
// with progress callbacks and a per-row hash check on copied blobs.
package migrate
