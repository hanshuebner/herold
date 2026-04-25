// Package restore reads a backup bundle (produced by
// internal/diag/backup) and inserts every row plus blob into a
// target store.
//
// Three modes:
//
//   - ModeFresh: target store MUST be empty; aborts on the first
//     existing row.
//   - ModeMerge: existing rows skip-on-conflict (idempotent re-restore
//     against partial state).
//   - ModeReplace: TRUNCATE-then-restore (admin uses this for full
//     recovery; cascades disabled where they would re-fire side
//     effects).
//
// Insert order respects FK dependencies (principals before aliases /
// mailboxes; mailboxes before messages; messages before
// state_changes). The order is encoded in
// internal/diag/backup.TableNames.
package restore
