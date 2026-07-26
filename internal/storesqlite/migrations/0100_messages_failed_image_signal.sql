-- 0100_messages_failed_image_signal.sql -- retryable/reason signal for
-- the failed-image badge (issue #271).
--
-- Extends the failed_image_count / failed_image_state pair added by
-- migration 0077 (issue #162) with the two values the JMAP Email
-- object needs to gate the retry affordance and explain a permanent
-- failure without decoding failed_image_state:
--
--   retryable_failed_image_count: the subset of failed_image_count
--   whose extimg.FetchOutcome.Retryable() is true -- a retry may
--   resolve it. Surfaced as Email.retryableFailedImageCount.
--
--   failed_image_reason: the highest-count permanent (non-retryable)
--   failure category, one of "blocked_by_policy" / "not_found" /
--   "unsupported" / "too_large" / "other", or '' when every failure is
--   retryable. Surfaced as Email.failedImageReason (null when empty).
--
-- Both are computed by extimg.DeriveFailureSignal from the same
-- per-outcome FailureCounts tally the internalize and retry paths
-- already produce, and are reset to 0/'' by ReplaceMessageBody
-- alongside failed_image_count / failed_image_state -- a new body
-- invalidates any previously retained state.
--
-- Column-only migration on an existing table -- no new backup/adapter
-- row type needed (mirrors migration 0077).
--
-- Back-fill: the per-outcome FailureCounts tally that would let us
-- recompute the true transient/permanent split is not retained
-- anywhere (RetainedState carries only the failed URLs), so an
-- existing row's failed_image_count cannot be re-derived offline.
-- Defaulting the new columns to 0/'' on their own would make every
-- pre-existing failed-image row look like an all-permanent failure
-- the moment the client starts gating the retry affordance on
-- retryable_failed_image_count > 0 -- silently withdrawing the retry
-- option (and today's behaviour) from mail that was already sitting
-- in a mailbox. Instead, back-fill retryable_failed_image_count to
-- failed_image_count for every existing row that has one: this
-- reproduces exactly today's "offer retry whenever failedImageCount >
-- 0" behaviour (failed_image_reason stays '' / null, since we cannot
-- honestly claim a permanent-failure category for these rows). A
-- message picks up the accurate transient/permanent split as soon as
-- it is re-internalized or retried, both of which recompute the two
-- columns via extimg.DeriveFailureSignal.
--
-- Forward-only. Mirrors storepg 0100.

ALTER TABLE messages ADD COLUMN retryable_failed_image_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN failed_image_reason TEXT NOT NULL DEFAULT '';

UPDATE messages SET retryable_failed_image_count = failed_image_count WHERE failed_image_count > 0;
