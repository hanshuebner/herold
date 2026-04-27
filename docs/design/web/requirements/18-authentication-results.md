# 18 — Authentication results

What tabard tells the user about whether a message's claimed sender actually authenticated. Sourced from the RFC 8601 `Authentication-Results` header that herold writes during inbound mail processing.

The principle: silent on pass, prominent on fail or none-where-expected, never false-precision. A lock icon next to a sender we can't actually verify is a worse user experience than an honest "could not verify".

## Source

| ID | Requirement |
|----|-------------|
| REQ-AR-01 | Tabard parses the topmost (most recent / our own) `Authentication-Results` header per RFC 8601. Multiple Authentication-Results headers exist on relayed mail; only the one written by our own server (the `authserv-id` matches the configured server identity) is authoritative. |
| REQ-AR-02 | If no `Authentication-Results` header from our own server is present, tabard displays "Authentication status not available" in the verbose tooltip and applies no soft cues. The card layout does not change. |
| REQ-AR-03 | The header is parsed for SPF (`spf=...`), DKIM (`dkim=... d=...`), DMARC (`dmarc=...`), and ARC (`arc=...`) results, plus the signing/asserted domain for each. |

## Display levels

Three states, in increasing prominence:

### Silent (most messages)

| ID | Requirement |
|----|-------------|
| REQ-AR-10 | When SPF=pass, DKIM=pass on a domain aligning with From, AND DMARC=pass: no banner, no chip. The sender's display name and email render normally. A tooltip on the From field shows the verbose result for users who explicitly check. |

### Soft cue (mild concerns)

| ID | Requirement |
|----|-------------|
| REQ-AR-20 | When the message would have passed DMARC by one mechanism but not the other (e.g. SPF aligned, DKIM not present; or DKIM aligned, SPF softfail): a small subtle indicator on the sender — `--text-secondary` weight on the email portion. The tooltip explains. |
| REQ-AR-21 | When ARC chain validates but the original auth didn't pass at the originating boundary: same soft indicator. ARC=pass means "a downstream mailer claims this passed earlier"; we trust the chain but communicate the indirection. |

### Prominent banner (real concerns)

| ID | Requirement |
|----|-------------|
| REQ-AR-30 | When DMARC=fail and the From domain has a published DMARC policy of `quarantine` or `reject`, but the message landed in our inbox anyway (e.g. operator override): a banner above the message body — "This message claims to be from <from-domain> but failed authentication checks the domain explicitly requires." |
| REQ-AR-32 | When `Reply-To` is set and differs from `From` *and* the From domain is one the user has corresponded with before (heuristic: any Email with this From in the user's mailboxes within the last 90 days): a banner — "Replies will go to a different address — <reply-to>." |
| REQ-AR-33 | When the message arrived via the mail-spam-llm classifier as spam but was placed in inbox anyway (the operator's filter chose so): banner — "This message was flagged as spam by the server." |

## Tooltip detail

| ID | Requirement |
|----|-------------|
| REQ-AR-40 | The verbose tooltip on the sender's email shows, in plain language: SPF result + IP / DNS that produced it; DKIM result + signing domain; DMARC result + alignment status; ARC chain summary if present. |
| REQ-AR-41 | The tooltip is also available from a small "i" icon on the message header for users who don't think to hover the email. |
| REQ-AR-42 | The wording in the tooltip is plain English — "DKIM signature from example.com verified" — not RFC jargon. The raw header is one click further down ("Show raw"). |

## What we deliberately do NOT do

- A green-lock icon on every signed message. False precision: most users don't know what a green lock means in mail context, and "DKIM=pass" doesn't mean "this message is from Alice", only "this message's body wasn't tampered with after example.com signed it".
- A red banner on every "DMARC=none" message. Many legitimate senders have no DMARC policy. Banners reserved for cases where the domain *says* it should pass and didn't.
- Auto-quarantine in the client. That's herold's job (filter / Sieve / mail-spam-llm). Tabard surfaces what's there.
- Phishing detection by URL inspection inside the body. Out of scope; defer to browser safe-browsing on click.

## Cross-reference to herold

The `Authentication-Results` header tabard reads is herold's output (its `internal/mailauth/` and `internal/maildkim/`/`internal/mailspf/`/`internal/maildmarc/`/`internal/mailarc/` packages produce it). The `authserv-id` tabard matches against is herold's configured server identity. If herold's auth pipeline regresses, tabard's banners regress with it; the source of truth is the server.
