package chat

import (
	"context"
	"log/slog"
	"sync"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// FTS is the chat-side projection of the storefts.Index surface used by
// Message/query (REQ-CHAT-80..82). The interface is narrow on purpose:
// the chat handler only needs the membership-scoped chat-message
// search; pulling in the full storefts.Index would couple the chat
// JMAP package to the email-side mapping for no callable benefit.
//
// Production wires storefts.Index here; tests can pass a fake
// implementation, and a nil value triggers the in-process substring
// fallback in Message/query so test fixtures without an index keep
// working (with a one-time warn log).
type FTS interface {
	SearchChatMessages(
		ctx context.Context,
		query string,
		conversationIDs []store.ConversationID,
		limit int,
	) ([]store.ChatMessageID, error)
}

// handlerSet bundles the dependencies the per-method handlers reach
// for. One instance is constructed by Register and wrapped by each
// method-handler struct.
type handlerSet struct {
	store  store.Store
	logger *slog.Logger
	clk    clock.Clock
	limits AccountLimits

	// fts is the membership-scoped chat search backend. Nil when the
	// caller did not wire a real index — the Message/query path then
	// falls back to a substring scan and warn-logs once so the
	// misconfiguration is visible in operator logs.
	fts FTS
	// ftsFallbackWarnOnce guards the one-time warn-log emitted when
	// Message/query takes the substring fallback path.
	ftsFallbackWarnOnce sync.Once
}

// AccountLimits is the per-account capability descriptor body. Defaults
// follow the REQ-CHAT capacity envelope and are overridable by the
// caller of Register.
type AccountLimits struct {
	// MaxConversationsPerAccount caps Conversation creation per account.
	MaxConversationsPerAccount int `json:"maxConversationsPerAccount"`
	// MaxMembersPerSpace caps Membership rows in a single Space.
	MaxMembersPerSpace int `json:"maxMembersPerSpace"`
	// MaxMessageBodyBytes caps the (text or html) body bytes per
	// Message.
	MaxMessageBodyBytes int `json:"maxMessageBodyBytes"`
	// MaxAttachmentsPerMessage caps Message.attachments.
	MaxAttachmentsPerMessage int `json:"maxAttachmentsPerMessage"`
	// MaxReactionsPerMessage caps the total reactions across all emojis
	// on one Message.
	MaxReactionsPerMessage int `json:"maxReactionsPerMessage"`
	// DefaultRetentionSeconds advertises the operator's default chat
	// retention window for this account (REQ-CHAT-92). 0 means
	// "never expire".
	DefaultRetentionSeconds int64 `json:"defaultRetentionSeconds"`
	// DefaultEditWindowSeconds advertises the operator's default chat
	// edit window for this account (REQ-CHAT-20). 0 means "no time
	// limit".
	DefaultEditWindowSeconds int64 `json:"defaultEditWindowSeconds"`
}

// DefaultLimits returns the conservative defaults from the REQ-CHAT
// capacity envelope.
func DefaultLimits() AccountLimits {
	return AccountLimits{
		MaxConversationsPerAccount: 10000,
		MaxMembersPerSpace:         1000,
		MaxMessageBodyBytes:        64 * 1024,
		MaxAttachmentsPerMessage:   10,
		MaxReactionsPerMessage:     100,
		DefaultRetentionSeconds:    store.ChatDefaultRetentionSeconds,
		DefaultEditWindowSeconds:   store.ChatDefaultEditWindowSeconds,
	}
}

// Register installs the Conversation/*, Message/*, Membership/*, and
// Block/* method handlers under the JMAP Chat capability
// (REQ-CHAT-01..06). It also installs the per-account capability
// descriptor advertising the chat capacity envelope. Called from the
// StartServer boot path alongside the other JMAP datatype Registers.
//
// The FTS index is not wired here; callers that have one available
// (admin/server.go on the production path) use RegisterWithFTS instead.
// Without an FTS index the Message/query handler falls back to an
// in-process substring scan and warn-logs once.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	RegisterWithLimits(reg, st, logger, clk, DefaultLimits())
}

// RegisterWithLimits is Register with explicit per-account limits;
// useful for tests and operator-tuned production deployments.
func RegisterWithLimits(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
	limits AccountLimits,
) {
	RegisterWithFTS(reg, st, nil, logger, clk, limits)
}

// RegisterWithFTS is RegisterWithLimits with an explicit FTS backend
// (Wave 2.9.6 Track D). Pass a nil fts to fall back to the in-process
// substring scan; the handler emits a one-time warn log so the
// misconfiguration is visible to operators.
func RegisterWithFTS(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	fts FTS,
	logger *slog.Logger,
	clk clock.Clock,
	limits AccountLimits,
) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, logger: logger, clk: clk, limits: limits, fts: fts}

	reg.Register(protojmap.CapabilityJMAPChat, &convGetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &convChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &convSetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &convQueryHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, convQueryChangesHandler{h: h})

	reg.Register(protojmap.CapabilityJMAPChat, &msgGetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &msgChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &msgSetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &msgQueryHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, msgQueryChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &msgReactHandler{h: h})

	reg.Register(protojmap.CapabilityJMAPChat, &memGetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &memChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &memSetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &memQueryHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, memQueryChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &memSetLastReadHandler{h: h})

	reg.Register(protojmap.CapabilityJMAPChat, &blockGetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPChat, &blockSetHandler{h: h})

	// Per-account capability descriptor advertises the capacity envelope
	// the server enforces. The server-wide capability descriptor is the
	// empty object — every tunable lives on the per-account axis.
	reg.RegisterAccountCapability(protojmap.CapabilityJMAPChat, chatAccountCapability{limits: limits})
}

// chatAccountCapability is the per-account capability descriptor
// provider.
type chatAccountCapability struct {
	limits AccountLimits
}

func (c chatAccountCapability) AccountCapability() any { return c.limits }

// FTS wired in Wave 2.9.6 Track D (REQ-CHAT-80..82): the FTS worker
// (internal/storefts/worker.go) recognises EntityKindChatMessage on
// ReadChangeFeedForFTS and indexes Message.body.text under a
// kind="chat_message" discriminator. Message/query routes free-text
// filters through Index.SearchChatMessages (membership-scoped). The
// in-process substring scan is retained as a fallback for fixtures
// that construct the chat handler without an FTS backend.
