// Package authsession owns the listener-keyed session HMAC envelope,
// cookie issuance, CSRF token generation, and session decoding.
//
// It is used by internal/protoui (HTML form login flow, until Phase 3b
// deletes it) and by internal/protoadmin (REST cookie auth). Extracting
// the shared primitives here breaks the protoadmin -> protoui import
// dependency so Phase 3b can delete internal/protoui wholesale without
// taking protoadmin down with it.
//
// This package has no dependency on internal/protoui.
package authsession
