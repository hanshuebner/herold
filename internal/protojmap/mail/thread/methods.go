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

// stateString stringifies the per-principal Thread state counter.
func stateString(seq int64) string { return strconv.FormatInt(seq, 10) }

func accountIDForPrincipal(p store.Principal) string {
	return protojmap.AccountIDForPrincipal(p.ID)
}

func validateAccountID(p store.Principal, requested jmapID) *protojmap.MethodError {
	if requested == "" {
		return nil
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
// The JWZ algorithm in jwz.go is the canonical threading logic but is
// not yet wired into the inbound delivery path -- it is intended to run
// at ingest and persist the result into store.Message.ThreadID. Until
// that lands, every message is a singleton thread; clicking a thread
// in the inbox loads the message bodies and the UI works, but
// reply-chain collapsing (multiple emails grouped under one thread)
// does not happen. See TODO(thread/jwz-at-ingest).
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
	st, err := g.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	_, threadToMsgs, err := g.h.computeForPrincipal(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := getResponse{
		AccountID: accountIDForPrincipal(p),
		State:     stateString(st.Thread),
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
	st, err := c.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	now := stateString(st.Thread)
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
	// Without a per-thread change feed we conservatively report the
	// current thread set as updated. Clients re-fetch on observed
	// updates, RFC 8620 §5.2 permits over-reporting.
	_, threadToMsgs, err := c.h.computeForPrincipal(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	for k := range threadToMsgs {
		resp.Updated = append(resp.Updated, renderThreadID(k))
	}
	return resp, nil
}
