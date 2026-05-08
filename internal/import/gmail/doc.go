// Package gmail imports a Google Takeout archive (mbox + JSON settings)
// into a herold principal. Requirements live in
// docs/design/server/requirements/16-import.md (REQ-IMPORT-01..86).
//
// The package is locale-aware: Takeout artefacts are localized in the
// user's Google UI language (label values, file/dir names). The locale
// label table maps Gmail system labels in every supported locale to a
// closed set of canonical concepts (Inbox, Sent, Drafts, Spam, Trash,
// Starred, Important, Unread, Opened, the five Gmail categories);
// user-defined labels are passed through verbatim as herold mailboxes
// of role Label.
//
// The public entry point is Importer.Run. The package does not open
// the store; callers pass an already-open store.Store. The CLI glue
// in internal/admin opens the store and drives the importer.
package gmail
