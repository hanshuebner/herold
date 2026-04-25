// Package keymgmt manages DKIM signing keys: generation, rotation, and
// retirement. It is the single owner of DKIM private-key material at
// runtime; the maildkim signer and the mailarc sealer both look up active
// keys via Manager.ActiveKey, and the autodns publisher renders the DNS
// TXT record via Manager.PublishedRecord.
//
// Ownership: mail-auth-implementor.
package keymgmt
