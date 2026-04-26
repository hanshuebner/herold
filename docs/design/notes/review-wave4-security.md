# Wave 4 — security review

Date: 2026-04-24. Scope: all security-sensitive surfaces committed up through Wave 3.

## Blocking findings (must resolve before Phase 1 ship)

- [severity: high] `internal/protosmtp/session.go:1034` — checklist item 7 (SASL channel binding) — `endpointBinding(tls.ConnectionState)` is a stub that always returns `nil`. `sasl/scram.go:180-186` then refuses `-PLUS` with `ErrChannelBindingMismatch`. Net effect: SCRAM-SHA-256-PLUS is advertised in the EHLO mechanism list (`session.go:342`) but cannot succeed. A conforming client that selects `-PLUS` will always fail authentication. This is failure-closed (not an auth-bypass) but is a correctness-and-downgrade-UX problem: clients may auto-fall-back to SCRAM without channel binding, exposing the TLS-auth binding gap that `-PLUS` was supposed to close. Same construct exists for IMAP at `internal/protoimap/session.go:393-397` (no `sasl.WithTLSServerEndpoint` is ever applied, so the binding comparison is structurally unreachable). Fix: either (a) populate `serverEndpoint` from the leaf certificate via `tls.Config.GetCertificate`'s callback, or (b) stop advertising `-PLUS` until that lands.

- [severity: high] `internal/directoryoidc/rp.go:354-385` — checklist item 6 (OIDC RP, audience confusion) — `VerifyAccessToken` iterates every registered provider and accepts the token if **any** one verifies + has a local link for the subject. If an operator ever registers two providers where provider A's issuer can also sign tokens with provider B's ClientID, the stored `oidc_links` row scoped to B is unlocked by an A-signed token. More practically: the function does not scope by a `hint` (e.g. `iss` from unverified JWT header), so a legitimate token for provider A that happens to produce a matching `sub` in B's link table authenticates as B's principal. Combine with the broad RFC 7662 introspection TODO at line 353 and this gets worse when extended. Fix: require the caller (SASL OAUTHBEARER) to pass an issuer/provider hint, or embed the provider name in the token the mail client presents (e.g. via the SCRAM/SASL `authzid`), and verify only against that one provider. Also: unauthenticated callers (SASL OAUTHBEARER on a just-opened submission port) can drive `VerifyAccessToken` into an iteration over every configured provider's JWKS fetch on unknown keys — modest DoS amplification.

- [severity: high] `internal/protoadmin/oidc.go:181-209` (`handleOIDCCallback`) — checklist item 6 — the callback only ever calls `CompleteLink`. `CompleteLink` first calls `takePending(state)` (rp.go:273) which **removes** the state regardless of outcome, then rejects with `ErrInvalidState` if the pending record was a sign-in flow. By then the state token is consumed and can never be retried via `CompleteSignIn`. Net effect: sign-in flows are unreachable through the sole HTTP callback. While the functional break is itself a ship-blocker, the security angle is that `takePending` mutates before classifying, inviting a future caller to re-introduce the race differently; the hand-rolled flow-type discrimination via in-memory `principalID` field is fragile. Fix: branch on the pending record's `principalID` (or an explicit flow-type enum) **before** consuming state, then dispatch to the correct completion. Separately, the callback accepts `code`/`state` from query string with no CSRF protection; since state itself is the binding, this is tolerable — but audit-log the outcome.

## Non-blocking findings (should resolve before external review)

- [severity: medium] `internal/protosmtp/deliver.go:267` — checklist item 1 — `Received:` header is rendered from `sess.helo` with no character-set validation. `readCommandLine` (session.go:796) strips the terminal CRLF via `bufio.Reader.ReadLine` but does not reject embedded bare CR, bare LF at byte 0, 8-bit bytes, or control characters. A malicious remote can inject bytes into the stored `Received` header that corrupt downstream log pipelines and some header-trusting tooling. Restrict `helo` to RFC 5321 domain / address-literal / `HelloField` atoms at parse time.

- [severity: medium] `internal/appconfig/appconfig.go:181-252` — checklist item 10 — `Import` mutates principals, domains, OIDC providers, and sieve scripts, and propagates `Flags` including `PrincipalFlagAdmin`, with **no** audit-log entries. CLI-only today, but Phase 1 operator mistakes are invisible in the audit trail. Emit one `appconfig.import.*` record per mutation.

- [severity: medium] `internal/protoadmin/auth.go:88` — checklist item 5 — API-key lookup calls `GetAPIKeyByHash(hashed)` then runs a constant-time compare of the returned `key.Hash` against the same `hashed` local. Because the lookup key **is** the hash, the compare is tautological and the preceding SQL lookup is the real timing surface. The comment correctly identifies this as defence-in-depth, but the timing story depends on the backend. Confirm that `storesqlite`/`storepg` `GetAPIKeyByHash` binds the hash as a parameter (not string interpolated) and returns ErrNotFound without leaking via execution time — out of my read scope here.

- [severity: medium] `internal/plugin/backoff.go:33` — checklist item 15 — uses `math/rand` seeded from `time.Now().UnixNano()` for restart-backoff jitter. Not a security-sensitive RNG use (plugin restart timing is not secret material), but it violates the wall-clock-injection rule in STANDARDS.md §5 and breaks deterministic testing. Replace with `crypto/rand`-seeded PRNG **or** inject via clock.

- [severity: low] `internal/mailarc/verifier.go:39-45` — checklist item 1 — ARC verification is structural-only in Wave 3; no cryptographic seal verification. The comment is explicit and DMARC does not currently consume ARC results (confirmed in `internal/maildmarc/evaluator.go`), so a spoofed ARC chain does not influence disposition today. Track: if any future Sieve `arctest` or scoring rule reads `r.ARC.Status == AuthPass`, this becomes exploitable.

- [severity: low] `internal/protosmtp/session.go:1040-1046` — checklist item 15 — `newSessionID` seeds from wall-clock + atomic counter for log-correlation IDs. Comment correctly identifies these as non-security; fine as-is. Noted for posterity.

- [severity: low] `internal/protoadmin/server_endpoints.go:182-252` — checklist item 10 — `/api/v1/audit` allows all admins to read every audit row with an arbitrary RFC 3339 `since`/`until` cursor and no record-count cap beyond an operator-supplied `limit`. Fine; the concern is that the store layer must enforce its own maximum `limit`. Confirm `ListAuditLog` caps pagination.

- [severity: low] `internal/protoimap/session.go:101` — checklist item 1 — `SetReadDeadline` uses a fixed 30-minute window per command. Combined with `MaxConnections` (which defaults to **unlimited** per `protoimap/server.go:33-36`), a single IP could park up to `MaxConcurrentPerIP`-free sessions until exhaustion. `protoimap` has no per-IP cap (unlike `protosmtp`). Add one at the server, or require operators to front IMAP with an external limiter.

- [severity: low] `internal/sysconfig/secret.go:44-46` — checklist item 9 — `ResolveSecret` silently passes through inline strings. STANDARDS.md §9 says "No inline secrets in system.toml" — the resolver enables the anti-pattern. Either reject the fallback path or warn loudly when it is taken.

- [severity: low] `internal/protosmtp/session.go:1043` — very minor — `fmt.Appendf(nil, "%d-%d", ...)` is fine but the session-ID format uses only the first 8 bytes of a SHA-256; still trivial to disambiguate. OK.

## Questions / needs clarification

- Channel binding for SCRAM-`*-PLUS`: is the plan to (a) compute `tls-server-end-point` from the actual served cert via `tls.ConnectionState.PeerCertificates` on a server config (stdlib does **not** expose the local cert on `ConnectionState`; `GetCertificate` gives us access at handshake), or (b) drop `-PLUS` from Phase 1? I assumed a Phase 1.5 plumbing — the AUTH advertisement needs to match whichever decision is made.
- OIDC RP: is Phase 1 intentionally single-provider-per-deployment? If so, the audience-confusion concern vanishes; say so in a comment. If multi-provider is a requirement, `VerifyAccessToken` needs a provider selector.
- Is there a documented plan for how a submission-listener client proves possession of an OIDC-issued token (vs. an ID token stolen from the browser)? OAUTHBEARER typically uses access tokens; the current verifier uses `oidc.Provider.Verifier(...).Verify()` which validates **ID tokens**. Access tokens are often opaque and need RFC 7662 introspection (per the TODO in rp.go:352-353). Confirm which token shape clients are expected to present at submission time.

## Observations (no action required)

- `internal/directory/password.go` — Argon2id parameters (t=2, m=64 MiB, p=4, salt=16, key=32) are sensible defaults; hash format is the standard `$argon2id$` encoding, verification reads parameters from the encoded hash so rotation is forward-compatible.
- `internal/directory/directory.go:283-316` — `Authenticate` calls `rl.record` on malformed-email, missing-principal, disabled-principal, and wrong-password paths alike, and wraps everything in `ErrUnauthorized`. No timing oracle for user-enumeration and no username-probe rate-limit bypass. Good.
- `internal/sasl/scram.go:250-253` — `fakeCredentials` keeps the Start-path timing indistinguishable for unknown authcids. Proper defence.
- `internal/sasl/plain.go:62-64` — PLAIN refuses `authzid != authcid` (no SASL proxying). Tight.
- `internal/tls/tls.go:103-110` — Mozilla Intermediate suites + TLS 1.2/1.3 only. Correct baseline.
- `internal/plugin/codec.go:16` — 16 MiB frame cap on JSON-RPC, `DisallowUnknownFields` on decode, newline-delimited framing, per-plugin client isolation via os.exec with pipes. No in-process loader; process boundary intact.
- `internal/protoadmin/problem.go:48-57` — `http.MaxBytesReader(..., 1<<20)` on JSON decode prevents body-bloat DoS at the admin API.
- `internal/observe/secret.go` — slog handler strips `password|token|api_key|secret|authorization|cookie|set-cookie` on both `Handle` and `WithAttrs`, recurses into groups. Solid.
- `grep 'unsafe.Pointer'` across `internal/`, `plugins/`, `cmd/` returns zero hits. Good.
- Admin bootstrap at `/api/v1/bootstrap` is rate-limited per source IP (1/5m default), guards on empty-principal-count precondition, uses `crypto/rand` for password + API-key.
- `internal/protoadmin/ratelimit.go` — per-principal sliding-window request limiter, concurrency semaphore at server level (default 512), panic-recover middleware, request-ID correlation. Defence-in-depth is present.

## Checklist trace

1. Input validation on wire input — PARTIAL. SMTP line cap 4 KiB, message cap 50 MiB, per-listener per-IP cap present. HELO bytes not constrained (medium finding).
2. TLS 1.2 + 1.3 only; Mozilla Intermediate; SNI per listener — PASS. `internal/tls/tls.go`.
3. Stdlib-only crypto; constant-time compares — PASS. Argon2id, HMAC-SHA256, PBKDF2, `crypto/subtle` used throughout.
4. Argon2id password storage, no downgrade paths — PASS. No MD5/SHA1/bcrypt anywhere in the password path.
5. Session tokens / API keys — PARTIAL. API keys are SHA-256-hashed at rest, `crypto/rand` generated, constant-time compared; the timing story depends on the storage backend (see medium finding). No session tokens yet — Phase 2.
6. OIDC RP — FAIL. Audience-confusion risk in multi-provider deployments; callback only completes Link, never Sign-In (high findings).
7. SASL plain-text rejected outside TLS; SCRAM channel binding — PARTIAL. Plain-text gating correct; `-PLUS` channel binding wire-up is a no-op stub (high finding).
8. Plugin isolation — PASS. Out-of-process, JSON-RPC on stdio, 16 MiB frame cap, no CGO, no in-process plugin loader.
9. Secret logging — PASS. `slog` redact handler; known-secret keys stripped; no observed secrets on URL paths or metric labels.
10. Audit on every config mutation — PARTIAL. `protoadmin` emits audit records on the mutating endpoints I checked; `appconfig.Import` does not (medium finding).
11. `unsafe.Pointer` — PASS. Zero uses.
12. Rate limiting — PASS. SMTP per-IP concurrency, SMTP command count, admin per-principal sliding window, bootstrap per-IP, directory auth-failure exponential cooldown.
13. SQL parameterisation — N/A to this review scope (delegated to storage review); no string-concat SQL spotted in the security-sensitive files read.
14. CSRF on state-changing admin endpoints — PASS. Phase 1 is Bearer-token only; no cookie-auth path; no CSRF concern. Confirmed `requireAuth` chain.
15. Cryptographic RNG on security tokens — PASS. `crypto/rand` for passwords, API keys, OIDC state, OIDC nonce, SCRAM nonce, TOTP secrets. `math/rand` only for non-security surfaces (plugin backoff jitter, IMAP UIDVALIDITY).
16. Timing attacks — PASS. `crypto/subtle.ConstantTimeCompare` used in password verify, TOTP verify, API-key hash compare, SCRAM proof compare, SCRAM channel-binding compare.
17. DoS surfaces — PASS. SPF has 10-lookup cap, DKIM has `MaxVerifications` cap, ARC has `MaxChainLength=50` cap, DMARC chains bounded, mailparse has depth/parts/size caps, JSON-RPC frame cap, HTTP body cap. No obvious O(n²) on wire input in the paths reviewed.
