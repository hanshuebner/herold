-- 0008_jmap_color_signature.sql — JMAP Mailbox.color (REQ-PROTO-56 /
-- REQ-STORE-34), Identity.signature (REQ-PROTO-57 / REQ-STORE-35),
-- and the JMAPStates.Sieve counter for the JMAP Sieve datatype
-- (REQ-PROTO-53 / RFC 9007). Three additive columns; forward-only.
--
-- color_hex is the JMAP-only Mailbox colour extension, NULL when
-- unset, otherwise the hex literal "#RRGGBB". The format is
-- enforced by the Go SetMailboxColor implementation; SQLite check
-- constraints would duplicate the validation without adding
-- protection (a backend bypass writes through Go anyway).
--
-- signature is the optional plain-text Identity.signature extension
-- (the existing text_signature / html_signature columns are the
-- RFC 8621 §7.1 standard properties; signature is the orthogonal
-- "extension" property the suite's compose UI consumes per
-- REQ-PROTO-57). NULL when unset.
--
-- sieve_state is the JMAPStates row's per-principal Sieve counter,
-- bumped on every successful Sieve/set call. Defaults to 0 so
-- existing rows do not need a backfill.

ALTER TABLE mailboxes      ADD COLUMN color_hex    TEXT;
ALTER TABLE jmap_identities ADD COLUMN signature   TEXT;
ALTER TABLE jmap_states    ADD COLUMN sieve_state  INTEGER NOT NULL DEFAULT 0;
