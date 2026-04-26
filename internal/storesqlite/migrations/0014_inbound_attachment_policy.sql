-- 0014_inbound_attachment_policy.sql — Phase 3 Wave 3.5c Track B:
-- REQ-FLOW-ATTPOL-01 (per-recipient / per-domain inbound attachment
-- policy). Mirrors storepg 0014. Forward-only.
--
-- Two additive tables, both keyed on the lowercased canonical
-- recipient or domain. Stored as plain text rather than referencing
-- the principals / domains tables so synthetic-recipient lookups
-- (REQ-DIR-RCPT-07) and pre-provisioned domain policies do not need
-- a directory row to exist first. The lookup function (see
-- store.Metadata.GetInboundAttachmentPolicy) walks the recipient
-- row first, then falls back to the recipient's domain row, and
-- finally to the operator-overridable default ("accept").
--
-- The policy column carries one of "accept" or "reject_at_data";
-- unknown values are treated as "accept" by the lookup so a typo in
-- a manually-edited config never silently refuses every message.
-- An optional reject_text column lets an operator override the
-- 552 5.3.4 reply text within the 5.x.x family.

CREATE TABLE inbound_attpol_recipient (
  address       TEXT PRIMARY KEY,    -- lowercased "local@domain"
  policy        TEXT NOT NULL,       -- "accept" | "reject_at_data"
  reject_text   TEXT NOT NULL DEFAULT '',
  updated_at_us INTEGER NOT NULL
) STRICT;

CREATE TABLE inbound_attpol_domain (
  domain        TEXT PRIMARY KEY,    -- lowercased domain (no leading dot)
  policy        TEXT NOT NULL,       -- "accept" | "reject_at_data"
  reject_text   TEXT NOT NULL DEFAULT '',
  updated_at_us INTEGER NOT NULL
) STRICT;
