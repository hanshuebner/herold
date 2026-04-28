package thread

import (
	"context"
	"encoding/json"
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

func accountIDForPrincipal(p store.Principal) string {
	return protojmap.AccountIDForPrincipal(p.ID)
}

// validateAccountID checks the inbound accountId against the
// authenticated principal. An absent accountId is rejected with
// "invalidArguments" per RFC 8620 §5.1; a mismatched one returns
// "accountNotFound".
func validateAccountID(p store.Principal, requested jmapID) *protojmap.MethodError {
	if requested == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if requested != accountIDForPrincipal(p) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// listAllMessages returns every message owned by p across every
// mailbox. Phase 1's store does not expose a per-principal "all
// messages" iterator; we fan out across the principal's mailboxes. The
// caller is single-threaded JMAP so the cost is bounded by the
// principal's mailbox + message count.
func (h *handlerSet) listAllMessages(ctx context.Context, p store.Principal) ([]store.Message, error) {
	mboxes, err := h.store.Meta().ListMailboxes(ctx, p.ID)
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

// computeForPrincipal returns the (msg→thread, thread→[msg]) maps for
// p's whole account.
//
// v1 keys threads by store.Message.ThreadID (falling back to MessageID
// when ThreadID is 0). This matches Email/get's threadIDForMessage --
// both render "t<m.ThreadID>" or, for un-threaded messages,
// "t<m.ID>" -- so a client that takes Email.threadId and passes it
// back into Thread/get always resolves to a thread row.
//
// Thread assignment happens at ingest time: InsertMessage resolves
// references via ParseReferences(env_in_reply_to) and looks up ancestor
// messages by env_message_id in the same principal's mailboxes. The
// resolved thread_id is persisted so this read path is a simple group-by.
func (h *handlerSet) computeForPrincipal(ctx context.Context, p store.Principal) (map[store.MessageID]ThreadKey, map[ThreadKey][]store.MessageID, error) {
	msgs, err := h.listAllMessages(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	msgToThread := make(map[store.MessageID]ThreadKey, len(msgs))
	threadToMsgs := make(map[ThreadKey][]store.MessageID)
	for _, m := range msgs {
		var key ThreadKey
		if m.ThreadID != 0 {
			key = ThreadKey(m.ThreadID)
		} else {
			key = ThreadKey(uint64(m.ID))
		}
		msgToThread[m.ID] = key
		threadToMsgs[key] = append(threadToMsgs[key], m.ID)
	}
	return msgToThread, threadToMsgs, nil
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
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	state, err := currentThreadState(ctx, g.h.store.Meta(), p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	_, threadToMsgs, err := g.h.computeForPrincipal(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := getResponse{
		AccountID: accountIDForPrincipal(p),
		State:     state,
		List:      []jmapThread{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		// Return all threads.
		for k, ids := range threadToMsgs {
			t := jmapThread{ID: renderThreadID(k), EmailIDs: make([]jmapID, 0, len(ids))}
			for _, id := range ids {
				t.EmailIDs = append(t.EmailIDs, renderEmailID(id))
			}
			resp.List = append(resp.List, t)
		}
		return resp, nil
	}
	for _, id := range *req.IDs {
		k, ok := parseThreadID(id)
		if !ok {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		ids, ok := threadToMsgs[k]
		if !ok {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		t := jmapThread{ID: id, EmailIDs: make([]jmapID, 0, len(ids))}
		for _, mid := range ids {
			t.EmailIDs = append(t.EmailIDs, renderEmailID(mid))
		}
		resp.List = append(resp.List, t)
	}
	return resp, nil
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
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	now, err := currentThreadState(ctx, c.h.store.Meta(), p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := changesResponse{
		AccountID: accountIDForPrincipal(p),
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
	if err := c.computeThreadChanges(ctx, p, sinceSeq, &resp); err != nil {
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
	p store.Principal,
	sinceSeq store.ChangeSeq,
	resp *changesResponse,
) error {
	const maxEntries = 1000
	feed, err := c.h.store.Meta().ReadChangeFeed(ctx, p.ID, sinceSeq, maxEntries)
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
			// Message is gone (destroyed); we cannot recover its thread key from
			// the change feed alone. The destroyed thread may still appear as
			// empty in threadToMsgs below.
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

	// Load all current threads to classify creates vs updates vs destroys.
	_, threadToMsgs, err := c.h.computeForPrincipal(ctx, p)
	if err != nil {
		return err
	}

	for tk := range affectedThreads {
		tid := renderThreadID(tk)
		msgIDs, exists := threadToMsgs[tk]
		if !exists {
			// Thread has no surviving messages → destroyed.
			resp.Destroyed = append(resp.Destroyed, tid)
			continue
		}
		// Thread is "created" if ALL its current messages are in newMsgIDs.
		allNew := true
		for _, mid := range msgIDs {
			if _, isNew := newMsgIDs[mid]; !isNew {
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
