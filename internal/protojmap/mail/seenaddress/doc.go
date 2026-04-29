// Package seenaddress implements the JMAP SeenAddress datatype
// (REQ-MAIL-11e..m). SeenAddress is exposed under the existing
// urn:ietf:params:jmap:mail capability; no new capability slug is needed.
//
// Methods:
//
//   - SeenAddress/get    — standard get; ids:null returns all entries.
//   - SeenAddress/changes — standard changes via change-feed cursor.
//   - SeenAddress/set    — destroy only; create and update are forbidden.
package seenaddress
