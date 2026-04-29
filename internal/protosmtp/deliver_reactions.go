package protosmtp

// Phase 3 Wave 3.9: inbound reaction email detection and handling.
// REQ-FLOW-104..108.
//
// The reaction-detection hook runs inside deliverOne, after spam
// classification (classification is already resolved at call time), but
// before mailbox storage.  Spam-flagged reactions fall through to normal
// delivery per REQ-FLOW-108.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// reactionHeaders holds the three structured header values from an
// inbound reaction email.
type reactionHeaders struct {
	reactionTo     string // X-Herold-Reaction-To (orig Message-ID with angle brackets)
	reactionEmoji  string // X-Herold-Reaction-Emoji
	reactionAction string // X-Herold-Reaction-Action
}

// extractReactionHeaders checks whether the parsed message carries all
// three X-Herold-Reaction-* headers.  Returns (headers, true) when all
// three are present; (zero, false) otherwise.
func extractReactionHeaders(msg mailparse.Message) (reactionHeaders, bool) {
	h := msg.Headers
	to := strings.TrimSpace(h.Get("X-Herold-Reaction-To"))
	emoji := strings.TrimSpace(h.Get("X-Herold-Reaction-Emoji"))
	action := strings.TrimSpace(h.Get("X-Herold-Reaction-Action"))
	if to == "" || emoji == "" || action == "" {
		return reactionHeaders{}, false
	}
	return reactionHeaders{
		reactionTo:     to,
		reactionEmoji:  emoji,
		reactionAction: action,
	}, true
}

// tryConsumeReaction attempts to apply an inbound reaction email as a
// native Email.reactions patch for the given recipient principal.
// Returns true when the email was consumed (caller must NOT deliver it
// to the mailbox).  Returns false to fall through to normal delivery.
//
// Precondition: spam classification already ran; caller MUST pass
// classification.Verdict so spam reactions fall through (REQ-FLOW-108).
func (sess *session) tryConsumeReaction(
	ctx context.Context,
	rcpID store.PrincipalID,
	msg mailparse.Message,
	classification spam.Classification,
) bool {
	// REQ-FLOW-108: spam is delivered normally regardless of reaction headers.
	if classification.Verdict == spam.Spam {
		return false
	}

	rh, ok := extractReactionHeaders(msg)
	if !ok {
		return false
	}
	if strings.ToLower(rh.reactionAction) != "add" {
		// Only "add" is handled; other values fall through.
		return false
	}

	// Strip angle brackets from the Message-ID header so it matches the
	// store's cached Envelope.MessageID (stored without brackets).
	origMsgID := strings.Trim(rh.reactionTo, "<>")
	if origMsgID == "" {
		return false
	}

	// REQ-FLOW-105.1: look up the original email in the recipient's mailbox.
	origMsg, err := sess.srv.store.Meta().GetMessageByMessageIDHeader(ctx, rcpID, origMsgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Original not found — deliver normally (REQ-FLOW-105.4).
			sess.log.DebugContext(ctx, "reaction: original not found; delivering normally",
				slog.String("activity", observe.ActivityInternal),
				slog.String("message_id", origMsgID))
			return false
		}
		sess.log.WarnContext(ctx, "reaction: store lookup error; delivering normally",
			slog.String("activity", observe.ActivityInternal),
			slog.String("message_id", origMsgID),
			slog.String("err", err.Error()))
		return false
	}

	// REQ-FLOW-105.2: identify the reactor from From:.
	reactorAddr, err := extractFromAddress(msg)
	if err != nil || reactorAddr == "" {
		return false
	}

	// Verify reactor is a recognised participant (sender or recipient).
	if !isRecognisedParticipant(reactorAddr, origMsg.Envelope) {
		// Deliver normally (REQ-FLOW-105.4).
		sess.log.DebugContext(ctx, "reaction: reactor not a recognised participant; delivering normally",
			slog.String("activity", observe.ActivityInternal),
			slog.String("reactor", reactorAddr),
			slog.String("message_id", origMsgID))
		return false
	}

	// REQ-FLOW-106: look up or synthesise a reactor principal id.
	reactorPID, err := sess.resolveOrSynthesiseReactorPrincipal(ctx, reactorAddr)
	if err != nil {
		sess.log.WarnContext(ctx, "reaction: reactor principal resolution failed; delivering normally",
			slog.String("activity", observe.ActivityInternal),
			slog.String("reactor", reactorAddr),
			slog.String("err", err.Error()))
		return false
	}
	if reactorPID == 0 {
		// External reactor not in directory — deliver normally.
		sess.log.DebugContext(ctx, "reaction: reactor not in directory; delivering normally",
			slog.String("activity", observe.ActivityInternal),
			slog.String("reactor", reactorAddr))
		return false
	}

	// REQ-FLOW-105.3: apply the native reaction patch.
	if err := sess.srv.store.Meta().AddEmailReaction(
		ctx, origMsg.ID, rh.reactionEmoji, reactorPID, sess.srv.clk.Now(),
	); err != nil {
		sess.log.WarnContext(ctx, "reaction: add reaction failed; delivering normally",
			slog.String("activity", observe.ActivitySystem),
			slog.String("err", err.Error()))
		return false
	}

	// Advance the Email JMAP state so push/EventSource clients see the change.
	// Best-effort: failure here does not prevent consumption.
	if _, err := sess.srv.store.Meta().IncrementJMAPState(ctx, rcpID, store.JMAPStateKindEmail); err != nil {
		sess.log.WarnContext(ctx, "reaction: bump jmap state failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
	}

	// REQ-FLOW-107: record metric.
	observe.RegisterReactionMetrics()
	observe.ReactionConsumedTotal.WithLabelValues(fmt.Sprintf("%d", uint64(rcpID))).Inc()

	sess.log.InfoContext(ctx, "reaction: consumed inbound reaction email",
		slog.String("activity", observe.ActivitySystem),
		slog.String("emoji", rh.reactionEmoji),
		slog.String("reactor", reactorAddr),
		slog.String("original_message_id", origMsgID))
	return true
}

// extractFromAddress returns the bare email address from the parsed
// message's From header.
func extractFromAddress(msg mailparse.Message) (string, error) {
	raw := strings.TrimSpace(msg.Headers.Get("From"))
	if raw == "" {
		return "", fmt.Errorf("no From header")
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		// Last-ditch: try the raw value itself as an address.
		return strings.TrimSpace(raw), nil
	}
	return strings.ToLower(addr.Address), nil
}

// isRecognisedParticipant returns true when candidate appears in the
// original message's sender or any recipient field.
func isRecognisedParticipant(candidate string, env store.Envelope) bool {
	candidate = strings.ToLower(candidate)
	for _, field := range []string{env.From, env.To, env.Cc, env.Bcc} {
		if field == "" {
			continue
		}
		addrs, err := mail.ParseAddressList(field)
		if err != nil {
			// Fall back to substring match on the raw value.
			if strings.Contains(strings.ToLower(field), candidate) {
				return true
			}
			continue
		}
		for _, a := range addrs {
			if strings.ToLower(a.Address) == candidate {
				return true
			}
		}
	}
	return false
}

// resolveOrSynthesiseReactorPrincipal returns the store PrincipalID for
// reactorAddr.  For local principals it does a directory lookup.  For
// external reactors (REQ-FLOW-106) it upserts a synthetic external-
// principal record via GetPrincipalByEmail; if the address is not found
// a best-effort synthetic id based on a hash is NOT allocated in v1 —
// we use the well-known sentinel 0 and let the caller decide.  In
// practice, the external-principal machinery from the directory layer
// handles this.
//
// v1 simplification: use GetPrincipalByEmail; if not found, return a
// sentinel 0 and let the caller fall through to normal delivery.
// The address may be a local or remote user.
func (sess *session) resolveOrSynthesiseReactorPrincipal(
	ctx context.Context,
	reactorAddr string,
) (store.PrincipalID, error) {
	p, err := sess.srv.store.Meta().GetPrincipalByEmail(ctx, strings.ToLower(reactorAddr))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// External reactor: allocate a synthetic principal.
			// v1 simplification: fall through to normal delivery rather
			// than synthesising a new principal.  The caller treats a
			// zero pid as "proceed with 0".
			return 0, nil
		}
		return 0, fmt.Errorf("reactor principal lookup: %w", err)
	}
	return p.ID, nil
}
