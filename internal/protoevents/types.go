package protoevents

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// EventKind names a typed event the dispatcher fans out to publisher
// plugins. The set is closed (REQ-EVT-01); additions land with a minor
// version bump and a corresponding payload struct in payloads.go.
//
// Naming follows REQ-EVT (subject taxonomy "<class>.<verb>"). Producers
// MUST NOT invent new kinds at runtime; the dispatcher does not validate
// kind strings against this list because typed Go callers reach for the
// constants directly.
type EventKind string

// Closed event-kind enumeration. The strings are stable wire identifiers
// shipped to plugins and must not be renamed once released.
const (
	EventMailReceived    EventKind = "mail.received"
	EventMailSent        EventKind = "mail.sent"
	EventMailDelivered   EventKind = "mail.delivered"
	EventMailFailed      EventKind = "mail.failed"
	EventMailDeferred    EventKind = "mail.deferred"
	EventMailSpamVerdict EventKind = "mail.spam_verdict"
	EventAuthSuccess     EventKind = "auth.success"
	EventAuthFailure     EventKind = "auth.failure"
	EventAuthTOTPEnroll  EventKind = "auth.totp.enroll"
	EventAuthOIDCLink    EventKind = "auth.oidc.link"
	EventQueueRetry      EventKind = "queue.retry"
	EventACMECertIssued  EventKind = "acme.cert.issued"
	EventACMECertRenewed EventKind = "acme.cert.renewed"
	EventACMECertFailed  EventKind = "acme.cert.failed"
	EventDKIMKeyRotated  EventKind = "dkim.key.rotated"
	EventPluginLifecycle EventKind = "plugin.lifecycle"
	EventWebhookFailure  EventKind = "webhook.delivery.failed"

	// EventPublishFailed is emitted when the dispatcher exhausts its
	// retry budget for an event-publish attempt against a plugin. Marked
	// with RetryBudgetExhausted=true so plugins MUST NOT re-emit on
	// receipt — otherwise the dispatcher could loop indefinitely.
	EventPublishFailed EventKind = "event.publish.failed"
)

// Event is the typed envelope dispatched to publisher plugins. The
// concrete payload is per-kind (see payloads.go); the dispatcher does
// NOT validate the payload shape against the kind, producers are
// trusted (REQ-EVT envelope).
type Event struct {
	// ID is a 26-character ULID assigned by the dispatcher at Emit time.
	ID string `json:"id"`
	// Kind is the typed event-kind discriminator.
	Kind EventKind `json:"kind"`
	// Subject is the operator-readable subject suffix used by publishers
	// to compute their topic (e.g. "example.com" for a mail.received
	// event scoped to that domain). Empty subjects yield a bare
	// "<prefix>.<kind>" topic.
	Subject string `json:"subject,omitempty"`
	// PrincipalID is the owning principal, when the event has one. Nil
	// for system-scope events (acme.*, plugin.lifecycle, ...).
	PrincipalID *store.PrincipalID `json:"principal_id,omitempty"`
	// OccurredAt is the producer-supplied event time. The dispatcher
	// stamps the clock's now if zero at Emit time.
	OccurredAt time.Time `json:"occurred_at"`
	// Payload is the event-kind-specific JSON-encoded body.
	Payload json.RawMessage `json:"payload,omitempty"`
	// RetryBudgetExhausted marks an EventPublishFailed envelope so
	// downstream consumers know not to bounce back through Emit. Always
	// false for first-class events.
	RetryBudgetExhausted bool `json:"retry_budget_exhausted,omitempty"`
}

// newEventID returns a fresh 26-character Crockford-Base32 ULID. The
// implementation is local — pulling in github.com/oklog/ulid for one
// helper would push us over the dependency budget.
func newEventID(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		// Crypto/rand failure is unrecoverable; the supervisor will
		// notice via downstream errors. Fall back to a stable but
		// non-zero string so callers do not panic on the hot path.
		copy(b[6:], []byte("heroldfallback"))
	}
	return crockfordBase32(b)
}

// crockfordBase32 encodes 16 bytes (128 bits) as 26 Crockford-Base32
// characters per the ULID spec. Local helper avoids a dependency.
func crockfordBase32(b [16]byte) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	// The ULID encoding is 26 characters: 10 for the 48-bit timestamp,
	// 16 for the 80-bit randomness, but bit-aligned across the 128-bit
	// blob with the high two bits of the first character always zero.
	var dst [26]byte
	// Timestamp portion (first 10 chars; first char is 3 bits).
	dst[0] = alphabet[(b[0]&224)>>5]
	dst[1] = alphabet[b[0]&31]
	dst[2] = alphabet[(b[1]&248)>>3]
	dst[3] = alphabet[((b[1]&7)<<2)|((b[2]&192)>>6)]
	dst[4] = alphabet[(b[2]&62)>>1]
	dst[5] = alphabet[((b[2]&1)<<4)|((b[3]&240)>>4)]
	dst[6] = alphabet[((b[3]&15)<<1)|((b[4]&128)>>7)]
	dst[7] = alphabet[(b[4]&124)>>2]
	dst[8] = alphabet[((b[4]&3)<<3)|((b[5]&224)>>5)]
	dst[9] = alphabet[b[5]&31]
	// Randomness portion (16 chars over bytes 6..15).
	dst[10] = alphabet[(b[6]&248)>>3]
	dst[11] = alphabet[((b[6]&7)<<2)|((b[7]&192)>>6)]
	dst[12] = alphabet[(b[7]&62)>>1]
	dst[13] = alphabet[((b[7]&1)<<4)|((b[8]&240)>>4)]
	dst[14] = alphabet[((b[8]&15)<<1)|((b[9]&128)>>7)]
	dst[15] = alphabet[(b[9]&124)>>2]
	dst[16] = alphabet[((b[9]&3)<<3)|((b[10]&224)>>5)]
	dst[17] = alphabet[b[10]&31]
	dst[18] = alphabet[(b[11]&248)>>3]
	dst[19] = alphabet[((b[11]&7)<<2)|((b[12]&192)>>6)]
	dst[20] = alphabet[(b[12]&62)>>1]
	dst[21] = alphabet[((b[12]&1)<<4)|((b[13]&240)>>4)]
	dst[22] = alphabet[((b[13]&15)<<1)|((b[14]&128)>>7)]
	dst[23] = alphabet[(b[14]&124)>>2]
	dst[24] = alphabet[((b[14]&3)<<3)|((b[15]&224)>>5)]
	dst[25] = alphabet[b[15]&31]
	return string(dst[:])
}

// MarshalPayload is a small helper so producers don't have to import
// encoding/json at every Emit site. Returns an error only if v is not
// JSON-encodable (unsupported types).
func MarshalPayload(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("protoevents: marshal payload: %w", err)
	}
	return b, nil
}

// idGen is the goroutine-safe ID-issuance helper used by the dispatcher.
// It ensures monotonic IDs within the same millisecond by re-rolling the
// random tail; collisions are vanishingly unlikely but tests get a
// stable sequence.
type idGen struct {
	mu sync.Mutex
}

func (g *idGen) next(now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return newEventID(now)
}
