# 14 — Mailing lists

How herold hosts a mailing list: where list expansion hooks into the inbound
path, how fan-out copies are shaped without breaking blob dedup, and how bounces
return and drive suspension. Requirements: `../requirements/28-mailing-lists.md`
(`REQ-MLIST-`). This is a sketch of the seams, not a line-by-line design.

## Component placement

A list is a Group principal (`../requirements/02-identity-and-auth.md`) plus
list configuration and a roster. The list logic is a new package,
`internal/maillist`, that plugs into three existing seams and adds one worker:

```
  inbound: accept ─► resolve recipient ─► [list? expand] ─► filter ─► deliver
                          (directory)         maillist

  outbound: enqueue N (message, member) rows ─► queue ─► delivery workers
                     maillist render/seal          (04-queue-and-delivery)

  bounce:  inbound to VERP token ─► [route to bounce processor] ─► score/suspend
                                        maillist.bounce

  public:  /lists/{list}/{subscribe,unsubscribe,confirm}  (public listener)
                     maillist.subscribe
```

Nothing here needs a new listener or a new queue. Expansion reuses inbound
routing, fan-out reuses the outbound queue, and bounces reuse the inbound path
(DSNs already return that way — `04-queue-and-delivery.md` §DSN).

## Expansion (inbound)

The directory already answers "resolve group -> members"
(`01-system-overview.md`). List expansion sits at the same point in the inbound
straight-line path (`03-mail-flow.md` REQ-FLOW-30..34): when a recipient
resolves to a list address, `internal/maillist` streams the list's `active`,
`each`-mode members from the roster and produces one fan-out target per member,
subject to the existing recursion-depth limit (REQ-FLOW-100). Before expanding,
the loop and abuse guards run (REQ-MLIST-30..32): drop if the message already
carries this list's `List-ID`, reject if `Auto-Submitted` is not `no`, reject if
oversize.

Expansion is deliberately at delivery time, not a stored materialised list, so a
roster edit takes effect on the next message with no fan-in state to migrate.

## Fan-out shaping without touching the blob

The persisted message blob is shared across all copies (blob dedup, REQ-FLOW-30).
Per-copy variation is applied at **enqueue/render** time, mirroring the synthetic
`X-Herold-Recipient` header pattern (REQ-FLOW-34) — the blob is never rewritten:

- **`List-*` headers** (REQ-MLIST-20) are computed from list config once and
  prepended at render.
- **ARC seal** (REQ-MLIST-21) is produced by `mailarc` over the rendered
  message, preserving the original `From:` and the poster's DKIM. This is the
  one CPU cost that scales with roster size; it is done in the enqueue worker,
  not on the inbound acceptance path, so acceptance latency is unaffected.
- **Subject tag** (REQ-MLIST-23), when configured, is an idempotent prefix edit
  at render.
- **VERP `MAIL FROM`** (below) is the per-member envelope sender on the queue row.

Each shaped copy becomes one `queue` row `(message_id, blob_id, sender=VERP,
recipient=member)` — exactly the queue's existing unit of work
(`04-queue-and-delivery.md`), so retries, DSN-on-failure, DKIM, and TLS policy
all apply unchanged. Large rosters enqueue incrementally streamed from the
roster (REQ-MLIST-12); copies are never all held in memory at once.

## VERP and the bounce processor

The envelope sender on every fan-out copy is a per-member VERP token
(REQ-MLIST-50):

```
  <list>+bounce-<hmac(member_id, deploy_key)>@<domain>
```

The HMAC (keyed by an `internal/secrets` deploy key) makes the token verifiable
and member-attributable without being member-enumerable. Inbound routing
recognises the `<list>+bounce-*` local-part shape for a list address and routes
it to `maillist.bounce` instead of to a mailbox (REQ-MLIST-51) — the same
inbound path DSNs already travel, just terminated at the bounce processor.

The bounce processor:

1. Verifies the HMAC, recovering `member_id`; unverifiable tokens are logged and
   dropped without attribution (REQ-MLIST-52).
2. Parses the DSN / non-delivery report to classify hard (5.x.x) vs soft
   (4.x.x). This DSN parser is a wire-format parser: it carries the full
   `../../STANDARDS.md` §8 obligations (fuzz target, deterministic tests,
   executable doc examples) — treated like the SMTP/IMAP parsers, not ad-hoc
   string matching.
3. Updates `member.bounce_score` / `last_bounce_at` under the per-list scoring
   policy (REQ-MLIST-53) and, on threshold crossing, transitions the member to
   `suspended`, writes an audit event, notifies the owner, and increments the
   suspension metrics (REQ-MLIST-54). Suspension is a store transition, not an
   in-memory flag, so it survives restart and is queryable for the operator's
   per-list bounce view.

## Subscription endpoints (public listener)

Self-service subscribe/unsubscribe (Stage 3) are unauthenticated HTTP routes on
the **public** listener (not the admin listener), guarded only by signed tokens:

- `POST /lists/{list}/subscribe` -> create `pending` member, email a signed
  double-opt-in token (REQ-MLIST-61).
- `GET/POST /lists/{list}/confirm?token=...` -> `pending` -> `active`.
- One-click `List-Unsubscribe` POST (RFC 8058, REQ-MLIST-57) and
  `POST /lists/{list}/unsubscribe` -> `unsubscribed`.

Tokens are HMAC-signed with a bounded TTL; no session is required, which is what
lets an MUA's one-click button and link-prefetch work. These share the token
machinery with the VERP bounce tokens.

## Archive mailbox and access (Stage 4)

The list's archive is a single mailbox owned by the Group principal
(REQ-MLIST-70), filed once per post via the normal blob-dedup store. Member read
access is a **read-only ACL grant** onto that mailbox (REQ-MLIST-72), consumed
by IMAP (RFC 4314 ACL), JMAP, and the Suite's member read-only mode.

The cross-principal ACL substrate this depends on is **not** list-specific: it is
the mailbox-level access-control model specified in the access-control work
(`../requirements/07-access-control.md` and its architecture note). This
document assumes that substrate and does not re-specify it — a list archive is
simply one principal's mailbox with read grants to member principals.

## Storage

Two new tables, both in the metadata store (SQLite + Postgres parity):

```
  mailing_list(
    principal_id   fk,           -- the backing Group principal
    posting_address text,
    display_name   text,
    owner_id       fk,           -- owning principal (access-control model)
    subject_tag    text null,
    arc_seal       bool default true,
    posting_policy text,         -- S1: open; later: members-only/announce/moderated
    subscribe_policy text,       -- closed | request-approval | open
    bounce_policy  json,         -- weights, window, threshold (per-list)
    archive_mailbox_id fk null   -- S4
  )

  mailing_list_member(
    list_id        fk,
    principal_id   fk null,      -- exactly one of principal_id / external_address
    external_address text null,
    state          text,         -- active | suspended | unsubscribed | pending
    delivery_mode  text,         -- each | nomail
    bounce_score   real default 0,
    last_bounce_at ts null,
    added_at ts, added_by fk
  )
```

Roster scale (hundreds of members per list) is trivial for either backend;
fan-out streams rows rather than loading the roster whole.

## Cross-references

- `../requirements/28-mailing-lists.md` — the requirements this realises.
- `03-protocol-architecture.md`, `04-queue-and-delivery.md` — the inbound path
  and outbound queue the list logic plugs into.
- `../requirements/07-access-control.md` — the mailbox/list/domain ownership and
  ACL model the archive-sharing (Stage 4) and list-ownership rows depend on.
- `../requirements/04-email-security.md` — `mailarc` ARC sealing reused on fan-out.
