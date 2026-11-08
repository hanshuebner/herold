-- 0120_queue_authenticated_local.sql -- adds queue.authenticated_local
-- (issue #559).
--
-- authenticated_local is 1 on a queue row whose message an authenticated
-- principal submitted (JMAP EmailSubmission, SMTP submission, the HTTP send
-- API) or this server generated itself (DSN, reports, verification mail),
-- and 0 on a row that carries externally received bytes (Sieve redirect,
-- alias forwarding, mailing-list fan-out). The loopback deliverer ingests
-- only the former as an authenticated local submission; every other row is
-- ingested as inbound mail and keeps its mail-auth evaluation.
--
-- Rows queued before this migration default to 0 (inbound treatment).
--
-- Forward-only.

ALTER TABLE queue ADD COLUMN authenticated_local INTEGER NOT NULL DEFAULT 0;
