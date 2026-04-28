// Package authsession owns the listener-keyed session HMAC envelope,
// cookie issuance, CSRF token generation, and session decoding.
//
// It is used by internal/protoadmin (admin-listener REST cookie auth),
// internal/protologin (public-listener JSON login), and by the
// stateless resolver helpers (ResolveSession, ResolveSessionWithScope)
// that protoimg, protochat, protocall, and protojmap use to validate
// public-listener cookies without a server lifecycle dependency.
//
// This package has no dependency on internal/protoadmin or
// internal/protologin; the dependency arrow always points inward.
package authsession
