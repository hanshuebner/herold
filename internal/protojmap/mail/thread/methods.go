package thread

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2).
type jmapID = string

// jmapThread is the wire-form Thread object (RFC 8621 §8.1).
type jmapThread struct {
	ID       jmapID   `json:"id"`
	EmailIDs []jmapID `json:"emailIds"`
}

// getRequest is the inbound shape of Thread/get.
type getRequest struct {
	AccountID jmapID    `json:"accountId"`
	IDs       *[]jmapID `json:"ids"`
}

// getResponse mirrors RFC 8620 §5.1.
type getResponse struct {
	AccountID string       `json:"accountId"`
	State     string       `json:"state"`
	List      []jmapThread `json:"list"`
	NotFound  []jmapID     `json:"notFound"`
}

// changesRequest is the inbound shape of Thread/changes.
type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges,omitempty"`
}

// changesResponse mirrors RFC 8620 §5.2.
type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []jmapID `json:"created"`
	Updated        []jmapID `json:"updated"`
	Destroyed      []jmapID `json:"destroyed"`
}

// handlerSet binds the Thread handlers to the store.
type handlerSet struct {
	store store.Store
}

// stateString stringifies a ChangeSeq into the wire form used by
// Thread/get and Thread/changes. Thread state is derived from the
// Email change feed (EntityKindEmail) so that Thread state advances
// whenever an email is created or destroyed, without requiring a
// separate Thread-level counter.
func stateString(seq uint64) string { return strconv.FormatUint(seq, 10) }

// parseSeq parses a wire-form state string into a ChangeSeq.
// Returns 0 on empty input (which is valid: "no state yet").
func parseSeq(s string) (store.ChangeSeq, bool) {
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return store.ChangeSeq(v), true
}

// currentThreadState returns the Thread state string derived from the
// maximum Email change-feed seq for this principal.
func currentThreadState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	seq, err := meta.GetMaxChangeSeqForKind(ctx, pid, store.EntityKindEmail)
	if err != nil {
		return "", err
	}
	return stateString(uint64(seq)), nil
}

// resolveAccount maps the JMAP accountId to the owning principal,
// returning ownerPID for downstream queries (REQ-PROTO-33).
func (h *handlerSet) resolveAccount(
	ctx context.Context,
	p store.Principal,
	requested jmapID,
) (store.PrincipalID, *protojmap.MethodError) {
	return protojmap.ResolveAccount(ctx, h.store.Meta(), p.ID, requested)
}

// listAllMessages returns every message owned by p across every
// mailbox. Phase 1's store does not expose a per-principal "all
// messages" iterator; we fan out across the principal's mailboxes. The
// caller is single-threaded JMAP so the cost is bounded by the
// principal's mailbox + message count.
//
// For cross-account use the caller passes ownerPID alongside callerPID;
// the iterator is scoped to mailboxes owned by ownerPID and visible to
// callerPID via ACL.
func (h *handlerSet) listAllMessages(ctx context.Context, callerPID, ownerPID store.PrincipalID) ([]store.Message, error) {
	mboxes, err := h.accessibleMailboxes(ctx, callerPID, ownerPID)
	if err != nil {
		return nil, err
	}
	var all []store.Message
	for _, mb := range mboxes {
		// Fetch all messages with envelopes; pagination by cursor.
		var afterUID store.UID
		for {
			batch, err := h.store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{
				AfterUID:     afterUID,
				Limit:        1000,
				WithEnvelope: true,
			})
			if err != nil {
				return nil, err
			}
			if len(batch) == 0 {
				break
			}
			all = append(all, batch...)
			afterUID = batch[len(batch)-1].UID
			if len(batch) < 1000 {
				break
			}
		}
	}
	return all, nil
}

// accessibleMailboxes returns the mailbox set for the requested
// (callerPID, ownerPID) routing. For caller == owner it is the owned
// mailbox list; otherwise it is the ACL-shared subset owned by
// ownerPID and visible to callerPID.
func (h *handlerSet) accessibleMailboxes(ctx context.Context, callerPID, ownerPID store.PrincipalID) ([]store.Mailbox, error) {
	if callerPID == ownerPID {
		return h.store.Meta().ListMailboxes(ctx, ownerPID)
	}
	shared, err := h.store.Meta().ListMailboxesAccessibleBy(ctx, callerPID)
	if err != nil {
		return nil, err
	}
	out := make([]store.Mailbox, 0, len(shared))
	for _, mb := range shared {
		if mb.PrincipalID == ownerPID {
			out = append(out, mb)
		}
	}
	return out, nil
}

// computeForPrincipal returns the (msg→thread, thread→[msgs]) maps for
// the requested account. The inner slice keeps full store.Message
// values so callers can sort by receivedAt without extra lookups.
//
// v1 keys threads by store.Message.ThreadID (falling back to MessageID
// when ThreadID is 0). This matches Email/get's threadIDForMessage --
// both render "t<m.ThreadID>" or, for un-threaded messages,
// "t<m.ID>" -- so a client that takes Email.threadId and passes it
// back into Thread/get always resolves to a thread row.
//
// Thread assignment happens at ingest time: InsertMessage resolves
// references by checking both env_in_reply_to and env_references (per
// RFC 5256 sec 2.2 and RFC 8621 sec 8.1) and looks up ancestor messages
// by env_message_id in the same principal's mailboxes. The resolved
// thread_id is persisted so this read path is a simple group-by.
func (h *handlerSet) computeForPrincipal(ctx context.Context, callerPID, ownerPID store.PrincipalID) (map[store.MessageID]ThreadKey, map[ThreadKey][]store.Message, error) {
	msgs, err := h.listAllMessages(ctx, callerPID, ownerPID)
	if err != nil {
		return nil, nil, err
	}
	msgToThread := make(map[store.MessageID]ThreadKey, len(msgs))
	threadToMsgs := make(map[ThreadKey][]store.Message)
	for _, m := range msgs {
		var key ThreadKey
		if m.ThreadID != 0 {
			key = ThreadKey(m.ThreadID)
		} else {
			key = ThreadKey(uint64(m.ID))
		}
		msgToThread[m.ID] = key
		threadToMsgs[key] = append(threadToMsgs[key], m)
	}
	return msgToThread, threadToMsgs, nil
}

// sortThreadMessages sorts a slice of messages by ReceivedAt ascending,
// with tie-break on MessageID ascending (RFC 8621 §8.1).
func sortThreadMessages(msgs []store.Message) {
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].ReceivedAt.Equal(msgs[j].ReceivedAt) {
			return msgs[i].ID < msgs[j].ID
		}
		return msgs[i].ReceivedAt.Before(msgs[j].ReceivedAt)
	})
}

// renderThreadID stringifies a ThreadKey for the JMAP wire. The "t"
// prefix matches the format Email/get's threadIDForMessage produces, so
// a client that takes Email.threadId and passes it back to Thread/get
// resolves to the same thread row. Without this prefix the two
// renderings disagreed and Thread/get returned notFound for every
// thread the suite asked about.
func renderThreadID(k ThreadKey) jmapID {
	return "t" + strconv.FormatUint(uint64(k), 10)
}

// parseThreadID accepts the "t<n>" wire form. The bare numeric form is
// also accepted for back-compatibility with any caller that constructed
// a thread id by hand before this format was unified.
func parseThreadID(s jmapID) (ThreadKey, bool) {
	if len(s) > 1 && s[0] == 't' {
		s = s[1:]
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return ThreadKey(v), true
}

func renderEmailID(id store.MessageID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// -- Thread/get -------------------------------------------------------

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "Thread/get" }

func (g getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	ownerPID, e := g.h.resolveAccount(ctx, p, req.AccountID)
	if e != nil {
		return nil, e
	}
	state, err := currentThreadState(ctx, g.h.store.Meta(), ownerPID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := getResponse{
		AccountID: string(req.AccountID),
		State:     state,
		List:      []jmapThread{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		// Return all threads. The full enumeration path is rare (the
		// suite always passes specific ids); we keep it correct via the
		// legacy whole-account scan, but cap output so a runaway client
		// cannot pull a 100k-row response into memory. Per RFC 8620
		// §5.1 the cap surfaces as a tooLarge MethodError.
		_, threadToMsgs, err := g.h.computeForPrincipal(ctx, p.ID, ownerPID)
		if err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
		const allThreadsCap = 50_000
		if len(threadToMsgs) > allThreadsCap {
			return nil, protojmap.NewMethodError("requestTooLarge",
				"Thread/get with ids:null exceeds the per-call cap; pass specific ids")
		}
		for k, msgs := range threadToMsgs {
			sortThreadMessages(msgs)
			t := jmapThread{ID: renderThreadID(k), EmailIDs: make([]jmapID, 0, len(msgs))}
			for _, m := range msgs {
				t.EmailIDs = append(t.EmailIDs, renderEmailID(m.ID))
			}
			resp.List = append(resp.List, t)
		}
		return resp, nil
	}

	// Targeted lookup: parse the requested ids, resolve them in one
	// SQL pass, render only the threads that resolved. Avoids the
	// per-call full-account scan listAllMessages performs.
	keys, keyToWire, notFound := parseRequestedThreadIDs(*req.IDs)
	resp.NotFound = append(resp.NotFound, notFound...)

	var threadRows map[uint64][]store.ThreadMessageRow
	if len(keys) > 0 {
		threadRows, err = g.h.store.Meta().ListThreadsByKeys(ctx, ownerPID, keys)
		if err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	// Cross-account: drop rows whose mailbox is not visible to the
	// caller (REQ-PROTO-33). Same-account: pass through.
	if p.ID != ownerPID && threadRows != nil {
		threadRows = g.h.filterThreadRowsByACL(ctx, p.ID, ownerPID, threadRows)
	}

	for _, id := range *req.IDs {
		k, ok := parseThreadID(id)
		if !ok {
			continue // already in NotFound from the parse pass
		}
		rows, hit := threadRows[uint64(k)]
		if !hit {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		sortThreadRows(rows)
		t := jmapThread{ID: keyToWire[uint64(k)], EmailIDs: make([]jmapID, 0, len(rows))}
		for _, r := range rows {
			t.EmailIDs = append(t.EmailIDs, renderEmailID(r.MessageID))
		}
		resp.List = append(resp.List, t)
	}
	return resp, nil
}

// parseRequestedThreadIDs walks the wire-form id list, decoding each
// entry to its ThreadKey. Unparseable entries are returned in notFound
// for the caller to merge into the response. keyToWire preserves the
// caller-supplied wire form so the response echoes the exact id the
// client used (RFC 8620 §5.1: ids in /list are the same strings the
// client passed in /ids), even when the client mixed bare-numeric and
// "tN" forms.
func parseRequestedThreadIDs(in []jmapID) (keys []uint64, keyToWire map[uint64]jmapID, notFound []jmapID) {
	keys = make([]uint64, 0, len(in))
	keyToWire = make(map[uint64]jmapID, len(in))
	for _, id := range in {
		k, ok := parseThreadID(id)
		if !ok {
			notFound = append(notFound, id)
			continue
		}
		key := uint64(k)
		if _, dup := keyToWire[key]; dup {
			continue
		}
		keys = append(keys, key)
		keyToWire[key] = id
	}
	return keys, keyToWire, notFound
}

// filterThreadRowsByACL drops thread membership rows whose message
// lives in a mailbox not visible to callerPID via ACL (REQ-PROTO-33).
// When all rows in a thread are filtered out the thread key is removed
// entirely so the caller sees it as a not-found id.
func (h *handlerSet) filterThreadRowsByACL(
	ctx context.Context,
	callerPID, ownerPID store.PrincipalID,
	in map[uint64][]store.ThreadMessageRow,
) map[uint64][]store.ThreadMessageRow {
	visible, err := h.accessibleMailboxes(ctx, callerPID, ownerPID)
	if err != nil {
		return map[uint64][]store.ThreadMessageRow{}
	}
	mbSet := make(map[store.MailboxID]struct{}, len(visible))
	for _, mb := range visible {
		mbSet[mb.ID] = struct{}{}
	}
	out := make(map[uint64][]store.ThreadMessageRow, len(in))
	for k, rows := range in {
		kept := make([]store.ThreadMessageRow, 0, len(rows))
		for _, r := range rows {
			m, gerr := h.store.Meta().GetMessage(ctx, r.MessageID)
			if gerr != nil {
				continue
			}
			matched := false
			for _, mm := range m.Mailboxes {
				if _, ok := mbSet[mm.MailboxID]; ok {
					matched = true
					break
				}
			}
			if !matched {
				if _, ok := mbSet[m.MailboxID]; ok {
					matched = true
				}
			}
			if matched {
				kept = append(kept, r)
			}
		}
		if len(kept) > 0 {
			out[k] = kept
		}
	}
	return out
}

// sortThreadRows sorts thread membership rows by ReceivedAt ASC with a
// stable MessageID tiebreak (RFC 8621 §8.1). Mirrors sortThreadMessages
// for the row-shaped result of ListThreadsByKeys.
func sortThreadRows(rows []store.ThreadMessageRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ReceivedAt.Equal(rows[j].ReceivedAt) {
			return rows[i].MessageID < rows[j].MessageID
		}
		return rows[i].ReceivedAt.Before(rows[j].ReceivedAt)
	})
}

// -- Thread/changes ---------------------------------------------------

type changesHandler struct{ h *handlerSet }

func (changesHandler) Method() string { return "Thread/changes" }

func (c changesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req changesRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	ownerPID, e := c.h.resolveAccount(ctx, p, req.AccountID)
	if e != nil {
		return nil, e
	}
	now, err := currentThreadState(ctx, c.h.store.Meta(), ownerPID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := changesResponse{
		AccountID: string(req.AccountID),
		OldState:  req.SinceState,
		NewState:  now,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if req.SinceState == now {
		return resp, nil
	}
	// Parse the sinceState into a change-feed cursor.
	sinceSeq, ok := parseSeq(req.SinceState)
	if !ok {
		return nil, protojmap.NewMethodError("cannotCalculateChanges",
			"unrecognised sinceState; please re-sync")
	}
	// Compute Thread changes from the Email change feed.
	// We read Email change-feed entries after sinceSeq and classify
	// the affected threads into created / updated / destroyed.
	if err := c.computeThreadChanges(ctx, p.ID, ownerPID, sinceSeq, &resp); err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	return resp, nil
}

// computeThreadChanges populates resp.Created/Updated/Destroyed by
// inspecting the Email change feed from sinceSeq onward.
//
// Algorithm (RFC 8620 §5.2 permits over-reporting):
//   - Read Email change feed entries after sinceSeq.
//   - Track which message IDs are brand-new (ChangeOpCreated) since sinceSeq.
//   - For each affected thread: if all its current messages are brand-new →
//     "created"; if it has no surviving messages → "destroyed"; otherwise →
//     "updated".
func (c changesHandler) computeThreadChanges(
	ctx context.Context,
	callerPID, ownerPID store.PrincipalID,
	sinceSeq store.ChangeSeq,
	resp *changesResponse,
) error {
	const maxEntries = 1000
	feed, err := c.h.store.Meta().ReadChangeFeed(ctx, ownerPID, sinceSeq, maxEntries)
	if err != nil {
		return err
	}

	// newMsgIDs: message IDs created since sinceSeq.
	newMsgIDs := map[store.MessageID]struct{}{}
	// affectedThreads: set of thread keys that had any change since sinceSeq.
	affectedThreads := map[ThreadKey]struct{}{}

	for _, entry := range feed {
		if entry.Kind != store.EntityKindEmail {
			continue
		}
		msgID := store.MessageID(entry.EntityID)
		if entry.Op == store.ChangeOpCreated {
			newMsgIDs[msgID] = struct{}{}
		}
		// Load the message to get its thread key.
		msg, merr := c.h.store.Meta().GetMessage(ctx, msgID)
		if merr != nil {
			// Message is gone (destroyed). We cannot recover its thread_id from
			// the change feed. Per RFC 8620 §5.2, over-reporting is permitted:
			// use the message ID as the thread key (correct for singleton threads,
			// and a safe approximation for multi-message threads since the
			// classification pass below will see the thread is absent and emit
			// "destroyed").
			affectedThreads[ThreadKey(uint64(msgID))] = struct{}{}
			continue
		}
		var tk ThreadKey
		if msg.ThreadID != 0 {
			tk = ThreadKey(msg.ThreadID)
		} else {
			tk = ThreadKey(uint64(msg.ID))
		}
		affectedThreads[tk] = struct{}{}
	}

	if len(affectedThreads) == 0 {
		return nil
	}

	// Resolve only the affected threads via the targeted SQL lookup.
	// The legacy path called computeForPrincipal here, which loaded
	// every message in the account on every Thread/changes call —
	// constant ~1.4 s overhead even when the change feed window
	// touched a single thread.
	keys := make([]uint64, 0, len(affectedThreads))
	for tk := range affectedThreads {
		keys = append(keys, uint64(tk))
	}
	threadRows, err := c.h.store.Meta().ListThreadsByKeys(ctx, ownerPID, keys)
	if err != nil {
		return err
	}
	if callerPID != ownerPID {
		threadRows = c.h.filterThreadRowsByACL(ctx, callerPID, ownerPID, threadRows)
	}

	for tk := range affectedThreads {
		tid := renderThreadID(tk)
		rows, exists := threadRows[uint64(tk)]
		if !exists || len(rows) == 0 {
			// Thread has no surviving messages → destroyed.
			resp.Destroyed = append(resp.Destroyed, tid)
			continue
		}
		// Thread is "created" if ALL its current messages are in newMsgIDs.
		allNew := true
		for _, r := range rows {
			if _, isNew := newMsgIDs[r.MessageID]; !isNew {
				allNew = false
				break
			}
		}
		if allNew {
			resp.Created = append(resp.Created, tid)
		} else {
			resp.Updated = append(resp.Updated, tid)
		}
	}
	return nil
}
