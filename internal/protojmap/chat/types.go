package chat

import (
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2: 1..255 printable
// ASCII). Conversation, Message, Membership ids are stringified store
// ids; clients echo them back unchanged on subsequent calls.
type jmapID = string

// conversationIDFromJMAP parses a wire-form id into a store.ConversationID.
// Empty / unparseable / zero values return (0, false); callers translate
// to a "notFound" SetError.
func conversationIDFromJMAP(id jmapID) (store.ConversationID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.ConversationID(v), true
}

// jmapIDFromConversation renders a store.ConversationID as the wire id.
func jmapIDFromConversation(id store.ConversationID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// messageIDFromJMAP parses a wire-form id into a store.ChatMessageID.
func messageIDFromJMAP(id jmapID) (store.ChatMessageID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.ChatMessageID(v), true
}

// jmapIDFromMessage renders a store.ChatMessageID as the wire id.
func jmapIDFromMessage(id store.ChatMessageID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// membershipIDFromJMAP parses a wire-form id into a store.MembershipID.
func membershipIDFromJMAP(id jmapID) (store.MembershipID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.MembershipID(v), true
}

// jmapIDFromMembership renders a store.MembershipID as the wire id.
func jmapIDFromMembership(id store.MembershipID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// principalIDFromJMAP parses a stringified principal id. Returns
// (0, false) on invalid input.
func principalIDFromJMAP(id jmapID) (store.PrincipalID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.PrincipalID(v), true
}

// jmapIDFromPrincipal renders a principal id as the wire form.
func jmapIDFromPrincipal(id store.PrincipalID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// rfc3339Layout is the timestamp layout used by every chat-side
// rendering helper. JMAP timestamps are RFC 3339 strings; the chat
// datatype follows the convention.
const rfc3339Layout = "2006-01-02T15:04:05Z07:00"

// rfc3339OrNilFromPtr returns *t formatted as RFC 3339 when t is
// non-nil, or nil for a JSON-null projection.
func rfc3339OrNilFromPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(rfc3339Layout)
}

// int64PtrOrNil returns *p as a JSON number when non-nil, or nil for a
// JSON-null projection. Used by Conversation rendering for nullable
// retentionSeconds / editWindowSeconds fields where null means "use
// the account default" and 0 has its own distinct meaning per the
// store.ChatConversation field docs.
func int64PtrOrNil(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// jmapMessageBody is the body envelope on a wire-form Message.
type jmapMessageBody struct {
	Text   string `json:"text"`
	HTML   string `json:"html,omitempty"`
	Format string `json:"format"`
}

// jmapAttachment is the wire form of one entry in Message.attachments.
type jmapAttachment struct {
	BlobID      string `json:"blobId"`
	ContentType string `json:"contentType"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
}

// jmapConversationMember is the denormalised member entry returned in
// Conversation.members. Convenience join over chat_memberships so a
// client renders a conversation header without a separate round trip.
// DisplayName is populated by renderConversation via principalDisplayName
// so message list renderers can label senders without a Principal/get call.
type jmapConversationMember struct {
	PrincipalID jmapID `json:"principalId"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	JoinedAt    string `json:"joinedAt"`
	IsMuted     bool   `json:"isMuted"`
}

// jmapConversation is the wire-form Conversation object. Per
// REQ-CHAT-02: id, kind, name, topic, lastMessageAt, messageCount,
// isArchived, members, myMembership, unreadCount.
//
// Wave 2.9.6 additive properties (REQ-CHAT-20/32/92):
//   - readReceiptsEnabled (REQ-CHAT-32) — Spaces only; DMs always
//     true on the wire. Server-side mutable on Spaces via
//     Conversation/set { update }.
//   - retentionSeconds (REQ-CHAT-92) — *int64 pointer in store form;
//     wire form uses any so a JSON null distinguishes "use account
//     default" from "never expire" (0).
//   - editWindowSeconds (REQ-CHAT-20) — same nil-vs-0 distinction as
//     retentionSeconds.
type jmapConversation struct {
	ID                  jmapID                   `json:"id"`
	Kind                string                   `json:"kind"`
	Name                string                   `json:"name"`
	Topic               string                   `json:"topic"`
	LastMessageAt       any                      `json:"lastMessageAt"`
	MessageCount        int                      `json:"messageCount"`
	IsArchived          bool                     `json:"isArchived"`
	ReadReceiptsEnabled bool                     `json:"readReceiptsEnabled"`
	RetentionSeconds    any                      `json:"retentionSeconds"`
	EditWindowSeconds   any                      `json:"editWindowSeconds"`
	Members             []jmapConversationMember `json:"members"`
	MyMembership        *jmapMembership          `json:"myMembership"`
	UnreadCount         int                      `json:"unreadCount"`
}

// jmapMembership is the wire-form Membership object.
type jmapMembership struct {
	ID                   jmapID `json:"id"`
	ConversationID       jmapID `json:"conversationId"`
	PrincipalID          jmapID `json:"principalId"`
	Role                 string `json:"role"`
	JoinedAt             string `json:"joinedAt"`
	LastReadMessageID    jmapID `json:"lastReadMessageId"`
	IsMuted              bool   `json:"isMuted"`
	MuteUntil            any    `json:"muteUntil"`
	NotificationsSetting string `json:"notificationsSetting"`
}

// jmapMessage is the wire-form Message object.
type jmapMessage struct {
	ID                jmapID              `json:"id"`
	ConversationID    jmapID              `json:"conversationId"`
	SenderPrincipalID any                 `json:"senderPrincipalId"`
	IsSystem          bool                `json:"isSystem"`
	Body              jmapMessageBody     `json:"body"`
	ReplyToMessageID  any                 `json:"replyToMessageId"`
	Reactions         map[string][]string `json:"reactions"`
	Attachments       []jmapAttachment    `json:"attachments"`
	LinkPreviews      []jmapLinkPreview   `json:"linkPreviews,omitempty"`
	Metadata          any                 `json:"metadata"`
	EditedAt          any                 `json:"editedAt"`
	DeletedAt         any                 `json:"deletedAt"`
	CreatedAt         string              `json:"createdAt"`
}

// jmapLinkPreview is the wire shape of a single link-preview card
// attached to a Message. Populated server-side by the linkpreview
// fetcher when the dispatcher detects URLs in body.text; clients
// render it as a card under the message body. Empty fields mean the
// upstream did not advertise the value; the renderer is expected to
// gracefully omit them.
type jmapLinkPreview struct {
	URL          string `json:"url"`
	CanonicalURL string `json:"canonicalUrl,omitempty"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	ImageURL     string `json:"imageUrl,omitempty"`
	SiteName     string `json:"siteName,omitempty"`
}

// jmapBlock is the wire-form Block object.
type jmapBlock struct {
	ID                 jmapID `json:"id"`
	BlockedPrincipalID jmapID `json:"blockedPrincipalId"`
	CreatedAt          string `json:"createdAt"`
}

// blockIDFromBlocked renders a (blocker, blocked) pair as a stable wire
// id. Block rows are keyed by the (blocker, blocked) pair; for JMAP we
// surface the blocked principal id as the row id since the blocker is
// always the requesting principal.
func blockIDFromBlocked(blocked store.PrincipalID) jmapID {
	return strconv.FormatUint(uint64(blocked), 10)
}

// blockIDToBlocked inverts blockIDFromBlocked.
func blockIDToBlocked(id jmapID) (store.PrincipalID, bool) {
	return principalIDFromJMAP(id)
}

// setError is the JMAP per-record SetError envelope.
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// comparator is the JMAP /query sort comparator.
type comparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending"`
	Collation   string `json:"collation,omitempty"`
}
