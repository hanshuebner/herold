// Command heroldfakeoidc is a standalone deterministic OpenID Connect
// provider for dev instances. It implements the same fake IdP as
// internal/testfakes/fakeoidc (discovery, JWKS, /authorize, /token
// issuing a signed ID token) but as an independent process that
// scripts/dev-instance.sh can build and manage, distinct from
// heroldfakeidp: this one speaks full OIDC (with a real ID token) for
// internal/directoryoidc's per-user external federation and first-login
// auto-provisioning (REQ-AUTH-56), where heroldfakeidp is a plain
// OAuth 2 token endpoint for the external-submission-credential surface.
//
// On startup it binds to a kernel-picked port on 127.0.0.1, writes a
// key=value report file (--report-file), and blocks until SIGTERM or
// SIGINT. The report file contains the issuer URL and credentials
// dev-instance.sh needs to register the provider as an oidc_providers
// row via the admin REST API.
//
// A login's identity defaults to sub=dev-user,
// email=dev-user@fakeoidc.local (pre-verified) so a bare click-through
// of the /authorize URL always works; individual fields can be
// overridden per request via ?sub=&email=&name=&email_verified=
// query parameters, letting a maintainer at a browser pick an address
// without recompiling anything.
//
// Usage:
//
//	heroldfakeoidc [--client-id ID] [--client-secret SECRET] --report-file PATH
package main
