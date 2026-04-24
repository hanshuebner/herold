// Package directoryoidc implements per-user external OIDC federation
// as a relying party (NG11: we are not an issuer).
//
// The RP manages operator-registered OIDC providers and per-user
// principal↔provider links keyed on the provider's "sub" claim. It
// exposes the two flows described in REQ-AUTH-50..58:
//
//   - BeginLink / CompleteLink: an authenticated local user attaches an
//     external identity to their principal.
//   - BeginSignIn / CompleteSignIn: an unauthenticated user signs in
//     via an already-linked identity; the server resolves the provider
//     sub to a local PrincipalID.
//
// The RP never mints tokens; REQ-AUTH-58 excludes us from acting as an
// OIDC issuer. All ID-token signature verification delegates to
// github.com/coreos/go-oidc/v3/oidc. Ownership: directory-auth-implementor.
package directoryoidc
