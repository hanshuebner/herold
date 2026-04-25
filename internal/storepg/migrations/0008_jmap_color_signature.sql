-- 0008_jmap_color_signature.sql — JMAP Mailbox.color (REQ-PROTO-56 /
-- REQ-STORE-34), Identity.signature (REQ-PROTO-57 / REQ-STORE-35),
-- and the JMAPStates.Sieve counter for the JMAP Sieve datatype
-- (REQ-PROTO-53 / RFC 9007). Mirrors storesqlite 0008. Forward-only.

ALTER TABLE mailboxes      ADD COLUMN color_hex   TEXT
  CHECK (color_hex IS NULL OR color_hex ~ '^#[0-9A-Fa-f]{6}$');
ALTER TABLE jmap_identities ADD COLUMN signature  TEXT;
ALTER TABLE jmap_states    ADD COLUMN sieve_state BIGINT NOT NULL DEFAULT 0;
