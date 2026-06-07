// Package protoshare implements the public, unauthenticated HTTP surface
// that serves attachment shares to external recipients. Routes are mounted
// on the public listener (not the admin listener) and require no session or
// API-key credential — the capability URL itself is the bearer token
// (REQ-SHARE-11a).
//
// Three routes:
//
//	GET  /share/{id}          — landing page (HTML): filename, size, expiry,
//	                            password field if locked, Download button.
//	GET  /share/{id}/download — stream bytes with range support.
//	POST /share/{id}/unlock   — verify password, mint unlock cookie, redirect.
//
// Serveability semantics (REQ-SHARE-11e): state==active AND now<ExpiresAt AND
// not revoked AND (MaxDownloads==nil OR DownloadCount<MaxDownloads) AND
// (no password OR valid unlock cookie). Every not-serveable case returns an
// identical 410 Gone — no distinguishing body or timing.
//
// Rate limiting (REQ-SHARE-32): in-process per-(source-IP, share-id) sliding
// window. On exceed: 429 + Retry-After. The full share ID is never written to
// logs; a truncated prefix is used instead (REQ-SHARE-11d).
//
// Owner: http-api-implementor.
package protoshare
