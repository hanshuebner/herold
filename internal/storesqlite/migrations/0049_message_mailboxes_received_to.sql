-- 0049_message_mailboxes_received_to.sql — per-recipient envelope
-- annotation on the message-mailbox fan-out row (REQ-FLOW-33..35).
--
-- Each fan-out mailbox row records the envelope RCPT TO that produced
-- it (one canonicalised address). This is the address the upstream MTA
-- gave us at RCPT TO, post-alias-expansion, in the form actually
-- accepted by the local listener. The render path (Email/get,
-- IMAP FETCH BODY[], JMAP downloads) prepends a synthetic
-- X-Herold-Recipient: <received_to> header to the message body so
-- mailing-list / BCC / alias deliveries are unambiguously identifiable
-- by the reading user (REQ-FLOW-34). Outbound submissions strip this
-- header unconditionally (REQ-FLOW-35).
--
-- The column is NOT NULL with default ''. Existing rows backfill to the
-- empty string, which the render path treats as "do not inject the
-- header" — the explicit sentinel for "pre-feature / unknown".
--
-- Forward-only. Mirrors storepg 0049. Caller-side wiring (passing the
-- envelope RCPT TO through InsertMessage) is task #17; this migration
-- only widens the schema and leaves callers unchanged.

ALTER TABLE message_mailboxes
  ADD COLUMN received_to TEXT NOT NULL DEFAULT '';
