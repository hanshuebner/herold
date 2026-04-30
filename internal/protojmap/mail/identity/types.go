package identity

import (
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2).
type jmapID = string

// emailAddress is the JMAP "EmailAddress" object (RFC 8621 §4.1.2.3:
// {name, email}). Identity uses it for replyTo and bcc.
type emailAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// jmapIdentity is the wire-form Identity object (RFC 8621 §7.1) plus
// the "signature" extension property defined by REQ-PROTO-57 /
// REQ-STORE-35, and the "avatarBlobId" / "xFaceEnabled" extension
// properties defined by REQ-SET-03b.
type jmapIdentity struct {
	ID    jmapID `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	// ReplyTo and Bcc are RFC 8621 §6.1 nullable arrays. When not set
	// they must appear as JSON null (not absent). Use pointer-to-slice.
	ReplyTo       *[]emailAddress `json:"replyTo"`
	Bcc           *[]emailAddress `json:"bcc"`
	TextSignature string          `json:"textSignature"` // RFC 8621 §7.1: always present, defaults to ""
	HTMLSignature string          `json:"htmlSignature"` // RFC 8621 §7.1: always present, defaults to ""
	Signature     *string         `json:"signature"`
	MayDelete     bool            `json:"mayDelete"`
	// AvatarBlobId is the herold extension (REQ-SET-03b): the JMAP blob id
	// of this identity's avatar image, or null when not set. The blob must
	// have an image/* content-type; validation is enforced on set.
	AvatarBlobId *string `json:"avatarBlobId"`
	// XFaceEnabled controls outbound X-Face: / Face: header injection.
	// When true and AvatarBlobId is non-null, createEmail prepends those
	// headers derived from the avatar. Default false.
	XFaceEnabled bool `json:"xFaceEnabled"`
}

// identityRecord is the in-memory representation backing an Identity.
// The default per-principal identity is synthesized from the principal
// row; overrides and additional identities are stored in the in-process
// overlay (see Store).
type identityRecord struct {
	ID            uint64
	PrincipalID   store.PrincipalID
	Name          string
	Email         string
	ReplyTo       []emailAddress
	Bcc           []emailAddress
	TextSignature string
	HTMLSignature string
	// Signature is the plain-text Identity.signature extension
	// property (REQ-PROTO-57 / REQ-STORE-35). Nil means unset.
	Signature *string
	// AvatarBlobHash is the BLAKE3 hex hash of the identity's avatar blob.
	// Empty string when no avatar is set (REQ-SET-03b).
	AvatarBlobHash string
	// AvatarBlobSize is the byte size of the avatar blob.
	AvatarBlobSize int64
	// XFaceEnabled controls outbound X-Face: / Face: injection (REQ-SET-03b).
	XFaceEnabled bool
	MayDelete    bool
	UpdatedAt    time.Time
}

func (r identityRecord) toJMAP() jmapIdentity {
	var sig *string
	if r.Signature != nil {
		v := *r.Signature
		sig = &v
	}
	// RFC 8621 §6.1: replyTo and bcc are null when not set (not absent).
	var replyTo *[]emailAddress
	if len(r.ReplyTo) > 0 {
		rt := append([]emailAddress(nil), r.ReplyTo...)
		replyTo = &rt
	}
	var bcc *[]emailAddress
	if len(r.Bcc) > 0 {
		b := append([]emailAddress(nil), r.Bcc...)
		bcc = &b
	}
	// avatarBlobId is null on the wire when not set.
	var avatarBlobId *string
	if r.AvatarBlobHash != "" {
		v := r.AvatarBlobHash
		avatarBlobId = &v
	}
	return jmapIdentity{
		ID:            renderID(r.ID),
		Name:          r.Name,
		Email:         r.Email,
		ReplyTo:       replyTo,
		Bcc:           bcc,
		TextSignature: r.TextSignature,
		HTMLSignature: r.HTMLSignature,
		Signature:     sig,
		MayDelete:     r.MayDelete,
		AvatarBlobId:  avatarBlobId,
		XFaceEnabled:  r.XFaceEnabled,
	}
}

// renderID stringifies an internal identity id; the principal-default
// identity uses id "default" (a stable opaque token clients echo back
// across syncs). Custom identities use their numeric overlay id as a
// decimal string.
func renderID(id uint64) jmapID {
	if id == 0 {
		return "default"
	}
	return strconv.FormatUint(id, 10)
}

// parseID inverts renderID. The literal "default" maps to 0; numeric
// strings parse as their uint64 value. Any other input returns ok=false.
func parseID(s jmapID) (uint64, bool) {
	if s == "default" {
		return 0, true
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return v, true
}

// localPartAndDomain splits an RFC 5322 addr-spec into local-part and
// domain. Returns ok=false for malformed inputs (no @, empty parts).
// The domain is lowercased; the local-part is preserved verbatim.
func localPartAndDomain(email string) (localPart, domain string, ok bool) {
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return "", "", false
	}
	return email[:at], strings.ToLower(email[at+1:]), true
}
