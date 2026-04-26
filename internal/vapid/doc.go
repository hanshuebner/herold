// Package vapid manages the deployment-level VAPID (RFC 8292) key
// pair the Web Push outbound dispatcher uses to authenticate to push
// gateways. Phase 3 Wave 3.8a (REQ-PROTO-122) ships only key
// management + capability advertisement; the JWT-signing path lands
// in 3.8b alongside the dispatcher.
//
// Operator config. The private key is referenced from
// internal/sysconfig.PushConfig via a $VAR or file:/path secret
// reference (STANDARDS §9). When the reference is empty the deployment
// has no VAPID configured and Web Push is disabled — the JMAP session
// descriptor advertises the https://tabard.dev/jmap/push capability
// without an applicationServerKey field, signalling clients that push
// is unavailable.
//
// Why one key pair per deployment, not per principal. RFC 8292 §2
// allows a server to use multiple keys, but every real-world deployment
// uses a single static pair: the VAPID public key acts like a stable
// "this is herold X" identifier across all subscriptions, and rotating
// per-principal keys would invalidate every existing subscription.
// REQ-PROTO-122 frames this as a deployment-level concern; tabard
// reads the active public key from the session descriptor and supplies
// it to ServiceWorker.pushManager.subscribe() at registration time.
//
// Operator key rotation. Generating a fresh key pair (herold vapid
// generate) and re-pointing the secret reference invalidates every
// existing subscription on the next push attempt — the gateway returns
// HTTP 410/404, the dispatcher destroys the row, and the client re-
// subscribes against the new key. The rotation is therefore a manual
// operator action; herold does not auto-rotate.
package vapid
