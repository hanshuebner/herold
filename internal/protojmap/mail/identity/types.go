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

// jmapIdentity is the wire-form Identity object (RFC 8621 §7.1).
type jmapIdentity struct {
	ID            jmapID         `json:"id"`
	Name          string         `json:"name"`
	Email         string         `json:"email"`
	ReplyTo       []emailAddress `json:"replyTo,omitempty"`
	Bcc           []emailAddress `json:"bcc,omitempty"`
	TextSignature string         `json:"textSignature,omitempty"`
	HTMLSignature string         `json:"htmlSignature,omitempty"`
	MayDelete     bool           `json:"mayDelete"`
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
	MayDelete     bool
	UpdatedAt     time.Time
}

func (r identityRecord) toJMAP() jmapIdentity {
	return jmapIdentity{
		ID:            renderID(r.ID),
		Name:          r.Name,
		Email:         r.Email,
		ReplyTo:       r.ReplyTo,
		Bcc:           r.Bcc,
		TextSignature: r.TextSignature,
		HTMLSignature: r.HTMLSignature,
		MayDelete:     r.MayDelete,
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
