package mail

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/protojmap/mail/mailbox"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs every Wave 2.2 JMAP Mail (RFC 8621) datatype
// handler this package owns — Mailbox/* and Email/* — onto reg. Called
// from internal/admin/server.go's StartServer wiring; the parallel
// agent's smaller-types package contributes EmailSubmission, Identity,
// Thread, SearchSnippet, and VacationResponse via its own Register.
//
// Datatype packages may also be registered individually by their own
// Register entry points; this top-level helper exists so the boot
// path has a single hook for the two big types.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	mailbox.Register(reg, st, logger, clk)
	email.Register(reg, st, logger, clk)
	// Per RFC 8621 §1, the JMAP Mail capability descriptor advertises
	// per-account hints (mayCreateTopLevelMailbox, maxMailboxesPerEmail,
	// emailQuerySortOptions). v1 publishes a conservative descriptor:
	// Mail capability is registered as an empty object on the server-
	// wide map (the dispatcher already auto-fills {} for capabilities
	// without a descriptor) and Register installs the per-account
	// provider so the session endpoint reflects the actual operator
	// posture.
	reg.RegisterAccountCapability(protojmap.CapabilityMail, mailAccountCapability{})
}

// mailAccountCapability is the per-account capability provider. RFC 8621
// §1 lists the fields verbatim; we publish the conservative defaults
// that match the v1 storage surface (one mailbox per email, no
// tree-restriction beyond name uniqueness, modest per-call limits).
type mailAccountCapability struct{}

func (mailAccountCapability) AccountCapability() any {
	return mailAccountCapabilityBody{
		MaxMailboxesPerEmail:       1,
		MaxMailboxDepth:            nil,
		MaxSizeMailboxName:         255,
		MaxSizeAttachmentsPerEmail: 50 * 1024 * 1024,
		EmailQuerySortOptions:      []string{"receivedAt", "sentAt", "size", "from", "to", "subject"},
		MayCreateTopLevelMailbox:   true,
	}
}

// mailAccountCapabilityBody is the JSON-marshaled body. Pointer fields
// encode "no limit" with an explicit null per RFC 8621 §1.
type mailAccountCapabilityBody struct {
	MaxMailboxesPerEmail       int      `json:"maxMailboxesPerEmail"`
	MaxMailboxDepth            *int     `json:"maxMailboxDepth"`
	MaxSizeMailboxName         int      `json:"maxSizeMailboxName"`
	MaxSizeAttachmentsPerEmail int64    `json:"maxSizeAttachmentsPerEmail"`
	EmailQuerySortOptions      []string `json:"emailQuerySortOptions"`
	MayCreateTopLevelMailbox   bool     `json:"mayCreateTopLevelMailbox"`
}
