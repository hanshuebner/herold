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
-- Forward-only. Mirrors storepg 0100.

ALTER TABLE messages ADD COLUMN retryable_failed_image_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN failed_image_reason TEXT NOT NULL DEFAULT '';
