package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// This file declares the Phase-2 Wave 2.8 entities backing the JMAP
// chat subsystem (REQ-CHAT-*). The schema-side commentary lives in
// internal/storesqlite/migrations/0012_chat.sql; this file is the
// Go-side companion.
//
// Storage strategy. Five new tables:
//
//   - chat_conversations: one row per DM or Space.
//   - chat_memberships  : per-principal membership of a conversation.
//   - chat_messages     : individual messages in a conversation.
//   - chat_blocks       : blocker/blocked principal pairs.
//
// Reactions and attachments live as JSON columns on chat_messages
// (ReactionsJSON, AttachmentsJSON). The Go layer enforces the
// reaction-emoji and attachment caps; CHECK constraints in SQL would
// fragment the schema across SQLite vs Postgres so we keep the
// validation in code.
//
// State-change feed integration. Every mutation appends a state_changes
// row in the same tx with one of the new EntityKind values:
// EntityKindConversation, EntityKindChatMessage, EntityKindMembership.
// ParentEntityID for chat-Message and Membership = conversation_id; for
// Conversation = 0. The architecture doc spells this out alongside the
// JMAP datatype names.
//
// Counter naming. The existing email-Email JMAP datatype already owns
// the EntityKindEmail name and the email_state column on jmap_states.
// Chat introduces a separate "Message" datatype (REQ-CHAT-03) whose
// state-string is tracked in the new message_chat_state column;
// EntityKindChatMessage names the entity-kind variant. Reusing
// EntityKindEmail or column email_state would collide the two
// datatypes' state strings on the wire and confuse Email/changes vs
// Message/changes on a shared session — hence the dual-meaning split.

// ConversationID identifies one row in the chat_conversations table.
type ConversationID uint64

// ChatMessageID identifies one row in the chat_messages table.
// Distinct alias from MessageID so callers cannot accidentally pass a
// mail-message ID where a chat-message ID is expected.
type ChatMessageID uint64

// MembershipID identifies one row in the chat_memberships table.
type MembershipID uint64

// ChatConversationKind enumerates the values stored in
// chat_conversations.kind. Strings are stable wire / storage tokens.
const (
	// ChatConversationKindDM is a 1:1 direct message conversation
	// (REQ-CHAT-10). Created server-side on first message between two
	// principals; subsequent DMs reuse the existing conversation.
	ChatConversationKindDM = "dm"
	// ChatConversationKindSpace is a multi-member group conversation
	// (REQ-CHAT-11). Created via Conversation/set { create, type:
	// "space" }; the creator becomes admin.
	ChatConversationKindSpace = "space"
)

// Chat membership role tokens. Stored verbatim in chat_memberships.role.
const (
	// ChatRoleMember is a regular conversation participant.
	ChatRoleMember = "member"
	// ChatRoleAdmin is a space administrator (REQ-CHAT-11/14).
	ChatRoleAdmin = "admin"
	// ChatRoleOwner is reserved for future "Slack-style" creator
	// elevation; v1 promotes to admin only, but the column accepts
	// owner so a later wave does not need a migration.
	ChatRoleOwner = "owner"
)

// Chat body-format tokens. Stored verbatim in chat_messages.body_format.
const (
	ChatBodyFormatText     = "text"
	ChatBodyFormatMarkdown = "markdown"
	ChatBodyFormatHTML     = "html"
)

// Chat per-membership notifications setting tokens. Stored verbatim in
// chat_memberships.notifications_setting.
const (
	ChatNotificationsAll      = "all"
	ChatNotificationsMentions = "mentions"
	ChatNotificationsNone     = "none"
)

// ChatConversation is one row in the chat_conversations table.
type ChatConversation struct {
	// ID is the assigned primary key.
	ID ConversationID
	// Kind is "dm" or "space".
	Kind string
	// Name is the space's display name; empty for DMs (the client
	// computes the DM label from members).
	Name string
	// Topic is an optional short description for spaces.
	Topic string
	// CreatedByPrincipalID is the principal that created the
	// conversation. For DMs this is the first sender.
	CreatedByPrincipalID PrincipalID
	// CreatedAt is the row insert instant.
	CreatedAt time.Time
	// UpdatedAt is the most recent mutation instant.
	UpdatedAt time.Time
	// LastMessageAt is the timestamp of the most recent message;
	// nil when the conversation carries no messages yet.
	// Denormalised from chat_messages so list-by-recent does not need
	// a per-row aggregate.
	LastMessageAt *time.Time
	// MessageCount is the denormalised count of non-deleted messages
	// in the conversation. Maintained by the metadata layer.
	MessageCount int
	// IsArchived suppresses the conversation from default views.
	IsArchived bool
	// ReadReceiptsEnabled toggles whether members of a Space see each
	// other's read pointers via Membership/get (REQ-CHAT-32). DMs
	// ignore the flag and always expose receipts (REQ-CHAT-31).
	// Default: true.
	ReadReceiptsEnabled bool
	// RetentionSeconds, when non-nil, overrides the account-default
	// retention for messages in this conversation (REQ-CHAT-92).
	// Semantics:
	//   nil      — use the owning principal's
	//              ChatAccountSettings.DefaultRetentionSeconds.
	//   *p == 0  — never expire (the per-conversation explicit
	//              "keep forever" override of an account default).
	//   *p > 0   — seconds since CreatedAt after which the retention
	//              sweeper hard-deletes the message.
	RetentionSeconds *int64
	// EditWindowSeconds, when non-nil, overrides the account-default
	// edit window for messages in this conversation (REQ-CHAT-20).
	// Semantics:
	//   nil      — use the owning principal's
	//              ChatAccountSettings.DefaultEditWindowSeconds.
	//   *p == 0  — no time limit; sender can edit at any time.
	//   *p > 0   — seconds after CreatedAt after which a Message/set
	//              update of the body fields is rejected.
	EditWindowSeconds *int64
	// ModSeq is the per-row monotonic counter used for /changes
	// pagination at the JMAP layer.
	ModSeq ModSeq
}

// ChatConversationFilter narrows a ListChatConversations read.
type ChatConversationFilter struct {
	// Kind, when non-nil, restricts to "dm" or "space".
	Kind *string
	// CreatedByPrincipalID, when non-nil, restricts to conversations
	// created by that principal.
	CreatedByPrincipalID *PrincipalID
	// IncludeArchived, when false (the default), excludes archived
	// conversations from the result set.
	IncludeArchived bool
	// AfterModSeq, when non-zero, returns rows whose ModSeq > the
	// supplied value (used by Conversation/changes).
	AfterModSeq ModSeq
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor; rows whose ID > AfterID. Zero
	// starts at the first row.
	AfterID ConversationID
}

// ChatMembership is one row in the chat_memberships table. The unique
// key (ConversationID, PrincipalID) is enforced by the schema.
type ChatMembership struct {
	// ID is the assigned primary key.
	ID MembershipID
	// ConversationID is the containing conversation.
	ConversationID ConversationID
	// PrincipalID is the member.
	PrincipalID PrincipalID
	// Role is "member", "admin", or "owner".
	Role string
	// JoinedAt is the row insert instant.
	JoinedAt time.Time
	// LastReadMessageID is the most recent message this principal has
	// read (REQ-CHAT-30). nil when the member has not read anything.
	LastReadMessageID *ChatMessageID
	// IsMuted suppresses notifications for this member's view of the
	// conversation.
	IsMuted bool
	// MuteUntil is the optional expiry for a time-limited mute. nil
	// when the mute (if set) has no expiry.
	MuteUntil *time.Time
	// NotificationsSetting is "all", "mentions", or "none".
	NotificationsSetting string
	// ModSeq is the per-row monotonic counter used by
	// Membership/changes.
	ModSeq ModSeq
}

// ChatMessage is one row in the chat_messages table. Body bytes
// (HTML, plain text) are stored inline; binary attachments live in
// the existing blob store and are referenced via AttachmentsJSON.
type ChatMessage struct {
	// ID is the assigned primary key.
	ID ChatMessageID
	// ConversationID is the containing conversation.
	ConversationID ConversationID
	// SenderPrincipalID is the message author. nil for system
	// messages (IsSystem == true) and for messages whose sender has
	// since been deleted (the FK is ON DELETE SET NULL).
	SenderPrincipalID *PrincipalID
	// IsSystem marks an in-band system message ("Alice started a
	// video call", "Bob joined the space"). System messages have
	// nil SenderPrincipalID and a non-empty MetadataJSON payload.
	IsSystem bool
	// BodyText is the plain-text rendering of the message used for
	// FTS and as a fallback for clients that do not render HTML.
	BodyText string
	// BodyHTML is the rich-text rendering for clients.
	BodyHTML string
	// BodyFormat is "text", "markdown", or "html"; describes the
	// authoring format the client supplied.
	BodyFormat string
	// ReplyToMessageID, when non-nil, is the parent message of this
	// reply.
	ReplyToMessageID *ChatMessageID
	// ReactionsJSON is the canonical JSON encoding of the
	// reactions map: {"<emoji>": [principal_id, ...]}.
	// Empty / nil means "no reactions". The Go layer enforces the
	// per-message reaction caps (≤100 distinct emojis × ≤200
	// reactors per emoji).
	ReactionsJSON []byte
	// AttachmentsJSON is the canonical JSON encoding of the
	// attachments array. Each entry: {blob_hash, content_type,
	// filename, size}. Each blob_hash references the existing
	// blob_refs table; refcount semantics match mail attachments.
	AttachmentsJSON []byte
	// MetadataJSON is the system-message payload (call IDs, joiner
	// IDs, durations). Empty / nil for non-system messages.
	MetadataJSON []byte
	// EditedAt records the most recent edit instant; nil when the
	// message has not been edited.
	EditedAt *time.Time
	// DeletedAt records the soft-delete instant; nil when the
	// message is live (REQ-CHAT-21). Soft-deleted rows survive so
	// thread offsets and read-receipt pointers stay stable.
	DeletedAt *time.Time
	// CreatedAt is the row insert instant.
	CreatedAt time.Time
	// ModSeq is the per-row monotonic counter used by
	// ChatMessage/changes.
	ModSeq ModSeq
}

// ChatMessageFilter narrows a ListChatMessages read.
type ChatMessageFilter struct {
	// ConversationID, when non-nil, restricts to a single
	// conversation. Most reads supply this.
	ConversationID *ConversationID
	// SenderPrincipalID, when non-nil, restricts to one author.
	SenderPrincipalID *PrincipalID
	// IncludeDeleted, when false (the default), excludes soft-deleted
	// rows from the result set. Read-receipt projections that need
	// every row regardless of soft-delete pass true.
	IncludeDeleted bool
	// CreatedAfter, when non-nil, restricts to messages whose
	// CreatedAt > *CreatedAfter. Pairs with CreatedBefore to form a
	// time-window predicate.
	CreatedAfter *time.Time
	// CreatedBefore, when non-nil, restricts to messages whose
	// CreatedAt < *CreatedBefore.
	CreatedBefore *time.Time
	// AfterModSeq, when non-zero, returns rows whose ModSeq > the
	// supplied value (used by Message/changes).
	AfterModSeq ModSeq
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor; rows whose ID > AfterID. Zero
	// starts at the first row.
	AfterID ChatMessageID
}

// ChatBlock is one row in the chat_blocks table. A block is one-way:
// the blocker stops receiving messages from the blocked principal in
// new DMs, but the blocked principal does not learn that they were
// blocked (REQ-CHAT-71 soft-block semantics).
type ChatBlock struct {
	// BlockerPrincipalID is the principal who issued the block.
	BlockerPrincipalID PrincipalID
	// BlockedPrincipalID is the principal blocked.
	BlockedPrincipalID PrincipalID
	// CreatedAt is the row insert instant.
	CreatedAt time.Time
	// Reason is an optional free-text note for the blocker's own
	// reference.
	Reason string
}

// ChatAccountSettings is one row in the chat_account_settings table
// (Phase 2 Wave 2.9.6). Carries the per-principal defaults consulted
// when a chat_conversations row leaves RetentionSeconds /
// EditWindowSeconds at NULL ("use account default"). When no row is
// persisted for a principal, the metadata layer returns the implicit
// defaults: DefaultRetentionSeconds=0 (never expire) and
// DefaultEditWindowSeconds=900 (15 minutes; REQ-CHAT-20).
type ChatAccountSettings struct {
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// DefaultRetentionSeconds is the account-wide retention default for
	// conversations whose RetentionSeconds is NULL (REQ-CHAT-92). 0
	// means "never expire". Positive values are seconds since the
	// message's CreatedAt at which the retention sweeper hard-deletes
	// it.
	DefaultRetentionSeconds int64
	// DefaultEditWindowSeconds is the account-wide edit-window default
	// for conversations whose EditWindowSeconds is NULL (REQ-CHAT-20).
	// 0 means "no time limit". Positive values are seconds since the
	// message's CreatedAt after which the body becomes immutable.
	DefaultEditWindowSeconds int64
	// CreatedAt is the row insert instant.
	CreatedAt time.Time
	// UpdatedAt is the most recent mutation instant.
	UpdatedAt time.Time
}

// Default values returned by GetChatAccountSettings when no row has
// been persisted for the requested principal.
const (
	// ChatDefaultRetentionSeconds is the implicit default retention
	// window: 0 means "never expire" (REQ-CHAT-92).
	ChatDefaultRetentionSeconds int64 = 0
	// ChatDefaultEditWindowSeconds is the implicit default edit window:
	// 15 minutes (REQ-CHAT-20).
	ChatDefaultEditWindowSeconds int64 = 900
)

// EntityKind values for the chat subsystem. Mirrors the JMAP datatype
// names in REQ-CHAT-DATA: Conversation, Message, Membership.
const (
	// EntityKindConversation is a JMAP `Conversation` row
	// (REQ-CHAT-02). The state-change feed carries (Kind, EntityID =
	// ConversationID, ParentEntityID = 0, Op).
	EntityKindConversation EntityKind = "conversation"
	// EntityKindChatMessage is a JMAP chat-`Message` row (REQ-CHAT-03).
	// Distinct from EntityKindEmail per docs/design/server/architecture/08-chat.md
	// "chat-Message; distinct from email-Email" — the email datatype's
	// state string and entity-kind name remain on EntityKindEmail. The
	// feed carries (Kind, EntityID = ChatMessageID, ParentEntityID =
	// ConversationID, Op) so per-conversation push filters dispatch
	// without a join.
	EntityKindChatMessage EntityKind = "message"
	// EntityKindMembership is a JMAP `Membership` row (REQ-CHAT-04).
	// The feed carries (Kind, EntityID = MembershipID,
	// ParentEntityID = ConversationID, Op).
	EntityKindMembership EntityKind = "membership"
)

// JMAPStateKind values for the chat subsystem. Bumped on every
// successful Conversation/set, Message/set, or Membership/set
// mutation. The columns on jmap_states are conversation_state,
// message_chat_state, membership_state — see the dual-meaning split
// note at the top of this file.
const (
	// JMAPStateKindConversation tracks JMAP Conversation changes
	// (REQ-CHAT-02).
	JMAPStateKindConversation JMAPStateKind = iota + 12
	// JMAPStateKindChatMessage tracks JMAP chat-Message changes
	// (REQ-CHAT-03). Distinct from JMAPStateKindEmail per the
	// dual-meaning split.
	JMAPStateKindChatMessage
	// JMAPStateKindMembership tracks JMAP Membership changes
	// (REQ-CHAT-04).
	JMAPStateKindMembership
	// JMAPStateKindPushSubscription tracks JMAP PushSubscription
	// changes (REQ-PROTO-120 / RFC 8620 §7.2). Bumped on every
	// successful PushSubscription/set mutation. Defined in this file
	// (rather than alongside the Wave-2 mail kinds in types_phase2.go)
	// so the iota chain stays contiguous: chat occupies iota+12 ..
	// iota+14, push the next slot.
	JMAPStateKindPushSubscription
	// JMAPStateKindShortcutCoach tracks JMAP ShortcutCoachStat changes
	// (REQ-PROTO-110..112). Bumped on every successful
	// ShortcutCoachStat/set mutation. Per REQ-PROTO-113, state-change
	// feed rows are optional for coach mutations; the state counter is
	// still advanced so clients can detect their own writes via the
	// standard /changes pattern.
	JMAPStateKindShortcutCoach
	// JMAPStateKindCategorySettings tracks JMAP CategorySettings changes
	// (REQ-CAT-50 / https://netzhansa.com/jmap/categorise). Bumped on
	// every successful CategorySettings/set or when a
	// CategorySettings/recategorise job starts or finishes.
	JMAPStateKindCategorySettings
	// JMAPStateKindManagedRule tracks ManagedRule changes (Wave 3.15 /
	// REQ-FLT-01..31 / https://netzhansa.com/jmap/managed-rules). Bumped
	// on every ManagedRule/set mutation. Defined here (rather than in
	// types_phase2.go) so the iota chain from the chat const block stays
	// contiguous and does not collide with the mail const block that ends
	// at JMAPStateKindCalendarEvent (value 11).
	JMAPStateKindManagedRule
	// JMAPStateKindSeenAddress tracks SeenAddress changes (REQ-MAIL-11e..m).
	// Bumped on every UpsertSeenAddress, DestroySeenAddress, or
	// PurgeSeenAddressesByPrincipal call.
	JMAPStateKindSeenAddress
)

// Chat-side server-enforced caps. CHECK constraints in SQL would
// fragment across SQLite vs Postgres, so we keep the rule in code.
const (
	// ChatReactionMaxEmojis is the cap on distinct emoji keys in one
	// message's reactions JSON.
	ChatReactionMaxEmojis = 100
	// ChatReactionMaxPerEmoji is the cap on reactor IDs under one
	// emoji key.
	ChatReactionMaxPerEmoji = 200
	// ChatReactionEmojiMaxBytes bounds an emoji key's UTF-8 byte
	// length.
	ChatReactionEmojiMaxBytes = 32
	// ChatAttachmentsMaxEntries caps the chat-message attachment
	// array length.
	ChatAttachmentsMaxEntries = 32
)

// ChatValidateEmoji enforces the per-key shape rule for a reactions
// JSON map key: non-empty, ≤32 bytes, valid UTF-8, no embedded HTML
// tags. Exposed for the metadata backends so the rule lives in one place.
func ChatValidateEmoji(emoji string) error {
	return chatValidateEmoji(emoji)
}

// ChatValidateReactions parses raw (when non-empty) and enforces the
// reaction caps documented in REQ-CHAT-23 plus the per-emoji shape
// rule via ChatValidateEmoji. Returns an ErrInvalidArgument-wrapped
// error on any cap violation.
func ChatValidateReactions(raw []byte) error {
	return chatValidateReactions(raw)
}

// ChatValidateAttachments parses raw (when non-empty) and verifies it
// is a JSON array of objects each carrying a non-empty blob_hash, with
// at most ChatAttachmentsMaxEntries entries.
func ChatValidateAttachments(raw []byte) error {
	return chatValidateAttachments(raw)
}

// ChatAttachmentBlobRef names one attachment's identifying metadata as
// stored in chat_messages.attachments_json: its content-addressed hash
// and the attachment's declared size in bytes. The metadata backends
// pair this with blob_refs.ref_count adjustments on insert / hard-delete
// so an INSERT into blob_refs happens with a non-zero size when this is
// the first reference to a given hash.
type ChatAttachmentBlobRef struct {
	Hash string
	Size int64
}

// ChatAttachmentHashes parses raw (when non-empty) and returns the
// distinct (blob_hash, size) entries referenced by the attachments JSON
// in the order in which each hash first appears. A nil or empty raw
// input returns (nil, nil). The shape rule mirrors
// ChatValidateAttachments; callers that have already validated raw can
// ignore the error path. Used by the metadata backends to drive
// blob_refs.ref_count adjustments atomically with chat_messages row
// inserts and hard-deletes. A duplicate blob_hash inside one message's
// attachments array is collapsed to a single ref so a single message
// referencing the same hash twice does not double-count the refcount;
// the first occurrence's size is retained.
func ChatAttachmentHashes(raw []byte) ([]ChatAttachmentBlobRef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("%w: attachments_json: %v", ErrInvalidArgument, err)
	}
	if len(arr) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(arr))
	out := make([]ChatAttachmentBlobRef, 0, len(arr))
	for i, entry := range arr {
		hash, ok := entry["blob_hash"].(string)
		if !ok || hash == "" {
			return nil, fmt.Errorf("%w: attachment %d missing blob_hash", ErrInvalidArgument, i)
		}
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		// size is optional from a validation perspective but the JMAP
		// encoder always emits it; tolerate an absent / non-numeric value
		// so a future caller serialising attachments by hand without size
		// does not fail validation here. The stored value is only used as
		// blob_refs.size on the first INSERT and is never authoritative.
		var size int64
		if v, ok := entry["size"]; ok {
			switch t := v.(type) {
			case float64:
				size = int64(t)
			case int64:
				size = t
			case int:
				size = int64(t)
			}
		}
		out = append(out, ChatAttachmentBlobRef{Hash: hash, Size: size})
	}
	return out, nil
}

// ChatApplyReaction adds or removes principalID from the reactor list
// for emoji on the canonical reactions JSON. Returns the new JSON
// (nil when the resulting map is empty so the caller can write SQL
// NULL), a "changed" flag (false on a no-op toggle), and any
// validation error. Caps and shape rules are enforced as in
// ChatValidateReactions.
func ChatApplyReaction(raw []byte, emoji string, principalID PrincipalID, present bool) ([]byte, bool, error) {
	return chatApplyReaction(raw, emoji, principalID, present)
}

func chatValidateEmoji(emoji string) error {
	if emoji == "" {
		return fmt.Errorf("%w: empty reaction emoji", ErrInvalidArgument)
	}
	if len(emoji) > ChatReactionEmojiMaxBytes {
		return fmt.Errorf("%w: reaction emoji exceeds %d bytes", ErrInvalidArgument, ChatReactionEmojiMaxBytes)
	}
	if !utf8.ValidString(emoji) {
		return fmt.Errorf("%w: reaction emoji is not valid UTF-8", ErrInvalidArgument)
	}
	low := strings.ToLower(emoji)
	if strings.Contains(low, "<script") || strings.Contains(low, "</script") {
		return fmt.Errorf("%w: reaction emoji contains script tag", ErrInvalidArgument)
	}
	return nil
}

func chatValidateReactions(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var m map[string][]int64
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("%w: reactions_json: %v", ErrInvalidArgument, err)
	}
	if len(m) > ChatReactionMaxEmojis {
		return fmt.Errorf("%w: reactions exceed %d distinct emojis", ErrInvalidArgument, ChatReactionMaxEmojis)
	}
	for emoji, reactors := range m {
		if err := chatValidateEmoji(emoji); err != nil {
			return err
		}
		if len(reactors) > ChatReactionMaxPerEmoji {
			return fmt.Errorf("%w: reaction %q has %d reactors (max %d)", ErrInvalidArgument, emoji, len(reactors), ChatReactionMaxPerEmoji)
		}
		seen := make(map[int64]struct{}, len(reactors))
		for _, r := range reactors {
			if _, dup := seen[r]; dup {
				return fmt.Errorf("%w: reactor %d duplicated under %q", ErrInvalidArgument, r, emoji)
			}
			seen[r] = struct{}{}
		}
	}
	return nil
}

func chatValidateAttachments(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return fmt.Errorf("%w: attachments_json: %v", ErrInvalidArgument, err)
	}
	if len(arr) > ChatAttachmentsMaxEntries {
		return fmt.Errorf("%w: attachments exceed %d entries", ErrInvalidArgument, ChatAttachmentsMaxEntries)
	}
	for i, entry := range arr {
		hash, ok := entry["blob_hash"].(string)
		if !ok || hash == "" {
			return fmt.Errorf("%w: attachment %d missing blob_hash", ErrInvalidArgument, i)
		}
	}
	return nil
}

func chatApplyReaction(raw []byte, emoji string, principalID PrincipalID, present bool) ([]byte, bool, error) {
	if err := chatValidateEmoji(emoji); err != nil {
		return nil, false, err
	}
	m := map[string][]int64{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, false, fmt.Errorf("%w: reactions_json: %v", ErrInvalidArgument, err)
		}
	}
	pid := int64(principalID)
	cur := m[emoji]
	idx := -1
	for i, r := range cur {
		if r == pid {
			idx = i
			break
		}
	}
	changed := false
	if present {
		if idx == -1 {
			if len(cur) >= ChatReactionMaxPerEmoji {
				return nil, false, fmt.Errorf("%w: reaction %q has %d reactors (max %d)", ErrInvalidArgument, emoji, len(cur), ChatReactionMaxPerEmoji)
			}
			if _, exists := m[emoji]; !exists && len(m) >= ChatReactionMaxEmojis {
				return nil, false, fmt.Errorf("%w: reactions exceed %d distinct emojis", ErrInvalidArgument, ChatReactionMaxEmojis)
			}
			cur = append(cur, pid)
			m[emoji] = cur
			changed = true
		}
	} else {
		if idx != -1 {
			cur = append(cur[:idx], cur[idx+1:]...)
			if len(cur) == 0 {
				delete(m, emoji)
			} else {
				m[emoji] = cur
			}
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	if len(m) == 0 {
		return nil, true, nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sort.Slice(m[k], func(i, j int) bool { return m[k][i] < m[k][j] })
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false, fmt.Errorf("store: marshal reactions_json: %w", err)
	}
	return out, true, nil
}
