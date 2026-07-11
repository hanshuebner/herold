# 28 — Hosted mailing lists (server)

This document specifies herold operating as a **mailing-list host** — expanding
a list address to its members, injecting the RFC 2369 `List-*` headers,
handling bounces, and managing subscriptions. It is the operator-side inverse of
the client-side `../../web/requirements/16-mailing-lists.md` (which consumes
`List-*` headers on inbound mail) and `../../web/requirements/14-unsubscribe.md`
(which consumes `List-Unsubscribe`). When herold hosts a list correctly, those
two consumer surfaces light up for herold's own users against herold's own
lists: the header contract herold emits here is the same contract those docs
parse there.

Prefix `REQ-MLIST-`.

## Anchor: a list is an extended Group principal

herold already defines a **Group** principal — addressable, fans out to members,
not authenticatable (`02-identity-and-auth.md` REQ-AUTH-01/10). A hosted list is
a Group principal plus a membership roster (with per-member state and delivery
mode), a delivery/header policy, and — from Stage 4 — an archive mailbox. This
reuses address-to-principal resolution, aliases, quota, and the group's mailbox
rather than introducing a parallel object.

A list differs from a plain **alias** (`03-mail-flow.md` REQ-FLOW-100), which
re-fans the envelope RCPT TO with the body untouched, in that a list rewrites
the message on fan-out (adds `List-*` headers, ARC-seals, optionally tags the
subject), attributes and processes bounces, and owns subscription lifecycle.
Where an alias is stateless routing, a list is stateful membership.

Today's Group fans out only to internal individual principals. A list roster
also holds **arbitrary external-address members** (the hundreds-of-members case
is mostly external addresses). Members that need protocol access (Stage 4:
`nomail` + IMAP/JMAP/web read access) MUST be internal individual principals,
because they authenticate; email-only members MAY be any address.

## Incremental stages

Stage 1 is authoritative and is the minimal shippable feature — domain-admin
maintained small lists. Later stages are specified here so the data model and
header contract are chosen once, but each is delivered independently.

| Stage | Scope |
|----|----|
| **S1 (v1)** | Static admin-maintained roster; fan-out with `List-*` headers, ARC-seal, optional subject tag, loop protection; admin CRUD (REST/CLI/UI). No posting policy, no bounce automation, no self-subscribe, no archive mailbox. |
| **S2** | VERP return-path; bounce ingestion, classification, per-member scoring, monitorable auto-suspend; `List-Unsubscribe` + RFC 8058 one-click generation and token endpoint. |
| **S3** | Self-subscription with double opt-in and per-list subscription gating. |
| **S4** | One list-owned archive mailbox; `nomail` (non-delivery) membership; ACL read grants; read-only IMAP/JMAP/Suite access for members. |
| **Later** | Moderation (held posts + owner approval) with UI — the maintainer's designated **v2** milestone; optional conditional From-munge fallback. |

## Data model

| ID | Requirement |
|----|-------------|
| REQ-MLIST-01 | A **list** is backed by a Group principal and carries list-specific configuration: `posting_address` (the canonical list address), `display_name` (for `List-ID`), `owner_principal`, `subject_tag` (nullable; off by default), `arc_seal` (default on), and the stage-specific policy fields below. Stored per `05-storage.md`. |
| REQ-MLIST-02 | A **member** row is `{list_id, member_ref, state, delivery_mode, added_at, added_by, last_bounce_at, bounce_score}` where `member_ref` is **exactly one of** `principal_id` or `external_address` (canonicalised). A given address appears at most once per list. |
| REQ-MLIST-03 | Member `state` is one of `active`, `suspended`, `unsubscribed`, `pending` (awaiting double-opt-in confirmation, Stage 3). Only `active` members receive fan-out and count as recipients. |
| REQ-MLIST-04 | Member `delivery_mode` is one of `each` (a copy per message; the only mode in S1) and `nomail` (no copy; read via the archive mailbox, Stage 4). `nomail` requires `member_ref = principal_id`. Digest mode is out of scope (see Non-goals). |
| REQ-MLIST-05 | A list belongs to exactly one domain and is governed by the domain-scoped operator model (`08-admin-and-management.md` REQ-ADM-307): a domain-scoped operator manages lists only on the domains they own; a super-admin manages all. "Domain admin maintains a short list" is this model applied to lists. |

## Stage 1 — Fan-out and headers (authoritative)

### Expansion

| ID | Requirement |
|----|-------------|
| REQ-MLIST-10 | Mail to a list's `posting_address` MUST resolve to the list and expand to its `active`, `each`-mode members at fan-out time. Expansion participates in the existing fan-out path (`03-mail-flow.md` REQ-FLOW-30..34) and the recursion-depth limit (REQ-FLOW-100, default 10); a list that expands to another list is bounded by that limit. |
| REQ-MLIST-11 | The single stored message blob is shared across all fan-out copies (blob dedup, REQ-FLOW-30 preserved). Per-recipient variation (VERP return-path, `List-*` headers, ARC seal) is applied at render/enqueue time, not by rewriting the persisted blob — matching the synthetic-header pattern of REQ-FLOW-34. |
| REQ-MLIST-12 | Each fanned-out copy is enqueued through the normal outbound queue (`03-mail-flow.md` REQ-FLOW-50..76) with DKIM signing for the list's sending domain. A large roster MUST enqueue incrementally (streamed from the roster) and MUST NOT materialise all copies in memory. |

### Headers

| ID | Requirement |
|----|-------------|
| REQ-MLIST-20 | Each fanned-out copy MUST carry `List-ID: "<display_name>" <listname.domain>` and `List-Post: <mailto:posting_address>`. `List-Help`, `List-Owner`, `List-Archive`, and `List-Subscribe`/`List-Unsubscribe` are added when the corresponding facet is configured (archive from S4; subscribe/unsubscribe from S2/S3). These are exactly the headers the consumer side keys on (`../../web/requirements/16-mailing-lists.md` REQ-LIST-01). |
| REQ-MLIST-21 | The list MUST **ARC-seal** each fanned-out copy using `mailarc` (`04-email-security.md`), preserving the original `From:` and the poster's DKIM signature. The list does not rewrite `From:` in S1. This keeps replies and sender identity correct; deliverability of `p=reject` posters at receivers that do not honor ARC is addressed by the optional munge fallback (Later). |
| REQ-MLIST-22 | The bounce return-path is set per the active stage: in S1 the envelope `MAIL FROM` is the list's bounce address; from S2 it is a per-member VERP token (REQ-MLIST-40). |
| REQ-MLIST-23 | When `subject_tag` is set, the list prepends `[tag] ` to the `Subject:` of fanned-out copies if not already present (idempotent — no double-tagging on replies that already carry it). Default is unset; herold's own consumer side strips such prefixes for threading (REQ-LIST-30), so the tag exists for the benefit of non-herold subscribers. |
| REQ-MLIST-24 | Fanned-out copies MUST set `Auto-Submitted: auto-forwarded` (RFC 3834) so downstream vacation responders and DSN generators do not auto-reply to the list (consistent with `03-mail-flow.md` REQ-FLOW-90). |

### Loop and abuse protection

| ID | Requirement |
|----|-------------|
| REQ-MLIST-30 | A message already bearing this list's `List-ID` MUST NOT be re-expanded (loop guard); it is dropped with an audit record rather than re-fanned. |
| REQ-MLIST-31 | Messages with `Auto-Submitted:` other than `no` (bounces, auto-replies, other lists' fan-out) MUST NOT be posted to the list; they are rejected/dropped per posting policy, preventing DSN and vacation loops. |
| REQ-MLIST-32 | A list MUST honor a configurable maximum message size for posts, rejecting oversize posts at DATA time (reusing the `03-mail-flow.md` size-check surface) rather than fanning them out. |

### Administration

| ID | Requirement |
|----|-------------|
| REQ-MLIST-40a | Admin REST under `/api/v1/lists`: CRUD for lists; `/{list}/members` for roster CRUD and bulk import/export; scoped to the caller's managed-domain set (REQ-ADM-307). |
| REQ-MLIST-41 | CLI `herold admin list {create,delete,list,show,rename,set,member-add,member-remove,members}` mirroring the REST surface (`08-admin-and-management.md` §CLI). |
| REQ-MLIST-42 | Admin UI (operator SPA) list screens: create/edit a list, view and edit the roster, bulk add/remove members. Roster edits and list config changes produce audit records (`08-admin-and-management.md` REQ-ADM-300). |
| REQ-MLIST-43 | Metrics: `herold_mlist_fanout_total{list}`, `herold_mlist_members{list,state}`, expansion latency. Extends `09-operations.md` metrics. |

## Stage 2 — Bounce handling and membership hygiene

### VERP and bounce ingestion

| ID | Requirement |
|----|-------------|
| REQ-MLIST-50 | Each fanned-out copy's envelope `MAIL FROM` MUST be a per-member **VERP** token of the form `<list>+bounce-<hmac(member_id)>@<domain>`, so an inbound DSN is attributable to the specific member without parsing the DSN body. The HMAC key is a deployment secret (`internal/secrets`); tokens are verifiable but not member-enumerable. |
| REQ-MLIST-51 | Inbound mail to a VERP bounce token MUST route to the **bounce processor**, not to a mailbox. The processor parses the DSN (RFC 3464) / non-delivery report to classify the failure as hard (5.x.x / permanent) or soft (4.x.x / transient). The DSN parser is a wire-format parser and carries the full parser obligations of `../../STANDARDS.md` §8 (fuzz target, deterministic tests, executable doc examples). |
| REQ-MLIST-52 | A non-VERP or unverifiable bounce token MUST be handled without member attribution (logged, not applied to any member) rather than mis-attributed. |

### Scoring and auto-suspend

| ID | Requirement |
|----|-------------|
| REQ-MLIST-53 | Each classified bounce updates the member's `bounce_score` and `last_bounce_at`. The scoring policy — hard-bounce weight, soft-bounce weight, decay window, and the suspend threshold — is **configurable per list** with deployment defaults (default: one hard bounce, or soft bounces spanning more than a configured window, crosses the threshold). |
| REQ-MLIST-54 | Crossing the threshold moves the member to `suspended` (stops delivery), records an audit event, and notifies the `owner_principal`. Suspension MUST be **monitorable**: metrics (`herold_mlist_suspended_total{list,reason}`, per-list bounce rate) and an operator-visible per-list bounce/suspension view. |
| REQ-MLIST-55 | A `suspended` member is reactivated by an operator action, or (from S3) by the member re-confirming via the subscription flow. Reactivation resets `bounce_score`. |

### Unsubscribe header generation

| ID | Requirement |
|----|-------------|
| REQ-MLIST-56 | When a list enables unsubscribe, each fanned-out copy MUST carry `List-Unsubscribe` (a herold-hosted HTTPS token URL, and optionally a `mailto:`), plus `List-Unsubscribe-Post: List-Unsubscribe=One-Click` per RFC 8058. This is the producer end of `../../web/requirements/14-unsubscribe.md`. |
| REQ-MLIST-57 | A one-click unsubscribe POST to the token URL MUST unsubscribe the addressed member (set `unsubscribed`) without requiring an authenticated session, validating the signed token only. GET on the same URL renders a minimal confirmation page for MUA previews that fetch the link. |

## Stage 3 — Self-subscription

| ID | Requirement |
|----|-------------|
| REQ-MLIST-60 | Each list has a subscription policy `closed` (admin-only roster; the S1 default), `request-approval` (self-request, owner approves), or `open` (self-subscribe with confirmation). Only `open` and `request-approval` expose a public subscribe endpoint. |
| REQ-MLIST-61 | Public `POST /lists/{list}/subscribe {address}` on an `open` list MUST create a `pending` member and send a **double-opt-in** confirmation email carrying a signed token; the member becomes `active` only on confirming the token. An unconfirmed `pending` member expires after a configured TTL. No mail is delivered to a `pending` member. |
| REQ-MLIST-62 | On a `request-approval` list, subscribe creates a `pending` member surfaced to the `owner_principal` for approval; approval transitions to `active`. |
| REQ-MLIST-63 | Unsubscribe (self-service link from REQ-MLIST-56, or `POST /lists/{list}/unsubscribe`) sets `unsubscribed` and stops delivery. Re-subscribe follows the same confirmation path as a new subscription. |

## Stage 4 — Archive mailbox and non-delivery membership

| ID | Requirement |
|----|-------------|
| REQ-MLIST-70 | A list MAY own **one archive mailbox** (the Group principal's mailbox) into which every fanned-out post is filed once. This is the single "shared mailbox with list email" — not a per-member copy. Posts are stored via the normal blob-dedup path. |
| REQ-MLIST-71 | Members with `delivery_mode = nomail` receive no email copy and instead read the list via the archive mailbox. `nomail` requires the member to be an internal individual principal (REQ-MLIST-02). |
| REQ-MLIST-72 | Read access to the archive mailbox is granted to member principals via an **ACL read grant** (IMAP ACL, `internal/protoimap`). Members get read-only access; they cannot delete, expunge, or write into the archive. |
| REQ-MLIST-73 | The archive is reachable read-only through IMAP, JMAP, and a member-scoped read-only mode of the Suite ("restricted web mailer"): a member sees the list archive and can read/search it, with compose/reply available only where the member is also an `each`/`active` poster and the list posting policy permits. |
| REQ-MLIST-74 | Archive retention is configurable per list (age and/or count bound); blob GC treats live archive references as roots (consistent with the blob-reference root discipline in `05-storage.md`). |

## Later — moderation and munge fallback

| ID | Requirement |
|----|-------------|
| REQ-MLIST-80 | **Moderation (v2 milestone).** A list MAY require posts to be held for owner approval. Held posts are queued (not fanned out) until an owner approves, rejects, or discards them, with an operator/owner moderation UI. Posting policy (`members-only` / `open` / `announce-only`) is enforced at post time; non-conforming posts are rejected or held per policy. |
| REQ-MLIST-81 | **Conditional From-munge fallback.** A per-list flag, off by default, that rewrites `From:` to the list address (with `Reply-To:` the original poster) **only** for posts whose author domain publishes DMARC `p=quarantine`/`p=reject`. Intended as an operator escape hatch when the ARC-seal default (REQ-MLIST-21) is observed to bounce at major receivers; the S2 bounce metrics (REQ-MLIST-54) are the signal that justifies enabling it. |

## Non-goals

- **Digests.** No batched digest delivery mode. The `nomail` + shared-archive combination (S4) covers the "don't flood my inbox" need.
- **List archive search as a public web surface.** The archive is a normal mailbox searched through the member's client (IMAP/JMAP/Suite); herold does not publish a standalone public archive site.
- **Detecting inbound list mail** from other hosts — that is the consumer side (`../../web/requirements/16-mailing-lists.md`), not this document.

## Cross-references

- `02-identity-and-auth.md` — Group principal (REQ-AUTH-01/10) that a list extends.
- `03-mail-flow.md` — fan-out (REQ-FLOW-30..34), aliases (REQ-FLOW-100), DSN generation (REQ-FLOW-76), `Auto-Submitted` handling (REQ-FLOW-90/92).
- `04-email-security.md` — ARC sealing (`mailarc`) used on fan-out.
- `08-admin-and-management.md` — admin REST/CLI surface, audit (REQ-ADM-300), domain-scoped operators (REQ-ADM-307).
- `../../web/requirements/16-mailing-lists.md` — consumer side of the `List-*` header contract emitted here.
- `../../web/requirements/14-unsubscribe.md` — consumer side of the `List-Unsubscribe` / RFC 8058 headers generated in S2.
- `../../STANDARDS.md` §8 — parser obligations the S2 DSN/bounce parser inherits.
