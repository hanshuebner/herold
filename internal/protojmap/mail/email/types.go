package email

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the JMAP wire-form id (RFC 8620 §1.2).
type jmapID = string

// emailIDFromJMAP parses an Email id wire string into a MessageID.
// Empty / unparseable values return (0, false); the caller emits
// "notFound".
func emailIDFromJMAP(id jmapID) (store.MessageID, bool) {
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.MessageID(v), true
}

// jmapIDFromMessage renders a MessageID into wire form.
func jmapIDFromMessage(id store.MessageID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// mailboxIDFromJMAP parses a Mailbox id wire string into a MailboxID.
// Used by Email/set's mailboxIds and Email/query's inMailbox filter.
func mailboxIDFromJMAP(id jmapID) (store.MailboxID, bool) {
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.MailboxID(v), true
}

func jmapIDFromMailbox(id store.MailboxID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// jmapAddress is the wire-form EmailAddress (RFC 8621 §4.1.2.3).
type jmapAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// jmapEmail is the wire-form Email object (RFC 8621 §4.1). Properties
// the v1 store cannot derive without re-parsing the body (preview,
// htmlBody, attachments) are populated only when the request opts in
// via the "properties" / "fetchTextBodyValues" / "fetchHTMLBodyValues" /
// "fetchAllBodyValues" hints.
//
// Dynamic header property accessors (RFC 8621 §4.1.2.4) like
// "header:Subject:asText" are carried in HeaderProperties and
// serialised by jmapEmail.MarshalJSON.
type jmapEmail struct {
	ID         jmapID          `json:"id"`
	BlobID     string          `json:"blobId"`
	ThreadID   jmapID          `json:"threadId"`
	MailboxIDs map[jmapID]bool `json:"mailboxIds"`
	Keywords   map[string]bool `json:"keywords"`
	Size       int64           `json:"size"`
	ReceivedAt string          `json:"receivedAt"`
	// SnoozedUntil is the JMAP snooze extension wake-up deadline
	// (REQ-PROTO-49 / IETF JMAP Snooze draft). Pointer + json:"snoozedUntil"
	// so the field renders as `null` when the message is not snoozed
	// and as a UTC ISO-8601 string (RFC 8620 UTCDate) otherwise.
	SnoozedUntil *string `json:"snoozedUntil"`
	// Reactions is the email reactions extension property
	// (REQ-PROTO-100, capability https://netzhansa.com/jmap/email-reactions).
	// Shape: {"<emoji>": ["<principal-id>", ...], ...}. Sparse — emojis
	// with no current reactors are absent. Nil means "not loaded";
	// callers that set this non-nil but empty will render as {}.
	Reactions map[string][]string `json:"reactions,omitempty"`

	// Header form (RFC 8621 §4.1.2 + §4.1.3).
	From       []jmapAddress `json:"from,omitempty"`
	To         []jmapAddress `json:"to,omitempty"`
	Cc         []jmapAddress `json:"cc,omitempty"`
	Bcc        []jmapAddress `json:"bcc,omitempty"`
	ReplyTo    []jmapAddress `json:"replyTo,omitempty"`
	Sender     []jmapAddress `json:"sender,omitempty"`
	// Subject must not use omitempty: RFC 8621 requires "" for no-Subject emails.
	Subject    string        `json:"subject"`
	MessageID  []string      `json:"messageId,omitempty"`
	InReplyTo  []string      `json:"inReplyTo,omitempty"`
	References []string      `json:"references,omitempty"`
	SentAt     string        `json:"sentAt,omitempty"`

	// Body parts (RFC 8621 §4.1.4). Populated only on Email/parse and
	// when the caller asks for bodyStructure / bodyValues; the cheap
	// metadata path leaves these nil.
	BodyStructure *bodyPart            `json:"bodyStructure,omitempty"`
	BodyValues    map[string]bodyValue `json:"bodyValues,omitempty"`
	// TextBody, HTMLBody, Attachments carry full EmailBodyPart objects
	// (RFC 8621 §4.2 specifies these as arrays of EmailBodyPart).
	TextBody      []bodyPart           `json:"textBody,omitempty"`
	HTMLBody      []bodyPart           `json:"htmlBody,omitempty"`
	Attachments   []bodyPart           `json:"attachments,omitempty"`
	HasAttachment bool                 `json:"hasAttachment"`
	Preview       string               `json:"preview,omitempty"`

	// HeaderProperties holds the decoded values for dynamic header
	// property accessors (RFC 8621 §4.1.2.4) — keys like
	// "header:Subject:asText". Serialised directly into the JSON object
	// alongside the other fields by MarshalJSON.
	HeaderProperties map[string]json.RawMessage `json:"-"`
}

// jmapEmailWire is the JSON-serialisable alias; we copy all exported
// fields so that MarshalJSON can merge in HeaderProperties without
// reflection.
type jmapEmailWire struct {
	ID            jmapID               `json:"id"`
	BlobID        string               `json:"blobId"`
	ThreadID      jmapID               `json:"threadId"`
	MailboxIDs    map[jmapID]bool      `json:"mailboxIds"`
	Keywords      map[string]bool      `json:"keywords"`
	Size          int64                `json:"size"`
	ReceivedAt    string               `json:"receivedAt"`
	SnoozedUntil  *string              `json:"snoozedUntil"`
	Reactions     map[string][]string  `json:"reactions,omitempty"`
	From          []jmapAddress        `json:"from,omitempty"`
	To            []jmapAddress        `json:"to,omitempty"`
	Cc            []jmapAddress        `json:"cc,omitempty"`
	Bcc           []jmapAddress        `json:"bcc,omitempty"`
	ReplyTo       []jmapAddress        `json:"replyTo,omitempty"`
	Sender        []jmapAddress        `json:"sender,omitempty"`
	Subject       string               `json:"subject"`
	MessageID     []string             `json:"messageId,omitempty"`
	InReplyTo     []string             `json:"inReplyTo,omitempty"`
	References    []string             `json:"references,omitempty"`
	SentAt        string               `json:"sentAt,omitempty"`
	BodyStructure *bodyPart            `json:"bodyStructure,omitempty"`
	BodyValues    map[string]bodyValue `json:"bodyValues,omitempty"`
	TextBody      []bodyPart           `json:"textBody,omitempty"`
	HTMLBody      []bodyPart           `json:"htmlBody,omitempty"`
	Attachments   []bodyPart           `json:"attachments,omitempty"`
	HasAttachment bool                 `json:"hasAttachment"`
	Preview       string               `json:"preview,omitempty"`
}

// MarshalJSON serialises jmapEmail, merging HeaderProperties keys
// directly into the JSON object so they appear as top-level fields
// (e.g. `"header:Subject:asText": "..."`) per RFC 8621 §4.1.2.4.
func (e jmapEmail) MarshalJSON() ([]byte, error) {
	wire := jmapEmailWire{
		ID:            e.ID,
		BlobID:        e.BlobID,
		ThreadID:      e.ThreadID,
		MailboxIDs:    e.MailboxIDs,
		Keywords:      e.Keywords,
		Size:          e.Size,
		ReceivedAt:    e.ReceivedAt,
		SnoozedUntil:  e.SnoozedUntil,
		Reactions:     e.Reactions,
		From:          e.From,
		To:            e.To,
		Cc:            e.Cc,
		Bcc:           e.Bcc,
		ReplyTo:       e.ReplyTo,
		Sender:        e.Sender,
		Subject:       e.Subject,
		MessageID:     e.MessageID,
		InReplyTo:     e.InReplyTo,
		References:    e.References,
		SentAt:        e.SentAt,
		BodyStructure: e.BodyStructure,
		BodyValues:    e.BodyValues,
		TextBody:      e.TextBody,
		HTMLBody:      e.HTMLBody,
		Attachments:   e.Attachments,
		HasAttachment: e.HasAttachment,
		Preview:       e.Preview,
	}
	if len(e.HeaderProperties) == 0 {
		return json.Marshal(wire)
	}
	// Merge HeaderProperties into the object. We serialise wire first,
	// then inject the extra keys.
	base, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	if len(base) < 2 || base[0] != '{' {
		return base, nil
	}
	var extra []byte
	for k, v := range e.HeaderProperties {
		kJSON, _ := json.Marshal(k)
		extra = append(extra, ',')
		extra = append(extra, kJSON...)
		extra = append(extra, ':')
		extra = append(extra, v...)
	}
	if len(extra) == 0 {
		return base, nil
	}
	// Insert before the closing '}'.
	result := make([]byte, 0, len(base)+len(extra))
	result = append(result, base[:len(base)-1]...)
	result = append(result, extra...)
	result = append(result, '}')
	return result, nil
}

// bodyPart is the wire-form EmailBodyPart (RFC 8621 §4.1.4).
type bodyPart struct {
	PartID      *string          `json:"partId"`
	BlobID      *string          `json:"blobId"`
	Size        int64            `json:"size"`
	Headers     []bodyPartHeader `json:"headers,omitempty"`
	Name        *string          `json:"name"`
	Type        string           `json:"type,omitempty"`
	Charset     *string          `json:"charset,omitempty"`
	Disposition *string          `json:"disposition,omitempty"`
	Cid         *string          `json:"cid,omitempty"`
	Language    []string         `json:"language,omitempty"`
	Location    *string          `json:"location,omitempty"`
	SubParts    []bodyPart       `json:"subParts,omitempty"`
}

// bodyPartHeader is the per-part Header object (RFC 8621 §4.1.2.1).
type bodyPartHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// bodyValue is one entry in Email.bodyValues (RFC 8621 §4.1.4).
type bodyValue struct {
	Value             string `json:"value"`
	IsEncodingProblem bool   `json:"isEncodingProblem"`
	IsTruncated       bool   `json:"isTruncated"`
}


// keywordsFromMessage projects the IMAP system flag bitfield + keyword
// list onto the JMAP keyword map. The IMAP \Seen / \Answered / \Flagged
// / \Draft system flags map to "$seen" / "$answered" / "$flagged" /
// "$draft" per RFC 8621 §4.1.1.
func keywordsFromMessage(m store.Message) map[string]bool {
	out := map[string]bool{}
	if m.Flags&store.MessageFlagSeen != 0 {
		out["$seen"] = true
	}
	if m.Flags&store.MessageFlagAnswered != 0 {
		out["$answered"] = true
	}
	if m.Flags&store.MessageFlagFlagged != 0 {
		out["$flagged"] = true
	}
	if m.Flags&store.MessageFlagDraft != 0 {
		out["$draft"] = true
	}
	for _, kw := range m.Keywords {
		out[kw] = true
	}
	return out
}

// flagsAndKeywordsFromJMAP inverts keywordsFromMessage. Returns a flag
// bitfield and the user-defined keyword set the store should attach.
func flagsAndKeywordsFromJMAP(kws map[string]bool) (store.MessageFlags, []string) {
	var f store.MessageFlags
	var custom []string
	for k, present := range kws {
		if !present {
			continue
		}
		switch strings.ToLower(k) {
		case "$seen":
			f |= store.MessageFlagSeen
		case "$answered":
			f |= store.MessageFlagAnswered
		case "$flagged":
			f |= store.MessageFlagFlagged
		case "$draft":
			f |= store.MessageFlagDraft
		default:
			custom = append(custom, k)
		}
	}
	return f, custom
}

// requirePrincipal pulls the authenticated principal from ctx.
// Mirrors the mailbox helper; redeclared here so the email package is
// independent of mailbox.
func requirePrincipal(getter func() (store.PrincipalID, bool)) (store.PrincipalID, *protojmap.MethodError) {
	pid, ok := getter()
	if !ok || pid == 0 {
		return 0, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	return pid, nil
}

// requireAccount validates the JMAP accountId against the principal.
// An absent accountId is rejected with "invalidArguments" per RFC 8620
// §5.1: every method that operates on an account MUST carry the field.
func requireAccount(reqAccountID jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if reqAccountID == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if reqAccountID != protojmap.AccountIDForPrincipal(pid) {
		return protojmap.NewMethodError("accountNotFound",
			"account "+reqAccountID+" is not accessible to this principal")
	}
	return nil
}

// serverFail wraps an internal error in a JMAP serverFail envelope.
func serverFail(err error) *protojmap.MethodError {
	if err == nil {
		return nil
	}
	return protojmap.NewMethodError("serverFail", err.Error())
}
