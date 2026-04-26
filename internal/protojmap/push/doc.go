// Package push implements the JMAP PushSubscription datatype handlers
// (REQ-PROTO-120..122 / RFC 8620 §7.2) plus the tabard-flavoured
// extension properties (REQ-PROTO-121: notificationRules, quietHours,
// vapidKeyAtRegistration).
//
// Wave 3.8a delivers persistence + the JMAP wire surface only. The
// outbound push dispatcher (REQ-PROTO-123..126) lands in 3.8b and
// the notificationRules evaluation engine (REQ-PROTO-127) in 3.8c —
// this package leaves clearly marked TODO(3.8b-coord) /
// TODO(3.8c-coord) markers at the integration points so the next
// wave's PR diffs locally.
//
// Methods. Two: PushSubscription/get and PushSubscription/set. Both
// follow the standard RFC 8620 §5.1 / §5.3 shape; the set handler
// supports create / update / destroy. Per RFC 8620 §7.2 most fields
// are immutable post-create — clients update only expires, types,
// the verification handshake's verificationCode, plus the tabard
// extension fields (notificationRules, quietHours).
//
// Authorisation. Subscriptions are private to their owning principal:
// a /get or /set against another principal's accountId returns
// "accountNotFound", and a row whose principal_id does not match the
// caller is invisible. Admin scope grants no additional read; the
// dispatcher (3.8b) reads rows directly via the store.
//
// State string. The handlers bump JMAPStateKindPushSubscription on
// every successful create / update / destroy; the resulting decimal
// counter is what /changes (and any future EventSource subscription
// to the "PushSubscription" type name) reports.
package push
