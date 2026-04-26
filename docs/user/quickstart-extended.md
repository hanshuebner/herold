# Extended quickstart: a real domain with public inbound

This is the follow-up to the README's 3-5 minute loopback quickstart.
It walks through standing up herold on a real domain you control, with
public inbound MX, ACME-issued certs, DKIM publication, DMARC,
MTA-STS, and TLS-RPT - what a deliverable mail server needs.

If you have not already done the loopback quickstart, start there:
[../../README.md](../../README.md). For the operator runbook, see
[./operate.md](./operate.md). For application administration, see
[./administer.md](./administer.md).

## Prerequisites

- **A domain you control.** You can edit DNS records at the
  registrar or DNS provider's control panel. For this walkthrough we
  use `example.com` - substitute your own.
- **A server with public IPv4 (and ideally IPv6).** A cloud VM, a
  bare-metal box, or a home server with a port-forward configured.
- **Public reachability of port 25.** Many residential ISPs and most
  major cloud providers (AWS, GCP, Azure) **block tcp/25 outbound by
  default**. Test before you start: from the host, run
  `nc -vz mx.gmail.com 25`. If you cannot reach Gmail's MX on port 25,
  outbound direct delivery will not work - you must use a smart host
  (see "Common gotchas" below).
- **Reverse DNS for the host's public IP.** Configure with the ISP /
  cloud provider so that `ip-of-mail.example.com` resolves back to
  `mail.example.com`. Many receivers refuse mail from hosts whose
  reverse DNS does not match HELO.
- **An IMAP client** (Thunderbird, Apple Mail, mutt, etc.) to verify
  inbound mail.

Estimate 30-60 minutes end-to-end, dominated by DNS propagation.

## Step 1 - DNS records (the four-record minimum)

Publish the following at your DNS provider before the server starts.
TTL 300 s while iterating; raise to 3600 once stable.

### A / AAAA

```
mail.example.com.    300    IN  A     203.0.113.10
mail.example.com.    300    IN  AAAA  2001:db8::10
```

`mail.example.com` is herold's primary FQDN. Set
`[server] hostname = "mail.example.com"` in `system.toml`.

### MX

```
example.com.         300    IN  MX    10 mail.example.com.
```

Priority 10 is conventional. Add lower-priority backups (`20
mx2.example.com.`) only if you actually run a backup MX - herold is
single-node, and a backup MX without herold's queue knowledge causes
delivery delays.

### SPF

```
example.com.         300    IN  TXT   "v=spf1 mx -all"
```

`mx` authorises every host in the MX record set. `-all` rejects every
other source (hard fail). Use `~all` (soft fail) for a few weeks while
you verify nothing else legitimately sends as `@example.com`, then
tighten to `-all`.

### DKIM

DKIM TXT records are generated when you run `herold domain add
example.com`. The selector and key body are emitted by herold; you
copy them into the zone (or, with a DNS-provider plugin, herold
publishes them itself - see step 4).

```
default._domainkey.example.com.   300  IN  TXT  "v=DKIM1; k=rsa; p=MIIBIjANBgkqhki..."
```

(The selector defaults to `default`; herold supports per-domain
per-selector keys - you can rotate selectors without breaking
in-flight mail.)

## Step 2 - TLS via ACME

ACME issues certs for `mail.example.com` and the MTA-STS vhost
`mta-sts.example.com`. The herold ACME client supports HTTP-01 (port
80), TLS-ALPN-01 (port 443), and DNS-01 (via DNS-provider plugin).

### Configure ACME

```toml
# /etc/herold/system.toml
[acme]
email = "ops@example.com"
directory_url = "https://acme-v02.api.letsencrypt.org/directory"
```

For iteration, swap the directory:

```toml
directory_url = "https://acme-staging-v02.api.letsencrypt.org/directory"
```

Staging issues untrusted certs but does not rate-limit aggressively -
the right place to debug.

### Bind herold to ports 80 and 443

For HTTP-01 / TLS-ALPN-01 to work, herold must be reachable on
`tcp/80` and `tcp/443` from the public internet. On Linux,
non-root processes cannot bind sub-1024 ports by default. Three ways
forward:

1. **systemd socket activation** - the cleanest. The systemd unit
   binds the privileged ports as root, then hands the file
   descriptors to herold which runs as `herold:herold`.
2. **`setcap`** - `setcap 'cap_net_bind_service=+ep' /usr/local/bin/herold`.
   The binary then binds privileged ports without root.
3. **Reverse-proxy / port-forward at the firewall** - bind the
   herold ACME listener on `tcp/8080` and have iptables redirect
   `tcp/80` and `tcp/443` to it.

With the `[acme]` block in place and the challenge method configured
above, herold will provision the cert at startup and renew automatically.
Use `herold cert list` to verify the issued cert and its remaining
lifetime once the server starts.

## Step 3 - DKIM key

```bash
herold domain add example.com
```

Herold generates a DKIM keypair for the domain and emits the
DNS-publishable TXT record. With no DNS plugin configured, the
record is printed for the operator to paste into the zone:

```
default._domainkey.example.com.  300  IN  TXT  "v=DKIM1; k=rsa; p=MIIBIjANBgkqhki..."
```

With a DNS-provider plugin (Cloudflare, Route53, Hetzner, manual /
webhook generic), herold publishes the record via the plugin
(REQ-OPS-60).

To re-emit the DKIM TXT for a domain already on the books:

```bash
herold dkim show example.com
```

prints the active selector, the algorithm, and the DNS TXT body
ready to copy into a zone file (or already published, if a DNS
plugin is configured). To rotate to a new selector:

```bash
herold dkim generate example.com
```

The same surface is available over REST at
`POST /api/v1/domains/{name}/dkim` (rotate) and
`GET /api/v1/domains/{name}/dkim` (list).

Verify DKIM publication by querying DNS from a third-party resolver:

```bash
dig TXT default._domainkey.example.com @1.1.1.1
```

The TXT record should contain the same `v=DKIM1; k=rsa; p=...` body
herold emitted.

## Step 4 - Start the server

With the system.toml config pointing at the real domain, start
herold:

```bash
herold server start --system-config /etc/herold/system.toml
```

Or under systemd:

```bash
systemctl start herold
```

Verify:

```bash
herold server status
herold cert status                # check TLS cert state.
herold domain list                # confirm example.com is registered.
```

`/healthz/ready` should return 200 once every listener is bound, the
store is open, ACME (when enabled) has issued certs, and every
critical plugin is up.

## Step 5 - First inbound test

From an external mailbox (Gmail, iCloud, your mobile carrier), send
an email to `you@example.com` (use a principal you've added - see
[./administer.md](./administer.md) for `herold principal create`).

Verify it arrives:

- Connect an IMAP client to `mail.example.com:993` (IMAPS) with
  the principal's credentials.
- The message should be in INBOX within a few seconds of send.

Troubleshooting:

- **The message is not in INBOX.** Check `herold queue list` - if a
  spam plugin or Sieve script routed the message, it will be visible
  via `herold queue show <id>`. (Inbound mail goes through the
  `protosmtp` ingress, then spam classification, then Sieve, then
  delivery; each stage logs.)
- **The remote sender's MX retried.** Run `tail -f` on herold's logs
  while sending - the SMTP session shows up with timestamps and a
  correlation ID. If herold accepted but the receiver did not see the
  message, the queue holds it; retry is automatic.
- **The remote sender refused before connecting.** Outbound MX
  lookup or reverse-DNS lookup failed. Check the sender's bounce
  message; common causes are MX TTL not yet propagated, or reverse
  DNS not configured.

## Step 6 - DMARC report monitoring

Once SPF and DKIM are in place, publish a DMARC TXT to start
collecting aggregate reports:

```
_dmarc.example.com.   300    IN  TXT   "v=DMARC1; p=quarantine; rua=mailto:dmarc-reports@example.com; pct=100"
```

Add the alias so the reports land in a real mailbox:

```bash
# Planned, Wave X.Y - until then, configure the reports mailbox via REST.
herold alias add dmarc-reports@example.com admin@example.com
```

Receivers (Google, Microsoft, Yahoo, etc.) send daily aggregate XML
reports to the address in `rua`. Herold parses and stores aggregate
reports per REQ-ADM-17; query them with the REST surface
(`/api/v1/reports/dmarc`) or, when the CLI lands,
`herold reports dmarc list --since 7d`.

Iterate from `p=none` (monitor only) to `p=quarantine` (suspect mail
goes to spam) to `p=reject` (suspect mail is refused) over the course
of a few weeks, watching the report rate.

## Step 7 - MTA-STS policy publication

MTA-STS (RFC 8461) tells sending MTAs that your domain requires TLS
on inbound and lets them refuse to fall back to plaintext on
downgrade.

### Publish the TXT record

```
_mta-sts.example.com.    300  IN  TXT   "v=STSv1; id=20260425000000Z;"
```

The `id` is a freely-chosen string (often a timestamp) that changes
when you change the policy. Senders cache the policy until the `id`
changes.

### Publish the policy file

The policy file lives at:

```
https://mta-sts.example.com/.well-known/mta-sts.txt
```

Body:

```
version: STSv1
mode: enforce
mx: mail.example.com
max_age: 86400
```

Herold serves this file out of its admin vhost when MTA-STS is
enabled for the domain. The cert for `mta-sts.example.com` is part
of the same ACME flow as `mail.example.com` (different SAN, same
account).

### TLS-RPT (companion)

```
_smtp._tls.example.com.   300  IN  TXT   "v=TLSRPTv1; rua=mailto:tlsrpt-reports@example.com"
```

Receivers send daily TLS report JSON to the `rua` address. Like
DMARC reports, these surface inbound-TLS health and downgrade
attempts. Herold parses them per REQ-ADM-18.

## Common gotchas

### "ISP / cloud provider blocks port 25"

Most major cloud providers (AWS, GCP, Azure, OVH on some plans) and
many residential ISPs block tcp/25 outbound. Inbound usually works
fine; outbound direct delivery does not. Two routes around it:

1. **Use a smart host.** Funnel outbound through SES / SendGrid /
   Gmail / a corporate relay. See
   [./examples/system.toml.smarthost](./examples/system.toml.smarthost)
   for the target config shape; smart-host implementation lands in
   Wave 3.1.
2. **File a request with the provider.** AWS lifts the SES sandbox
   limit on request; some VPS providers will unblock port 25 on
   request after vetting.

### "Reverse DNS doesn't match HELO"

Many receivers (Google, Microsoft) refuse mail from hosts whose
reverse DNS lookup does not match the HELO hostname. Symptom:
delivery to Gmail returns `421 4.7.0 ... must have rdns matching
helo`. Fix: configure rDNS at the ISP / cloud provider's control
panel so the public IP resolves to `mail.example.com`. The change
takes minutes-to-hours to propagate.

### "Receiver refuses without DMARC alignment"

Once you publish a DMARC TXT, receivers expect SPF or DKIM to align
with the From: domain. Symptom: delivery returns `550 5.7.26 ... DMARC
policy violation`. Fix: confirm

- The MAIL FROM domain (SPF check basis) matches your hosted domain.
- The DKIM `d=` tag matches your hosted domain.

If both align and the bounce continues, check that the DMARC TXT is
syntactically valid: `dig TXT _dmarc.example.com` should return a
single record beginning `v=DMARC1;`.

### "ACME hits a rate limit"

Let's Encrypt enforces aggressive rate limits per registered domain
and per IP. While iterating, switch to staging:

```toml
[acme]
directory_url = "https://acme-staging-v02.api.letsencrypt.org/directory"
```

Once stable, swap back to production. Staging certs are not trusted
by browsers or mail clients, so test delivery flows separately.

### "MTA-STS policy fetch fails"

Symptom: senders log `unable to fetch mta-sts policy`. Causes:

- The cert on `mta-sts.example.com` does not chain to a public root.
  ACME-issue a SAN cert covering both `mail.example.com` and
  `mta-sts.example.com`.
- The `https://mta-sts.example.com/.well-known/mta-sts.txt` URL
  returns 404 or is not served by herold. Confirm the domain has
  MTA-STS enabled (REST: `/api/v1/domains/{name}/mta-sts`).
- The TXT record `_mta-sts.example.com` is missing or malformed.

### "DKIM verification fails on receivers"

Symptom: receivers' DMARC reports show `dkim=fail`. Causes:

- DKIM TXT record body is wrong - copy-paste truncation is the
  classic. Re-emit with `herold domain dkim show <domain>` (planned;
  until then, the REST surface) and re-publish.
- DNS provider mangled the TXT body. Some providers split long TXT
  records on whitespace; herold's DKIM keys are 2048-bit and the TXT
  record is long. Concatenate quoted strings per RFC 6376.
- The signing selector in `system.toml` does not match the published
  selector. Confirm with `dig TXT <selector>._domainkey.example.com`.

## Where to next

- Day-to-day operator runbook (queue triage, observability, signals,
  performance tuning): [./operate.md](./operate.md).
- Application administration (principals, mailboxes, aliases, API
  keys, Sieve, audit log): [./administer.md](./administer.md).
- The historical record (requirements, architecture, implementation):
  the `docs/design/` tree.
