package push

import (
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id. PushSubscription ids are the
// stringified PushSubscriptionID. Clients echo them back unchanged on
// subsequent calls.
type jmapID = string

// pushIDFromJMAP parses a wire id into a PushSubscriptionID. Empty
// strings and unparseable values return (0, false); callers translate
// that to a "notFound" SetError.
func pushIDFromJMAP(id jmapID) (store.PushSubscriptionID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.PushSubscriptionID(v), true
}

// jmapIDFromPush renders a PushSubscriptionID into the wire id form.
func jmapIDFromPush(id store.PushSubscriptionID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// jmapKeys is the nested object RFC 8620 §7.2 names "keys": the
// RFC 8291 P-256 ECDH public key (p256dh) plus the 16-byte auth
// secret (auth), both base64url-encoded on the wire. The store keeps
// the bytes raw; the serialiser handles the encoding round-trip.
type jmapKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// jmapQuietHours is the suite extension shape for the per-account
// quiet-hours pair (REQ-PROTO-121). null on the wire when the
// principal has not configured quiet hours.
type jmapQuietHours struct {
	StartHourLocal int    `json:"startHourLocal"`
	EndHourLocal   int    `json:"endHourLocal"`
	TZ             string `json:"tz"`
}

// jmapPushSubscription is the wire-form PushSubscription object
// (RFC 8620 §7.2 + REQ-PROTO-121 extensions). Every field is named
// per the spec / suite contract; the omitempty handling for the
// extension fields keeps responses small for clients that did not
// register them.
type jmapPushSubscription struct {
	ID                     jmapID          `json:"id"`
	DeviceClientID         string          `json:"deviceClientId"`
	URL                    string          `json:"url"`
	Keys                   jmapKeys        `json:"keys"`
	VerificationCode       *string         `json:"verificationCode,omitempty"`
	Expires                *string         `json:"expires"`
	Types                  []string        `json:"types"`
	NotificationRules      any             `json:"notificationRules,omitempty"`
	QuietHours             *jmapQuietHours `json:"quietHours,omitempty"`
	VAPIDKeyAtRegistration string          `json:"vapidKeyAtRegistration,omitempty"`
}
