# 18 — Partial mailbox replica (two-peer)

*(Added 2026-05-08 after the maintainer asked for an "edge mail
server reachable from the internet with only the most recent N days
of mail, with the full archive on a home NAS that is not internet-
reachable but is fully accessible to LAN clients." Bidirectional
sync, configured per user. After challenging the design we settled
on Shape B from that conversation: two peer mail servers — edge and
home — with the in-window subset bidirectionally replicated. Out-of-
window messages live only on the home archive. Both sides accept
client connections; clients use whichever server is reachable.)*

## Scope

Two herold installations cooperate as **edge** and **home** for the
same set of accounts:

- **edge** is on a public IP. Its MX record points at edge. SMTP
  inbound, JMAP, IMAP, suite are all reachable from the internet.
  Edge holds every account's mail for a configurable window (e.g.
  the last 14 days by received_at).
- **home** is on a private network (LAN, home NAS). It is **never**
  reachable from the public internet. It holds the full archive —
  every message that has ever existed for the account. LAN clients
  (laptops on home wifi, phones via VPN) talk to home directly with
  normal JMAP / IMAP / suite.
- A **secure tunnel** between the two — Tailscale, WireGuard, SSH
  port-forward, the operator picks — carries the replication
  traffic. The tunnel is initiated **from edge**. Home never opens
  ports on its public-side network.

Mail arrives at edge over SMTP and lands on edge first. The
replication agent ships it to home. State changes (keyword
mutations, mailbox membership, snooze, etc.) flow both ways for the
in-window subset. Old messages roll out of the edge by an explicit
GC pass and from then on live only on home.

This file does **not** specify multi-master replication across more
than two peers, nor an active-active failover topology. The edge ↔
home pair is the only supported shape; "edge can talk to N homes"
or "N edges share one home" are deliberately out of scope.

## Non-goals (explicitly)

- **Home-as-a-public-server when edge is down.** If edge is down the
  user's public-internet clients are offline. We do not promote home
  to public-IP-reachable; the operator's threat model demands home
  stays unreachable from the internet. Disaster recovery is
  "rebuild edge from a backup of home"; not "swing DNS at home."
- **Subset-by-content rules.** The window is strictly time-based on
  received_at. No "starred messages always replicate" or "messages
  from this sender always replicate" filters in v1. Those sit
  obviously next to a future iteration but are deferred.
- **Selective per-mailbox replication.** Either all of a user's
  mailboxes participate in the in-window replication or none do
  (the user opts in or out at the principal level — REQ-REPL-04).
  Per-mailbox toggles are deferred.
- **Cross-user batching.** Each user's replication state is
  independent. Two users running on the same edge do not share a
  replication stream. Operationally simpler; harder to scale to
  thousands of users on one tunnel, but a small homelab is the
  target deployment.
- **Edge ↔ edge gossip / load balancing.** Single edge per home.

## Trust model and threat surface

The user is intentionally trading a smaller privacy surface (cleartext
mail visible to whoever can compromise edge) for the convenience of
internet-reachable mail. Restating it explicitly so operators know
what they're picking:

- An attacker who compromises edge sees every message in the in-
  window subset in cleartext, plus every credential the edge holds
  (DKIM signing key, smarthost creds, JMAP API keys, OIDC client
  secrets, etc.).
- The home archive's confidentiality survives an edge compromise
  only insofar as the edge does **not** have credentials that grant
  it ad-hoc read access to home's full archive — only to its own
  in-window subset and to the replication-protocol surface.
- The replication tunnel itself uses end-to-end TLS *plus* the
  operator's chosen WireGuard/Tailscale/SSH layer. We assume the
  outer tunnel is honest and focus the replication-protocol design
  on the in-tunnel TLS layer.

## Topology and message flow

| ID | Requirement |
|----|-------------|
| REQ-REPL-01 | Mail flow on inbound: external sender → MX (edge) → edge SMTP receiver → edge inbound pipeline (spam, DKIM verify, sieve, attpol, webhooks) → edge store (canonical for first-write) → replication agent → home. Home does not have an MX record and does not receive SMTP from the internet. |
| REQ-REPL-02 | Mail flow on outbound (compose-and-send): client → submission listener (whichever side is reachable) → submitting-side queue → smarthost / direct MX. The submitting side is also the side that produces the Sent-folder copy first; the other side picks it up via replication. |
| REQ-REPL-03 | Auth: JMAP, IMAP, suite, and submission auth surfaces are independently configured on each side. A user has the same canonical_email on both sides; password / API-key / OIDC linkage is replicated as part of the principal record. (Implementation note: we already replicate principals at the storage layer — this is the same flow.) |
| REQ-REPL-04 | Per-principal opt-in. A principal is `replicated = true` on both sides or `replicated = false` on both. The default is **off**: an operator who installs herold on one box and never sets up a peer is in the same place as today. The toggle lives on the principal row; flipping it triggers a full state sync as the next replication tick. |
| REQ-REPL-05 | Configuration scope on each side names the peer with a single config block: `[replication]` block in `system.toml` selects `role = "edge"` or `role = "home"`, declares the tunnel endpoint, the shared TLS material, and the connection identity. Operators run the same binary on both sides; the role flag picks behaviour. |
| REQ-REPL-06 | Edge **always** initiates the tunnel TCP connection. Home accepts. This is non-negotiable: the operator's threat model requires home to bind no public-internet listeners. The tunnel substrate (WireGuard / Tailscale / SSH port-forward) is the operator's choice; herold does not embed one. |

## Window semantics

| ID | Requirement |
|----|-------------|
| REQ-REPL-10 | The in-window subset for an account is every message whose `received_at` is no older than `window_days` days, configured per principal. Default 14. The window is a **strict** cutoff on `received_at`; thread look-back ("an old thread got a reply, pull the whole thread back") is deferred (REQ-REPL-94). |
| REQ-REPL-11 | A message with `received_at` inside the window MUST be present on both sides (modulo the natural propagation lag — see REQ-REPL-30). A message with `received_at` outside the window is on home; it MAY be on edge for "soft expiration" leeway (REQ-REPL-13). |
| REQ-REPL-12 | The window applies per-principal. Operators MAY override the default for individual principals (e.g. an account that pays to keep 30 days online instead of 14). |
| REQ-REPL-13 | Expiration is performed by the edge's `replication-gc` pass: every M minutes (default 60), enumerate every replicated principal, find messages whose `received_at` is older than `window_days + soft_grace_hours` (default 24h grace), and verify home has acknowledged the latest changes for that message before deleting locally on edge. The grace prevents bouncing a message off the window edge as it ages. |
| REQ-REPL-14 | A message that gets a NEW mailbox-membership change on edge after rolling out of the window is **not** snapped back to edge. The edge's local copy is gone; the change is forwarded to home via the change feed. (This means "stage a flag change while a message is mid-expiration" is allowed; the change lands on home even though the body no longer exists on edge.) |
| REQ-REPL-15 | The window is documented as approximate. A user reading an in-window message and seeing it expire mid-read is acceptable degraded UX; the suite should re-fetch from home in that case (deferred, REQ-REPL-95). |

## Replication protocol surface

| ID | Requirement |
|----|-------------|
| REQ-REPL-20 | The replication channel uses an HTTP/2 connection over the operator's tunnel, terminating in a dedicated `protorepl` package on each side. Inside the tunnel the connection is mutually-authenticated TLS using a shared CA + per-side cert pair the operator generates at install time. The reuse of the existing TLS / heroldtls plumbing keeps the code surface small. |
| REQ-REPL-21 | The protocol is JSON-RPC 2.0 over a long-lived bidirectional HTTP/2 stream, with server-initiated push (home → edge) and client-initiated push (edge → home) both supported. Why HTTP/2 + JSON-RPC and not raw JMAP: the data primitives match (state cursor, change feed, blob fetch) but the security and authentication needs differ enough that we don't want to double-purpose the public JMAP surface. |
| REQ-REPL-22 | The protocol carries: change-feed deltas (one per principal, per direction), blob fetch / put (for body bytes outside the change feed), full-state snapshots (used during initial sync and after long disconnects). |
| REQ-REPL-23 | Both sides store a per-principal `replication_cursor` keyed by direction (`edge_to_home`, `home_to_edge`) tracking the last acknowledged change feed seq. Restart resumes from the cursor; never re-replays already-acked deltas. |
| REQ-REPL-24 | The protocol uses CRDT-style last-writer-wins on per-(message, mailbox) state, tagged with a hybrid logical clock (HLC) timestamp. Both sides issue HLCs from a shared monotonic-increasing seed exchanged at handshake. Conflict cases listed in REQ-REPL-40..48. |

## Identifier reconciliation

| ID | Requirement |
|----|-------------|
| REQ-REPL-30 | Each side maintains its own local `messages.id`. Messages are correlated across sides by the canonical `message_id` (RFC 5322 Message-ID) plus a content-hash fallback (`blob_hash`) for messages that lack a Message-ID or whose Message-ID is reused (the same spam-ring case the importer already handles). |
| REQ-REPL-31 | Per-mailbox UID space is **independent per side**. Edge's UID 42 in INBOX is unrelated to home's UID 42 in INBOX. The replication agent maintains a `replica_uid_map(principal_id, message_id, mailbox_id, edge_uid, home_uid)` so IMAP clients on either side see stable per-side UIDs. |
| REQ-REPL-32 | Per-mailbox MODSEQ space is independent per side. The same message has different MODSEQ values on each side. CONDSTORE clients use the side's MODSEQ; replication translates between them when sync'ing flag changes. |
| REQ-REPL-33 | JMAP `state` strings are independent per side. JMAP `Email/changes` produces correct delta vectors for the side the client is talking to; cross-side state strings are never exposed to clients. |
| REQ-REPL-34 | Mailbox identifiers are reconciled by `(principal_id, name)` rather than by id. The replication agent ensures both sides have the same set of mailbox names; ids may differ. Clients see their side's id. |

## Flag, keyword, and mailbox-membership sync

| ID | Requirement |
|----|-------------|
| REQ-REPL-40 | Per-(message, mailbox) state — flags bitfield, keywords list, snoozedUntil — is replicated bidirectionally with HLC last-writer-wins. The HLC-tagged delta encodes the value change (set / clear) plus the timestamp; the receiving side applies it iff the incoming HLC dominates the local HLC for that field. |
| REQ-REPL-41 | Keyword adds and removes are replicated as set-merge / set-diff per keyword, not as a full keyword-list overwrite. Two concurrent additions of different keywords on different sides converge cleanly. |
| REQ-REPL-42 | Mailbox-membership changes (a message moving between mailboxes) are replicated as `add (msg, mailbox)` and `remove (msg, mailbox)` events, again HLC-ordered. The receiving side applies them iff incoming HLC dominates. A concurrent move on both sides converges to the destination of whichever side issued the later HLC. |
| REQ-REPL-43 | Message creation events ("new message arrived on edge via SMTP") flow edge → home with full body. The home side stores the body and updates its own UID/MODSEQ space. |
| REQ-REPL-44 | Message destruction events ("user deleted permanently") flow either direction. The receiving side decrements blob refcount and removes the membership row, mirroring local Expunge semantics. |
| REQ-REPL-45 | Body rewrites (e.g. on-demand external-image internalization, REQ-EXTIMG-93) replicate as a `body-replace` event carrying the new blob hash. The receiving side fetches the new blob and updates its message row. |
| REQ-REPL-46 | When a flag change arrives for a message that has already rolled out of the window on the receiving side (edge), the change is applied directly to the home-side row via the open replication channel — edge does NOT need to re-fetch the body. |
| REQ-REPL-47 | Conflict resolution corner case: the same field is mutated on both sides between sync ticks, with the same HLC timestamp (collision). The deterministic tiebreak is "side with the lexicographically smaller principal ID wins"; since both sides have the same principal ID per account, the tiebreak is the side's `replication_role` ("edge" < "home"). Edge wins ties. |
| REQ-REPL-48 | Operations that are not idempotent on the underlying store (e.g. EmailSubmission/set creating a queue row) are scoped to the side that handled the original client action and are NOT replicated. The replicated state is the *result* visible in mailboxes (the Sent-folder copy that lands after the queue dispatches the message), not the intermediate queue rows. |

## Initial sync and recovery

| ID | Requirement |
|----|-------------|
| REQ-REPL-50 | When `replicated = true` is enabled for a principal that already has data on one side, the next replication tick performs a full-state sync: enumerate every in-window message, push to the peer if absent, reconcile flags / keywords / mailboxes via HLC merge. The full sync may take minutes-to-hours for an account with 100k+ in-window messages; progress is reported via a per-principal admin endpoint. |
| REQ-REPL-51 | A principal that was previously replicated and is now `replicated = false` triggers a one-shot purge on the side that no longer wants the data (the user keeps both sides; edge stops mirroring; edge runs a GC pass over the old in-window subset and removes it). |
| REQ-REPL-52 | After a long disconnect (tunnel down for hours/days, peer rebooted), reconnection restarts from the persisted replication cursor. Each side replays its outgoing change feed from the cursor; the peer applies HLC-merged. There is no "from-scratch" resync requirement — the cursors are durable. |
| REQ-REPL-53 | When a peer's persisted cursor would point at a change feed entry the other side has GC'd (state-changes table truncation, default 90 days), the affected principal falls back to a full-state sync on next reconnect. Operators are warned but do not need to intervene. |
| REQ-REPL-54 | Edge must continue accepting new SMTP-inbound mail while disconnected from home. The change feed buffers on edge; replication catches up when home reconnects. The buffer is bounded by the change_feed retention (90 days) — operators get an alert when the buffer is older than 80% of retention and they have not reconnected. |

## Outbound mail (Sent / Drafts / Submission)

| ID | Requirement |
|----|-------------|
| REQ-REPL-60 | Drafts: a draft created on either side replicates to the other. The user can resume drafting on whichever side is reachable. Conflict resolution per REQ-REPL-40 — last-writer-wins on the drafted body. |
| REQ-REPL-61 | Submission: when a user composes-and-sends, the side handling the submission queues the outbound, dispatches it through smarthost / MX, and produces the Sent-folder copy locally. The Sent copy then replicates to the other side. The non-submitting side never has a "ghost queue row" for the submission. |
| REQ-REPL-62 | DKIM signing happens on the submitting side. Both sides are configured with the same DKIM private key (or the home side is the canonical key holder and the edge side has a copy). The shared-key scenario is the simplest; future iterations may add "edge submits but home signs via the tunnel" but defer that until someone asks. |
| REQ-REPL-63 | Inbound DKIM verification verdict (REQ-EXTIMG-40) replicates as part of the message metadata. Recipients viewing a replicated message on either side see the same Authentication-Results. |
| REQ-REPL-64 | Vacation responder, sieve, attpol — every per-principal policy executes on the side that received the inbound message (i.e. edge). The output (auto-reply queued, sieve action applied, etc.) replicates the resulting state to home. There is no "policy is replicated and re-runs on home" semantic; that would double-act. |

## Operations

| ID | Requirement |
|----|-------------|
| REQ-REPL-70 | A `herold replication status` admin command summarises per-principal cursor lag, full-sync progress, conflict resolutions in the last hour, and the tunnel TCP state. |
| REQ-REPL-71 | A `herold replication resync <principal>` command forces a full-state sync for one principal. Used for surgical recovery from a known-bad replication state. |
| REQ-REPL-72 | A `herold replication peer-info` command on each side prints the peer's identity, the agreed protocol version, the last-handshake timestamp. |
| REQ-REPL-73 | Per-principal Prometheus metrics: `replication_cursor_lag_seconds{principal,direction}`, `replication_changes_applied_total{principal,direction,kind}`, `replication_conflicts_total{principal,kind}`, `replication_full_sync_in_progress{principal}` (gauge), `replication_tunnel_up` (gauge). |
| REQ-REPL-74 | Operator-visible logs name a principal_id on every replication-related line; activity = `replication`. |

## Out of scope (deferred / future iterations)

- **REQ-REPL-90** ARC-style message-modification chain when both sides
  modify a message body (e.g. extimg on both sides). Today only edge
  receives via SMTP so only edge runs extimg; this is fine. When
  inbound paths diverge (e.g. IMAP-import lands on home), the
  modification chain becomes load-bearing.
- **REQ-REPL-91** Edge-as-cache mode (Shape A from the design
  conversation): clients only talk to edge, home is a body-blob
  archive. Strictly simpler design that we deliberately did not pick;
  reserve the slot in case a future user needs it.
- **REQ-REPL-92** Multiple peers per principal (N edges or N homes).
- **REQ-REPL-93** Active-active failover.
- **REQ-REPL-94** Thread look-back: if an old thread gets a reply,
  pull the entire thread (including out-of-window ancestors) back
  into the edge's window. Better UX for long-running conversations.
- **REQ-REPL-95** Suite UX for "this message has been archived to
  home; opening it requires home reachability." Deferred until the
  edge GC actually starts producing such states.
- **REQ-REPL-96** Per-mailbox replication toggles (e.g. "replicate
  Inbox but not Spam").
- **REQ-REPL-97** Selective replication by content predicate
  ("starred messages always replicate regardless of age").
- **REQ-REPL-98** Replication of the queue (in-flight outbound
  submissions). v1 keeps queue local to the submitting side.
- **REQ-REPL-99** Home initiating the tunnel. Reserved as an explicit
  non-feature: the operator's threat model requires edge-initiated
  tunneling.

## Configuration sketch (illustrative; final shape lands at impl)

```toml
[replication]
role = "edge"                       # "edge" or "home"
peer_address = "192.0.2.50:9443"    # only meaningful when role = "edge"
                                    # (the address INSIDE the operator's
                                    # tunnel; never a public-internet IP)
peer_ca_file  = "/etc/herold/replication/peer-ca.pem"
local_cert    = "/etc/herold/replication/local-cert.pem"
local_key_ref = "$HEROLD_REPL_KEY"
window_days_default = 14
soft_grace_hours    = 24

[[replication.principal_override]]
canonical_email = "alice@example.com"
window_days     = 30
```

The operator-facing `herold admin principal replicate <email> on|off`
command flips the `replicated` flag without editing TOML.
