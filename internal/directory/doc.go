// Package directory implements the internal directory: principals, aliases,
// groups, credentials, quotas, forwarding.
//
// The Directory is Herold's sole built-in identity backend
// (REQ-AUTH-10). It owns principal CRUD, password hashing (Argon2id per
// STANDARDS.md §9), TOTP enrollment and verification, and address →
// principal resolution. It is consumed by SMTP submission, IMAP LOGIN,
// JMAP, and the admin API via narrow interfaces (see internal/sasl for
// the Authenticator surface).
//
// Ownership: directory-auth-implementor.
package directory
