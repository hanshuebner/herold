# 04 — Email security

DKIM, SPF, DMARC, ARC, MTA-STS, DANE, TLS-RPT, and related. These protocols govern whether our outbound mail is deliverable and whether our inbound filtering can trust sender claims. This is not optional; it is table stakes for a mail server in 2026.

## SPF — Sender Policy Framework (RFC 7208)

### Verify (inbound)

- **REQ-SEC-01** MUST verify SPF for every inbound message at or before end-of-DATA, producing one of: `pass`, `fail`, `softfail`, `neutral`, `temperror`, `permerror`, `none`.
- **REQ-SEC-02** SPF check uses `MAIL FROM` domain (RFC 5321.From) and HELO domain. The HELO result is used when `MAIL FROM` is null (bounces).
- **REQ-SEC-03** SPF result contributes to the DMARC evaluation and the spam score. SPF `fail` alone does not reject in v1 (DMARC decides).
- **REQ-SEC-04** MUST enforce RFC 7208 §4.6.4 — a maximum of 10 DNS lookups per SPF evaluation. `permerror` on exceed.

### Publish (outbound / operator help)

- **REQ-SEC-05** Admin tooling MUST produce a recommended SPF record per sending domain (based on the server's public IP/IPv6 or configured `ip4:`/`ip6:` blocks).

## DKIM — DomainKeys Identified Mail (RFCs 6376, 8301, 8463)

### Sign (outbound)

- **REQ-SEC-10** MUST sign outbound messages with DKIM for their sending domain.
- **REQ-SEC-11** MUST support `rsa-sha256` (RFC 6376) and `ed25519-sha256` (RFC 8463). Default: generate both key types per domain, sign with both.
- **REQ-SEC-12** Keys per domain are managed by the server: generation, storage, publishing instructions (TXT record content), rotation.
- **REQ-SEC-13** Canonicalization: `relaxed/relaxed` default. `simple/simple` MAY be configured. Other combinations rejected.
- **REQ-SEC-14** Selector naming: `<year><month>` (e.g. `202604`) default. Operator-override allowed.
- **REQ-SEC-15** Signed headers: default set is `From`, `Reply-To`, `Subject`, `Date`, `To`, `Cc`, `MIME-Version`, `Content-Type`, `Content-Transfer-Encoding`, `Message-ID`, `Content-ID`, `Content-Description`, `References`, `In-Reply-To`, `List-Id`, `List-Help`, `List-Unsubscribe`, `List-Subscribe`, `List-Post`, `List-Owner`, `List-Archive`. `From` MUST be signed. Body-length limit (l= tag) MUST NOT be used.

### Verify (inbound)

- **REQ-SEC-20** MUST verify all DKIM signatures present on inbound messages.
- **REQ-SEC-21** MUST enforce RFC 8301: reject SHA-1-based signatures as invalid, reject RSA < 1024, warn on RSA < 2048.
- **REQ-SEC-22** Per-signature result contributes to DMARC (aligned signatures only) and to the spam score.

### Key rotation

- **REQ-SEC-23** Operator action `rotate-dkim <domain>` MUST generate a new key, publish the new selector (via admin UI/CLI displayed TXT record), continue signing with the old key for a grace period (default 7 days), then switch to new.
- **REQ-SEC-24** Inbound verify uses the `s=` selector from the signature to fetch the public key; cached with TTL from DNS.

## DMARC (RFC 7489)

### Evaluate (inbound)

- **REQ-SEC-30** MUST evaluate DMARC for every inbound message with a parseable `From:` header. Result: `pass` (aligned SPF or DKIM) or `fail`.
- **REQ-SEC-31** MUST honor `p=` and `sp=` policies. On `reject`, reject the message at DMARC evaluation time (end of DATA or immediately after). On `quarantine`, deliver to `Junk`/`Spam` folder (unless Sieve overrides).
- **REQ-SEC-32** MUST honor `pct=` sampling correctly — apply policy only to the pct fraction.
- **REQ-SEC-33** MUST handle alignment strictness (`aspf`, `adkim` = `r` or `s`) correctly.
- **REQ-SEC-34** Report (RUA) aggregation: MUST collect per-source aggregates and send daily reports to the `rua=` addresses. `ruf=` (failure reports) optional — default off (privacy-sensitive).

### Publish (outbound)

- **REQ-SEC-35** Admin tooling MUST produce a recommended DMARC record per domain, starting at `p=none` with `rua=` pointing at the server's report ingestion endpoint.

### Report ingestion

- **REQ-SEC-36** MUST accept DMARC aggregate reports delivered to a configured mailbox (e.g. `dmarc-reports@<domain>`). Parse and expose via admin UI/CLI.
- **REQ-SEC-37** Report storage bounded (default: 90 days retention; configurable).

## ARC — Authenticated Received Chain (RFC 8617)

### Verify and re-sign

- **REQ-SEC-40** MUST verify existing ARC chain on inbound. `cv=pass|fail|none`.
- **REQ-SEC-41** MUST preserve ARC chain on forwarded/re-delivered messages (Sieve redirect, alias fanout) — add our own `ARC-Seal` and `ARC-Message-Signature` with instance++.
- **REQ-SEC-42** MUST NOT reject solely on ARC fail; it feeds the spam score and DMARC-override logic (if we trust the forwarder).

## MTA-STS (RFC 8461) and DANE (RFC 7672)

### Outbound enforcement

- **REQ-SEC-50** For every outbound delivery, MUST look up recipient domain's MTA-STS policy (`_mta-sts.<domain>` TXT + `https://mta-sts.<domain>/.well-known/mta-sts.txt`) and DANE TLSA records.
- **REQ-SEC-51** Policy precedence for outbound TLS:
  1. If DANE TLSA records present and DNSSEC-validated → require TLS with matching cert.
  2. Else if MTA-STS `enforce` → require TLS with a valid cert matching the MX.
  3. Else MTA-STS `testing` or no policy → opportunistic TLS.
- **REQ-SEC-52** MUST cache MTA-STS policies per their `max_age` up to 31 days (spec limit).
- **REQ-SEC-53** MUST emit TLS-RPT reports to the domain's `rua` if they publish TLSRPT. Reports include TLS failures and MTA-STS policy misses.

### Inbound publishing (operator help)

- **REQ-SEC-54** Admin tooling MUST publish MTA-STS policy via the server's HTTPS surface at `/.well-known/mta-sts.txt` for configured domains. The `mta-sts.<domain>` HTTPS vhost is served by the server itself (cert via ACME).
- **REQ-SEC-55** SHOULD generate TLSRPT-style records for the operator to publish: `_smtp._tls.<domain> TXT v=TLSRPTv1; rua=mailto:…`.

## DANE TLSA publishing (operator help)

- **REQ-SEC-60** Admin tooling MUST emit the correct TLSA record content for the current cert (CU=3 1 1 SHA-256 of SPKI) so operators can publish in DNSSEC-signed zones.
- **REQ-SEC-61** On certificate rotation, operator warned that TLSA must be updated ahead of cutover (standard DANE "two TLSA records" advice).

## Forged-sender / impersonation protection

- **REQ-SEC-70** Inbound DMARC `reject` on a message claiming to be from a domain we host (a forgery of our own domain from outside) MUST be rejected unconditionally.
- **REQ-SEC-71** The spam filter MUST flag unaligned `From:` vs `Return-Path:` vs envelope sender mismatches independent of DMARC.
- **REQ-SEC-72** Brand Indicators for Message Identification (BIMI, RFC 9512) — deferred, not a v1 requirement.

## Certificate and TLS requirements (summary; detailed in 09-operations.md)

- **REQ-SEC-80** TLS 1.2+ on all listeners. TLS 1.3 preferred.
- **REQ-SEC-81** ACME (RFC 8555) for automatic cert issuance: HTTP-01 for HTTPS vhosts, DNS-01 for wildcards and mail hostnames where HTTP-01 isn't feasible. TLS-ALPN-01 supported on 443.
- **REQ-SEC-82** Inbound STARTTLS on 25 MUST present a valid cert for the MX hostname (DANE/MTA-STS depends on this).
- **REQ-SEC-83** Self-signed certs usable only in dev mode. Production mode refuses to start with self-signed unless explicitly allowed.

## Cryptographic algorithm policy

- **REQ-SEC-90** Allowed: Ed25519, ECDSA P-256, RSA ≥ 2048, SHA-256, SHA-384, SHA-512, ChaCha20-Poly1305, AES-GCM.
- **REQ-SEC-91** Forbidden for signing: MD5, SHA-1, RSA-1024 (except as inbound-verify fallback per DKIM).
- **REQ-SEC-92** Forbidden TLS: CBC suites, RC4, 3DES, export ciphers, static DH, NULL cipher, RSA key exchange (non-PFS).

## Out of scope

- S/MIME signing/encryption at the server level (end-to-end concern).
- OpenPGP autocrypt.
- BIMI / VMC logo display.
- Upcoming drafts: DKIM2, "Responsible-From", Authentication-Results-Chain.
