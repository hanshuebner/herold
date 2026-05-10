-- 0046_internalize_pending_received_at_index.sql -- partial covering
-- index supporting newest-first iteration of pending rows by
-- received_at_us, tie-broken on id.
--
-- See REQ-EXTIMG-BG-INTERNAL-80..82 and the "Worker prioritisation"
-- section of docs/design/server/requirements/23-extimg-background-internalize.md.
--
-- Background: the internalize-worker drains rows with
-- internalize_pending = 1 newest-first so freshly-arrived mail is
-- rewritten before older archive entries. The original REQ-EXTIMG-BG-03
-- ordered by message id DESC; for live SMTP delivery message-id and
-- received-at are correlated and the rule did what its rationale said.
-- For a one-shot bulk import the two are anti-correlated (a 2026-05-08
-- receipt with id=2156 was queued behind 60k+ years-old archive rows
-- whose ids are higher than the freshly-imported 2018 mail), so the
-- worker processed the wrong rows first.
--
-- REQ-EXTIMG-BG-INTERNAL-80 redefines "newest" as descending
-- received_at_us, tie-broken on descending id. This index supports the
-- new ListMessagesWithInternalizePendingByReceivedAt scan
-- (REQ-EXTIMG-BG-INTERNAL-81) without a sequential scan over messages.
--
-- The cost: one index-row per pending message, dropped as the flag
-- clears. At the maintainer's 62k-pending peak the index is a few MB;
-- at a hypothetical future 10M-row backlog the index is on the order
-- of hundreds of MB and worth re-evaluating then.
--
-- Forward-only. Mirrors storepg 0046.
--
-- Note: the existing column-level partial-index strategy from earlier
-- migrations (none yet, but the pre-condition the worker assumed) was
-- a sequential scan with the WHERE filter; this migration replaces it
-- with an index walk. The worker's legacy id-ordered method
-- ListMessagesWithInternalizePending stays for tests that need
-- deterministic id ordering.

CREATE INDEX idx_messages_internalize_pending_received_at
  ON messages(principal_id, received_at_us DESC, id DESC)
  WHERE internalize_pending = 1;
