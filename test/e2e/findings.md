# Phase 1 e2e findings

One entry per bug observed while writing the Phase 1 e2e suite. Severity
rubric: blocker = exit criterion unmet; major = exit criterion met but
contract broken; minor = cosmetic or ergonomic.

(Finding 11 closed in Wave 4.5 by stamping the spam verdict into the
stored `Authentication-Results:` header as an `x-herold-spam=<verdict>`
method token. See `internal/protosmtp/deliver.go` and
`TestPhase1_LLMClassifier_HeaderStamped` for the regression guard.)

## R53-1 — Route53 hosted-zone discovery is not cached (minor)

`plugins/herold-dns-route53/main.go` `resolveHostedZone` re-queries
`ListHostedZonesByName` on every dns.* call when `hosted_zone_id` is
unset, instead of caching the discovered id back into
`h.opts.hostedZoneID`. ACME issuance against an unconfigured zone
therefore pays an extra Route53 list call per challenge. Cloudflare and
Hetzner have the same shape (also re-resolve), so the gap is consistent
across providers.

Test guard: `TestAutoDiscoverZoneID` in
`plugins/herold-dns-route53/plugin_test.go` asserts the present-time
behaviour (lookup + create on each call). Flip the assertion when the
plugin is updated to memoise the discovered id.
