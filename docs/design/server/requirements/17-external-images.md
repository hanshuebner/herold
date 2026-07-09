# 17 — External image internalization

*(Added 2026-05-08 after the maintainer raised the privacy / UX
trade-off of HTML mail. Today's behaviour — refuse to load external
images until the user clicks "show images" — protects privacy by
making the cost visible. The cost is a friction tax on every mail the
user reads. This document proposes that the **server** prefetch
external images at delivery time, internalize them as inline MIME
parts, and serve the rewritten body to every client. The sender's
ability to track per-open opens is removed; the user's reading
experience becomes equivalent to mail with no external references at
all. Operators who would rather keep the existing "click to load"
experience flip a single config knob.)*

## Scope

Inbound HTML mail frequently embeds external images via `<img src>`,
`srcset`, `<picture>`/`<source>`, CSS `url(...)` in `style=""`
attributes or `<style>` blocks, and SVG `<image href>` references.
Each such reference, when rendered, makes the user's mail client
fetch from the sender's CDN — leaking the user's IP, user-agent, and
"opened at" timestamp. Marketers and abusers exploit this to track
delivery and engagement; some senders ship per-recipient unique URLs
so the fetch identifies the specific user.

This file specifies how herold can prefetch those images at delivery
time and store them as inline parts of the message, so the user-side
render needs no external fetch. The sender still observes one
delivery-time fetch from the server's IP — they cannot tell when (or
if) the user opened the message, from which device, with what
user-agent.

This file does **not** cover image proxying at view time (relaying
the fetch through the operator's server when the user opens a
message). View-time proxying is strictly worse: the sender still
times the open. The internalize-at-delivery design is the only one
that meaningfully decouples delivery from open.

## Modes (operator policy)

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-01 | The operator selects one external-image policy per system, in `[external_images]` of `system.toml`: `internalize` (default) or `passthrough`. There is no per-user override; the operator's policy is the ceiling. |
| REQ-EXTIMG-02 | `internalize`: every inbound HTML message is rewritten at delivery time. External image references are fetched, the bytes are attached as new MIME parts on the message, and the references are rewritten to `cid:` URLs pointing at those parts. Both IMAP FETCH and JMAP `Email/get` return the rewritten body. |
| REQ-EXTIMG-03 | `passthrough`: the message body is stored verbatim. External references are not fetched; the suite-side "show images" gating remains the user's privacy fence. This mode preserves DKIM signatures unchanged and is the only mode that does so. |
| REQ-EXTIMG-04 | The mode is read from config on every inbound message; a SIGHUP / config reload changes behaviour for messages delivered after the reload. Already-stored messages are never re-rewritten. |
| REQ-EXTIMG-05 | The default ships as `internalize`. The maintainer's argument: most installations want privacy without the friction; operators with a high-threat-model user base flip explicitly. |

## HTML rewrite scope

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-10 | The rewriter walks the message's HTML body parts. Each `<img src>`, `srcset` (every URL in the comma-separated list), `<source srcset>`, `<source src>`, and `<image href>` (SVG, including `xlink:href` for legacy producers) is a candidate. Plain-text and non-HTML parts are passed through unchanged. |
| REQ-EXTIMG-11 | CSS `url(...)` references inside `style=""` attributes and inside `<style>` blocks are rewritten when the property is one that loads images: `background`, `background-image`, `border-image`, `list-style`, `list-style-image`, `cursor`, `mask`, `mask-image`, `content`. `@font-face src:` is **not** rewritten — fonts are higher-bandwidth and rarely worth caching, and font-foundry CDNs occasionally enforce origin checks that internalization would break. |
| REQ-EXTIMG-12 | URLs whose scheme is `cid:`, `data:`, `mid:` (RFC 2392), or relative are pass-through; only `http://` and `https://` references are candidates for fetching. |
| REQ-EXTIMG-13 | The rewriter is HTML-aware (golang.org/x/net/html), not regex-based. It must round-trip through a real parser. Malformed HTML — Outlook-grade nesting, unbalanced tags, comments inside attributes — must not crash the rewriter; on parse failure the message is stored verbatim and the policy effectively degrades to `passthrough` for that message. |
| REQ-EXTIMG-14 | Rewriting is deterministic: the same input message produces the same output bytes (modulo content-id allocation, which is monotonic from a per-message counter). Tests rely on this. |

## Fetcher and limits

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-20 | The fetcher reuses a single `*http.Client` per server with the SSRF-aware `Transport` (REQ-EXTIMG-30..37). Per-image fetches run in parallel up to a configurable concurrency cap (default 8 per message). |
| REQ-EXTIMG-21 | Per-image byte cap: default 5 MiB. Reads beyond the cap are aborted; the fetch is recorded as `failed: too_large` and the original URL is preserved in the rewritten body so the suite's "load originals" affordance still works (REQ-EXTIMG-50). |
| REQ-EXTIMG-22 | Per-message image count cap: default 100. Candidates beyond the cap are skipped (left as their original URL); the rewriter records a `truncated_at = N` flag in the per-message audit. |
| REQ-EXTIMG-23 | Per-message cumulative byte cap: default 50 MiB. When cumulative bytes for the message would exceed this cap, remaining candidates are skipped. |
| REQ-EXTIMG-24 | Per-image timeouts: 5 s connect, 30 s total. Per-message wall-clock budget: 60 s. The wall-clock cap is enforced even if per-image timeouts are loose. |
| REQ-EXTIMG-25 | HTTPS is the default; `http://` URLs are refused by default (`require_https = true`). Operators who serve users with legitimate http-image needs flip the toggle. |
| REQ-EXTIMG-26 | Redirects (3xx) are followed up to a configurable depth (default 3). Every redirect is re-validated against the SSRF guard; a redirect to a blocked address aborts the fetch. The final fetched URL is recorded. |
| REQ-EXTIMG-27 | Response `Content-Type` is captured and stored on the inline part. When the response declares a non-image MIME type (`text/html`, `application/javascript`, etc.) the fetch is recorded as `failed: not_image` and the bytes are discarded. |
| REQ-EXTIMG-28 | Empty / zero-byte responses are recorded as `failed: empty` and the original URL is preserved. |

## SSRF guard

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-30 | The fetcher's `Transport.DialContext` resolves the hostname **once**, validates the resolved IPs against the deny list (REQ-EXTIMG-32), and dials the validated IP literal. The HTTP client never re-resolves the hostname; this defeats DNS rebinding. |
| REQ-EXTIMG-31 | Schemes `http` and `https` only. Userinfo (`http://user:pass@host`) is refused. Only ports 80, 443, and any operator-allowlisted ports are accepted. |
| REQ-EXTIMG-32 | The deny list refuses every IP whose address falls inside a non-routable / locally-significant range. The default list covers the full set documented in IANA's IPv4 / IPv6 special-purpose registries: `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `224.0.0.0/4`, `240.0.0.0/4`, `255.255.255.255/32`, and the IPv6 equivalents including `::/128`, `::1/128`, `::ffff:0:0/96` (IPv4-mapped — bypasses naive v4 checks), `64:ff9b::/96`, `100::/64`, `2001::/23`, `fc00::/7`, `fe80::/10`, `ff00::/8`. |
| REQ-EXTIMG-33 | Operators MAY extend the deny list (`deny_cidrs = [...]`) but MUST NOT shrink it. An `allow_private = true` toggle exists for operators who legitimately fetch from RFC 1918 (rare; documented as dangerous). When `allow_private = true` only the IPv4-mapped-v6 and special-use ranges remain blocked. |
| REQ-EXTIMG-34 | Redirect targets (3xx Location) are subjected to the same guard as the originating URL. The HTTP client's `CheckRedirect` callback is the enforcement point. |
| REQ-EXTIMG-35 | When DNS resolution returns multiple A/AAAA records, **every** record is checked. If any one falls inside the deny list, the fetch aborts. The fetcher does not "race the safe ones"; the assumption is that an attacker who can answer DNS can mix poison records. |
| REQ-EXTIMG-36 | Operators MAY run the fetcher worker process in a network namespace or behind a firewall rule that drops packets to private ranges. The application guard does not depend on this; it is a belt-and-suspenders defence the operator chooses. |
| REQ-EXTIMG-37 | A guarded request that fails for SSRF reasons is recorded as `failed: blocked_ssrf` with the resolved IP and the matching CIDR in the audit log. The original URL is preserved in the rewritten body. |

## DKIM disposition (Option A)

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-40 | DKIM verification runs against the **wire-original** body, before any rewriting. The verdict (pass / fail / none / tempfail) and the signing domain + selector are persisted on the message in a `dkim_verification` field. |
| REQ-EXTIMG-41 | When the rewriter modifies the body, the inbound `DKIM-Signature` header is **stripped** from the stored message. A signature that no longer verifies is more confusing than no signature at all. |
| REQ-EXTIMG-42 | Inbound `Authentication-Results` headers (which encode the upstream relay's verification verdict) are stripped from the stored message; they reference the wire-original body and would mislead recipients that can verify locally. |
| REQ-EXTIMG-43 | The rewriter emits a single replacement `Authentication-Results` header naming the operator's host, encoding the verdict captured in REQ-EXTIMG-40, and recording that the body was modified. Example: `Authentication-Results: mail.huebner.org; dkim=pass header.d=newsletter.example.com header.s=ml-2024 (verified at delivery; body modified for image-privacy rewrite)`. |
| REQ-EXTIMG-44 | The rewriter adds an `X-Herold-Body-Modified: image-internalization` header so debugging tools can identify rewritten bodies. |
| REQ-EXTIMG-45 | When the operator selects `passthrough` mode, the rewriter does nothing. DKIM signatures are preserved unchanged and recipients can independently re-verify. This is the only mode in which independent verification is possible. |
| REQ-EXTIMG-46 | The wire-original body is **not retained** by default. The verdict is the only artifact of verification kept. Operators who need cryptographic forensic proof must use `passthrough` mode. (A future `retain_original` toggle for hybrid setups is reserved but unimplemented.) |
| REQ-EXTIMG-47 | **Reserved for a future ARC-sealing extension.** When implemented, the rewriter will emit `ARC-Authentication-Results`, `ARC-Message-Signature`, and `ARC-Seal` headers chaining the original verdict and signing the rewritten body with the operator's ARC key. Until that extension lands, recipients downstream of the operator (e.g., the operator forwards to another server) cannot validate the chain. |

## Storage and lifecycle

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-50 | The rewritten message is stored as the message's body blob. Internalized images are inline MIME parts of that body; their lifecycle is identical to any inline `cid:` reference that arrived inline. Refcounting, GC, blob dedup all work as for unmodified bodies. **There is no separate image cache table.** |
| REQ-EXTIMG-51 | Fetched bytes flow through the existing content-addressed `storeblobfs` for body-blob storage. Two recipients of the same newsletter end up sharing the underlying body blob (BLAKE3 dedup), so the storage cost of internalization scales with distinct messages, not recipients. |
| REQ-EXTIMG-52 | When a message is expunged, its body blob is refcount-decremented; the inline image bytes follow. No bespoke GC. |
| REQ-EXTIMG-53 | The rewritten body's size counts against the recipient's `quota_bytes`. A 50 MiB cumulative cap (REQ-EXTIMG-23) is the upper bound on what one message contributes. |

## Failure handling

*(REQ-EXTIMG-60/63/71/73 rewritten 2026-07-09, issue #162, to agree
with 23-extimg-background-internalize.md REQ-EXTIMG-BG-14. The
original wording let the rewritten body keep the raw origin URL (via
a `data-original-src` breadcrumb) for a client-side "load anyway"
retry; BG-14 later closed that as a privacy leak — any path that
bypasses the suite's gate (raw blob download, IMAP FETCH) would
expose the still-external URL to the sender's origin. The client-side
retry affordance these four REQs originally specified was never
built. The wording below describes the server-side retry that
replaces it: see "Server-side retry" below for the full mechanism.)*

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-60 | When a single image fetch fails (timeout, byte-cap exceeded, 4xx, 5xx, SSRF-blocked, redirect-loop, non-image content type), the rewriter replaces that image's reference with the herold-local placeholder (`extimg.PlaceholderDataURI`) in the rewritten body — the origin URL is never written into the delivered body. The URL and a splice-back HTML template are retained server-only (never returned to any client), keyed to the message, so a later retry can re-attempt it (REQ-EXTIMG-RETRY-01). |
| REQ-EXTIMG-61 | When the entire message rewrite fails (HTML parser error, MIME re-emit error, unrecoverable I/O), the message is stored verbatim. Delivery does not fail. |
| REQ-EXTIMG-62 | Per-message rewrite outcomes are written to an audit record on the message: `internalized = N images, failed = N (with reasons), original_size, rewritten_size, wall_ms`. The operator can query this for debugging without a server-side log dive. |
| REQ-EXTIMG-63 | The rewriter preserves enough server-only state to retry a failed fetch later without ever re-deriving the URL from the delivered (placeholder-only) body: the failed URLs, the HTML immediately before the placeholder pass (successes already `cid:`, failures still the raw URL), and the DKIM verdict, so a rebuilt Authentication-Results header matches the original. See REQ-EXTIMG-RETRY-01..08. |

## Suite UX

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-70 | When `internalize` is the active policy, every successfully-internalized image renders inline with no user action. The "show images" prompt is suppressed for messages whose images all internalized successfully. |
| REQ-EXTIMG-71 | When some images failed to internalize, the message renders with the successful ones inline and the failed ones as placeholders, plus a per-message "N images could not be loaded" affordance with a retry control. Retrying issues `Email/retryImages` (REQ-EXTIMG-RETRY-01): the SERVER re-attempts the retained URLs and, on success, rewrites the stored body in place. The browser never receives an origin URL to fetch itself — this is the privacy-preserving replacement for the pre-BG-14 client-side "load failed images" affordance. |
| REQ-EXTIMG-72 | When `passthrough` is the active policy, the suite's existing "show images" gating remains and the in-page image-load affordance is unchanged from today. |
| REQ-EXTIMG-73 | A badge on the message (and in the mailbox-row summary) surfaces `Email.failedImageCount` when it is greater than 0 (`"N images could not be loaded"`), backed by the retry control from REQ-EXTIMG-71. No badge when every image internalized or the message never had external images. The badge is a plain count; the failed URLs themselves are never sent to the client. |

## Server-side retry (issue #162)

*(Added 2026-07-09. Reconciles the privacy guarantee added by
23-extimg-background-internalize.md REQ-EXTIMG-BG-14 — the origin URL
never reaches the browser, via any path — with the recoverability
REQ-EXTIMG-71/73 originally promised. The chosen design: badge +
server-side retry, not a client-side "load anyway".)*

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-RETRY-01 | A new JMAP method `Email/retryImages` (request `{accountId, id}`, response `{accountId, id, retriedCount, failedImageCount, newState}`) re-fetches the message's retained failed-image URLs server-side. Registered under a new vendor capability, `https://netzhansa.com/jmap/email-image-retry`, so clients can detect the affordance. |
| REQ-EXTIMG-RETRY-02 | The retained state (failed URLs, splice-back HTML template, DKIM verdict) is server-only: a `messages.failed_image_state` column holding an opaque blob the store never interprets, decoded only by the `Email/retryImages` handler. It is never included in `Email/get`, IMAP FETCH, or any other client-facing response. |
| REQ-EXTIMG-RETRY-03 | `Email.failedImageCount` (a plain integer, `messages.failed_image_count`) is the only client-visible signal. 0 means every image internalized or the message never had external images. |
| REQ-EXTIMG-RETRY-04 | On a successful retry (some or all retained URLs newly fetched), the server rewrites the stored body in place — real images replace their placeholders — and advances Email state with `cause = 'user'` so `Email/changes` and the EventSource push loop notify the client to re-fetch, exactly like any other user-driven mutation. |
| REQ-EXTIMG-RETRY-05 | URLs that still fail on retry remain placeholdered in the delivered body (REQ-EXTIMG-BG-14 unchanged) and stay retained server-side for a further retry attempt; `failedImageCount` reflects only the still-unresolved count. |
| REQ-EXTIMG-RETRY-06 | Partial success is a normal outcome: a message with 5 failed images where 3 now fetch successfully rewrites those 3 and keeps 2 placeholdered/retained. `retriedCount` in the response is the number newly resolved this call, not the total. |
| REQ-EXTIMG-RETRY-07 | A retry that resolves nothing (still failing, or nothing retained) is a no-op response (`retriedCount = 0`); it does not clear or corrupt the retained state. |
| REQ-EXTIMG-RETRY-08 | `Email/retryImages` requires the same access the caller would need to mutate the message (owner, or ACL write right for a shared mailbox) — it changes the stored body, so read-only (Lookup) access is insufficient. |

## Observability

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-80 | Per-fetch metrics: `extimg_fetches_total{outcome=...}`, `extimg_fetch_bytes_total`, `extimg_fetch_duration_seconds` (histogram). Outcomes cover `ok`, `too_large`, `timeout`, `not_image`, `blocked_ssrf`, `http_4xx`, `http_5xx`, `redirect_loop`, `parse_error`. |
| REQ-EXTIMG-81 | Per-message log line at INFO recording: message id, mode, candidates found, internalized, failed (with the dominant reason), wall-ms. Aggregable in the existing log pipeline. |
| REQ-EXTIMG-82 | A debug toggle (`audit.log_fetches`) emits a structured record per fetch including the URL hash (`sha256(url)[:16]`, never the URL itself — operators sometimes share logs and URLs can be tracking pixels), bytes, status, duration, resolved IP. |

## Configuration

The full surface in `system.toml`:

```toml
[external_images]
mode = "internalize"        # internalize | passthrough; default internalize

[external_images.limits]
max_per_image_bytes      = 5_000_000
max_per_message_images   = 100
max_per_message_bytes    = 50_000_000
per_image_connect_timeout = "5s"
per_image_total_timeout  = "30s"
per_message_timeout      = "60s"
concurrent_fetches       = 8
require_https            = true
follow_redirects_max     = 3

[external_images.network]
deny_cidrs    = []          # extends the built-in deny list
allow_private = false

[external_images.dkim]
on_modification = "strip"   # strip | keep
arc_seal        = false     # reserved; not implemented in this iteration

[external_images.imports]
internalize_imports = "on_demand"  # on_demand | off
                                   # default: on_demand
                                   # imported messages flagged for
                                   # one-shot rewrite at first read

[external_images.audit]
log_fetches = false
```

## On-demand internalization for imported mail

*(Added 2026-05-08 after the maintainer asked whether Gmail-import
messages should also be internalized, and pushed me to challenge the
naive answer. Bulk-fetching at import time has three problems:
the privacy gain is asymmetric — for archived mail the sender
already received the original tracking pixel, so re-fetching only
leaks "this user is migrating" without denying the sender any
information; the async fetch window is itself a privacy hole during
which messages live in the DB with their original URLs; and 100k+
parallel fetches from one IP look like scraping to upstream CDNs.
The on-demand-at-first-read alternative spends fetches only on
messages the user actually engages with, exactly when the user
would have triggered a fetch via "show images" anyway, so engagement
maps 1:1 to internalization cost.)*

The on-demand path applies to **any** message that lands without
having gone through the live-delivery `internalize` rewriter — most
commonly imported mail, but also messages stored under a previously-
configured `passthrough` policy that the operator later switched to
`internalize`. Messages already-internalized at delivery (or stored
under `passthrough` with no flag set) are unaffected.

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-90 | Inbound message importers (Gmail Takeout, future IMAP-mirror imports) MUST NOT bulk-fetch external images at import time. Imported messages land verbatim. |
| REQ-EXTIMG-91 | When the operator policy is `internalize_imports = "on_demand"` (default) and the importer detects HTML body content with at least one external `http(s)://` reference, the imported message is flagged with a per-message `internalize_pending` marker. The detection is a fast substring scan; precise URL extraction is deferred to the read-time pass (REQ-EXTIMG-93). |
| REQ-EXTIMG-92 | Operators MAY disable on-demand internalization with `internalize_imports = "off"`. Imported messages then never carry the marker; reads behave identically to the live `passthrough` path. |
| REQ-EXTIMG-93 | The first JMAP `Email/get` (or any read path that materialises body content for the user) for a message carrying `internalize_pending` triggers a one-shot synchronous internalization: fetch every external image through the same SSRF-aware fetcher and limits used at delivery (REQ-EXTIMG-20..37), rewrite the HTML, replace the stored body blob via `Metadata.ReplaceMessageBody`, clear the marker. The user waits for the rewrite — bounded by the existing per-message timeout (REQ-EXTIMG-24, default 60 s). |
| REQ-EXTIMG-94 | The marker is cleared even when the rewrite makes no changes (every fetch failed, parser refused the HTML, etc.). Subsequent reads MUST NOT re-attempt the rewrite. The cost-of-engagement budget is "one fetch attempt per message per import"; an operator-driven retry hatch is reserved for a future iteration. |
| REQ-EXTIMG-95 | When the rewritten body would push the principal over `quota_bytes` (a fetched-images-heavy newsletter could add many MiB), the rewrite is abandoned, the marker is cleared, and a WARN log records the over-quota outcome with the message id. The user sees the original-URL body. |
| REQ-EXTIMG-96 | The rewrite uses the same DKIM disposition as live `internalize` (REQ-EXTIMG-41..44). For imported mail this is a no-op: the import path does not preserve original DKIM signatures across the rewrite, and the server-stamped `Authentication-Results` records the import-time verdict (typically `dkim=none` for archive imports). |
| REQ-EXTIMG-97 | The marker lives on the message metadata so `Email/get` can route on it without first hitting the body blob. A small integer column on `messages` (`internalize_pending`, default 0) is sufficient; the migration adds it as a non-NULL column with default 0 so existing rows do not need backfill. |
| REQ-EXTIMG-98 | The on-demand pass is observable: a per-message INFO log line (mode = `on_demand`, candidates, internalized, failed, wall-ms) and a `extimg_on_demand_total{outcome=...}` Prometheus counter mirror the live-delivery audit (REQ-EXTIMG-80..82). |
| REQ-EXTIMG-99 | Quota cost: the rewritten body's size is recorded in the principal's quota at the moment of the rewrite, replacing the original body's size contribution. There is no separate "pending image quota"; storage scales with engagement, not with archive size. |

### What this gives operators

Default install: an operator who runs `herold import gmail` on a
200k-message archive sees imports complete at the speed of pure
metadata insertion — no per-message HTTP fetch latency. Over the
following days/weeks/months the user's actual reads quietly
internalize the messages they engage with. Senders see a fetch from
the operator's IP only when (and if) the user actually opens that
specific message — which is the same moment the user would have
triggered a fetch via "show images" anyway, so internalization adds
no new fetch signal compared to the user opening the message in
"passthrough" mode and clicking the existing button.

For the user, the experience is: imported messages render with
"show images" still gated until first open; on first open the body
is rewritten and cached; every subsequent open is fast and
externally-silent. The first-open latency is bounded by the
per-message rewrite budget (60 s default).

### Out of scope (future iterations)

- **Retry hatch** for messages whose first-read rewrite failed
  (network was flaky, sender's CDN was down). REQ-EXTIMG-94 makes
  this explicit. A per-message admin endpoint to re-flag is the
  obvious shape; defer until someone asks.
- **Bulk re-internalize** when the operator switches policy from
  `passthrough` to `internalize`. The on-demand path covers this
  organically as users re-read affected messages, but a one-shot
  "rewrite everything in this principal" admin command is a
  reasonable future addition.
- **Per-domain `internalize_imports`** — defer until an operator
  asks. The config schema permits a `per_domain` extension later.

## Out of scope

- **ARC sealing** (REQ-EXTIMG-47) — reserved for a future iteration.
- **View-time image proxying** — strictly worse than delivery-time
  internalization; never planned.
- **Per-user "always show images" override** — recreates the bad UX
  the policy is trying to escape; deliberately omitted.
- **Per-domain mode override** — defer until an operator asks. The
  config schema permits adding `[[external_images.per_domain]]`
  later without breaking existing configs.
- **Retaining wire-original body** — REQ-EXTIMG-46 reserves this; not
  implemented in this iteration. Operators who need cryptographic
  forensic proof use `passthrough` mode.
