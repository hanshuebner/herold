package chat

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// handlerSet bundles the dependencies the per-method handlers reach
// for. One instance is constructed by Register and wrapped by each
// method-handler struct.
type handlerSet struct {
	store  store.Store
	logger *slog.Logger
	clk    clock.Clock
	limits AccountLimits
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
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, logger: logger, clk: clk, limits: limits}

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

// FTS coordination note (REQ-CHAT-80..82): the existing FTS worker
// drives off ReadChangeFeedForFTS and only acts on EntityKindEmail
// today; chat-message indexing requires the worker to also recognise
// EntityKindChatMessage and call FTS.IndexMessage with the chat row's
// BodyText. Track A/parent reconcile that small extension at commit
// time. See doc.go for the architecture spec link.
//
// TODO(2.8-coord): wire EntityKindChatMessage into the FTS worker so
// Message/query's text search returns hits without a separate
// in-process scan; until then, Message/query falls back to a plain
// substring filter on BodyText (see message.go).
