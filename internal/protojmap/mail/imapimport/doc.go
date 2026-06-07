// Package imapimport implements the JMAP IMAPImport datatype for the
// inbound IMAP live-mirror feature (REQ-IMAP-IMP-61). It provides
// IMAPImport/get and IMAPImport/set under the CapabilityIMAPImport
// capability ("https://netzhansa.com/jmap/imap-import").
//
// IMAPImport/get returns non-secret account fields; credentials are
// write-only and never returned (REQ-IMAP-IMP-70). IMAPImport/set
// supports create, update, and destroy. Credential plaintext supplied
// at create or update is sealed server-side with secrets.Seal using the
// AEAD data key (the same key the outbound submission path uses) before
// it is handed to the store layer.
//
// IMAPImport/changes is deferred: no jmap_states column exists for
// IMAPImport in migration 0057 (wave 1 frozen). Adding /changes
// requires a migration 0058 ALTER TABLE jmap_states ADD COLUMN
// imap_import_state and a new JMAPStateKindIMAPImport constant, which
// is wave-2 work.
package imapimport
