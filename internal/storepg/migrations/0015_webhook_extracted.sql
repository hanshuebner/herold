-- 0015_webhook_extracted.sql — Phase 3 Wave 3.5c Track C:
-- REQ-HOOK-02 (target.kind=synthetic) + REQ-HOOK-EXTRACTED-01..03
-- (body_mode=extracted with extracted_text_max_bytes and the
-- text_required drop policy). Mirrors storesqlite 0015. Forward-only.
--
-- Three additive columns on `webhooks`:
--
--   * target_kind            -- 0 means "fall back to owner_kind"; new
--                               value 3 == synthetic.  The existing
--                               owner_kind / owner_id columns continue
--                               to carry the address|domain|principal
--                               classification for legacy rows; new
--                               rows write target_kind verbatim.
--   * body_mode              -- 0 means "fall back to delivery_mode";
--                               new value 3 == extracted.
--   * extracted_text_max_bytes  per-subscription cap on body.text;
--                               default 5 MiB, max 32 MiB enforced at
--                               the REST / CLI boundary.  Only
--                               consulted when body_mode=extracted.
--   * text_required          -- when true and body_mode=extracted, the
--                               dispatcher drops messages whose
--                               extractor result has origin=none
--                               instead of POSTing the payload.

ALTER TABLE webhooks ADD COLUMN target_kind              INTEGER NOT NULL DEFAULT 0;
ALTER TABLE webhooks ADD COLUMN body_mode                INTEGER NOT NULL DEFAULT 0;
ALTER TABLE webhooks ADD COLUMN extracted_text_max_bytes BIGINT  NOT NULL DEFAULT 0;
ALTER TABLE webhooks ADD COLUMN text_required            BOOLEAN NOT NULL DEFAULT FALSE;
