package webpush

// dismiss.go implements the mail-dismiss push (REQ-PUSH-84, re #481):
// telling a device that a notification it already posted for a
// message is now stale because the message was read, archived, or
// destroyed on another client or session.
//
// Unlike the arrival gate in rules.go (Evaluate, ReasonDroppedNotArrival),
// a dismiss is not a rules.Rules decision: it is exempt from the
// per-event-type mute map, the mail category allowlist, and quiet
// hours, and is governed only by the subscription's master switch
// (dispatcher.go's processChange applies that gate directly rather
// than routing through Evaluate).

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/hanshuebner/herold/internal/store"
)

// Dismiss reason tokens carried in the wire payload's "reason" field
// (REQ-PUSH-84). Closed set; a client that receives an unrecognised
// value still resolves the emailId/threadId as a no-op dismissal.
const (
	DismissReasonSeen      = "seen"
	DismissReasonLeftInbox = "left-inbox"
	DismissReasonDestroyed = "destroyed"
)

// dismissMaxTracked bounds DismissTracker's memory footprint. It is
// far larger than the set of messages realistically still showing a
// notification on any device, so eviction under the cap is expected to
// be rare; it exists to give a long-lived server's memory a hard
// ceiling that does not grow with total mailbox size.
const dismissMaxTracked = 100_000

// dismissState is the per-message bookkeeping DismissTracker keeps.
type dismissState struct {
	// inInbox records that the message held an Inbox-role mailbox
	// membership the last time the tracker observed it.
	inInbox bool
	// dismissedSeen records that a "seen" dismissal has already fired
	// for the message's current read state, so an unrelated later
	// update (starring, an unrelated keyword) on an already-read inbox
	// message does not refire it. Cleared when the message is
	// observed unseen again, so a genuine unread-then-read cycle
	// dismisses each time.
	dismissedSeen bool
}

// DismissTracker is a bounded, best-effort, in-memory record of which
// messages the dispatcher has most recently observed holding an
// Inbox-role mailbox membership, plus whether a "seen" dismissal has
// already fired for the message's current read state (re #481,
// REQ-PUSH-84).
//
// It exists because a mailbox move's change-feed row names the
// destination mailbox in ParentEntityID, not the vacated membership
// (MoveMessage, storesqlite/storepg metadata.go): the "message left
// the Inbox" transition carries no direct signal in the event itself,
// unlike a keyword change or a mailbox-scoped destroy, both of which
// name the affected membership directly. The tracker fills that gap
// by remembering the Inbox-presence fact from the message's arrival (or
// any later observation) so a subsequent Updated event that shows no
// remaining Inbox membership is recognised as a departure rather than a
// static property of a message that was never in the Inbox (e.g. one
// filed to Sent directly, which must never dismiss).
//
// State resets on dispatcher restart, exactly like the retry and
// coalesce maps on Dispatcher; a dismissal missed across a restart is
// bounded by the client's own fold-time reconciliation against its
// posted notification set (SyncEngine on Android), which does not
// depend on push.
type DismissTracker struct {
	mu    sync.Mutex
	state map[store.MessageID]dismissState
}

// NewDismissTracker returns an empty tracker.
func NewDismissTracker() *DismissTracker {
	return &DismissTracker{state: make(map[store.MessageID]dismissState)}
}

// markInInbox records that id currently holds an Inbox-role mailbox
// membership and has not yet had a "seen" dismissal fired for its
// current (unread) state. Called when the dispatcher observes the
// message's arrival into the Inbox; a Created row never itself
// produces a dismiss, so there is nothing to fire here.
func (t *DismissTracker) markInInbox(id store.MessageID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.setLocked(id, dismissState{inInbox: true})
}

// update folds a freshly observed (hasInbox, seenInInbox) reading for
// id into the tracker and reports which dismiss transition, if any,
// just occurred. hasInbox is whether id currently holds any Inbox-role
// membership; seenInInbox is whether that membership (when present)
// carries $seen.
func (t *DismissTracker) update(id store.MessageID, hasInbox, seenInInbox bool) (fireSeen, fireLeftInbox bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, tracked := t.state[id]
	if !hasInbox {
		wasInInbox := tracked && st.inInbox
		delete(t.state, id)
		return false, wasInInbox
	}
	fireSeen = seenInInbox && !st.dismissedSeen
	t.setLocked(id, dismissState{inInbox: true, dismissedSeen: seenInInbox})
	return fireSeen, false
}

// forget drops any tracked state for id. Called once a "destroyed"
// dismissal fires so a later, unrelated message id starts fresh.
func (t *DismissTracker) forget(id store.MessageID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.state, id)
}

// setLocked installs state for id, evicting one arbitrary existing
// entry first if the map is already at capacity. Callers hold t.mu.
func (t *DismissTracker) setLocked(id store.MessageID, s dismissState) {
	if _, exists := t.state[id]; !exists && len(t.state) >= dismissMaxTracked {
		for k := range t.state {
			delete(t.state, k)
			break
		}
	}
	t.state[id] = s
}

// ClassifyDismiss reports whether ev is a mail-dismiss-eligible Email
// transition and, when it is, the wire reason (REQ-PUSH-84 / re #481).
// tracker supplies the Inbox-presence memory a mailbox move's
// ParentEntityID (the destination, not the vacated membership) cannot
// provide on its own; see DismissTracker's doc comment.
func ClassifyDismiss(ctx context.Context, st store.Store, tracker *DismissTracker, ev store.StateChange) (reason string, ok bool) {
	if ev.Kind != store.EntityKindEmail {
		return "", false
	}
	switch ev.Op {
	case store.ChangeOpCreated:
		mbox, err := st.Meta().GetMailboxByID(ctx, store.MailboxID(ev.ParentEntityID))
		if err == nil && isInboxRoleMailbox(mbox) {
			tracker.markInInbox(store.MessageID(ev.EntityID))
		}
		return "", false
	case store.ChangeOpDestroyed:
		// The destroy row names the exact membership removed
		// (ExpungeMessages / RemoveMessageFromMailbox scope
		// ParentEntityID to that mailbox), so — unlike the "left the
		// Inbox" move case — this is a direct signal: destroying the
		// Inbox membership dismisses regardless of whether the
		// message row itself, or another membership, survives.
		mbox, err := st.Meta().GetMailboxByID(ctx, store.MailboxID(ev.ParentEntityID))
		if err != nil || !isInboxRoleMailbox(mbox) {
			return "", false
		}
		tracker.forget(store.MessageID(ev.EntityID))
		return DismissReasonDestroyed, true
	case store.ChangeOpUpdated:
		msgID := store.MessageID(ev.EntityID)
		msg, err := st.Meta().GetMessage(ctx, msgID)
		if err != nil {
			// The row is gone (fully destroyed between the change-feed
			// write and this read) or otherwise unreadable; nothing to
			// classify.
			return "", false
		}
		hasInbox, seenInInbox := false, false
		for _, mm := range msg.Mailboxes {
			mbox, mErr := st.Meta().GetMailboxByID(ctx, mm.MailboxID)
			if mErr != nil || !isInboxRoleMailbox(mbox) {
				continue
			}
			hasInbox = true
			if mm.Flags&store.MessageFlagSeen != 0 {
				seenInInbox = true
			}
		}
		fireSeen, fireLeftInbox := tracker.update(msgID, hasInbox, seenInInbox)
		switch {
		case fireSeen:
			return DismissReasonSeen, true
		case fireLeftInbox:
			return DismissReasonLeftInbox, true
		default:
			return "", false
		}
	default:
		return "", false
	}
}

// emailDismissPayload is the REQ-PUSH-84 wire shape: kind and type are
// both "mail-dismiss" so a client dispatches on either field the same
// way it already does for the "mail" arrival payload, followed by the
// shared stateChangeBase envelope and the three dismiss-specific
// fields. Web Push, FCM, and UnifiedPush carry the identical JSON —
// the FCM transport (deliverFCM) already forwards a built payload
// verbatim as the single "payload" data field, so no transport-specific
// mapping is needed here.
type emailDismissPayload struct {
	stateChangeBase
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	EmailID  string `json:"emailId"`
	ThreadID string `json:"threadId"`
	Reason   string `json:"reason"`
}

// buildEmailDismissPayload builds the mail-dismiss payload for ev,
// already classified as reason by ClassifyDismiss.
//
// A "destroyed" dismiss frequently arrives after the message row
// itself is gone (the dispatcher reads the change feed asynchronously,
// after the destroying transaction committed), so ThreadID cannot
// always be recovered from the store at build time. In that case the
// payload falls back to "t<emailID>", the same convention
// buildEmailPayload uses for an unthreaded message — exact for the
// (common) single-message-thread case, and a best-effort miss for a
// destroyed reply that leaves the rest of its thread's notification
// undismissed; the client's own fold-time reconciliation is the
// backstop for that gap.
func buildEmailDismissPayload(ctx context.Context, st store.Store, ev store.StateChange, reason string) (buildPayloadResult, error) {
	msgID := store.MessageID(ev.EntityID)
	threadID := fmt.Sprintf("t%d", msgID)
	if msg, err := st.Meta().GetMessage(ctx, msgID); err == nil {
		if msg.ThreadID != 0 {
			threadID = fmt.Sprintf("t%d", msg.ThreadID)
		} else {
			threadID = fmt.Sprintf("t%d", msg.ID)
		}
	}
	out := emailDismissPayload{
		stateChangeBase: newStateChangeBase(ev.PrincipalID, "Email", stateValueForKind(ev)),
		Kind:            "mail-dismiss",
		Type:            "mail-dismiss",
		EmailID:         fmt.Sprintf("%d", msgID),
		ThreadID:        threadID,
		Reason:          reason,
	}
	js, err := json.Marshal(out)
	if err != nil {
		return buildPayloadResult{}, fmt.Errorf("webpush: marshal email dismiss payload: %w", err)
	}
	// Per-email coalescing (REQ-PUSH-84): the tag is keyed by message,
	// not by thread like the arrival tag ("email/<threadID>"), so
	// several qualifying transitions on the same email within the
	// dispatcher's coalescing window collapse to one push without
	// merging with a sibling message's dismissal in the same thread.
	tag := fmt.Sprintf("email-dismiss/%d", msgID)
	return buildPayloadResult{JSON: js, CoalesceTag: tag, OriginatorID: uint64(msgID)}, nil
}
