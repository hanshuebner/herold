package email

// SQL-pushable Email/query fast path. The legacy Execute path in
// query.go loaded every accessible message for the principal (full
// envelope JOIN), filtered in Go, sorted in Go, and paged in Go —
// constant 1.3 s per call on a ~280k-message account. tryFastEmailQuery
// recognises the dominant suite case (inMailbox + receivedAt sort +
// limit, optionally collapseThreads) and routes it through
// Metadata.QueryEmailFast, which evaluates the filter, sort, and
// pagination at the SQL layer. Predicates the store cannot evaluate
// (hasAttachment, body, non-cached headers, FTS text, thread-keyword
// aggregation) keep the slow path; this file gates entry to the fast
// path on a strict pushability check so the slow path's behaviour is
// never silently weakened.

import (
	"context"
	"errors"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// fastEmailQueryParams is the fast-path translation of a queryRequest
// + decoded emailFilter. nil ok means "not pushable" so the caller
// must use the slow path.
type fastEmailQueryParams struct {
	opts            store.EmailQueryFastOpts
	collapseThreads bool
	limit           int
	position        int
	calculateTotal  bool
}

// tryFastEmailQuery returns (response, true) when the fast path
// produced an authoritative result for req. Returns (nil, false) when
// the request is not SQL-pushable; the caller must fall through to
// the slow path. Errors propagate as serverFail through the caller.
func tryFastEmailQuery(
	ctx context.Context,
	st store.Store,
	pid store.PrincipalID,
	req queryRequest,
	filter *emailFilter,
	state string,
) (*queryResponse, bool, error) {
	params, ok := buildFastEmailQueryParams(req, filter)
	if !ok {
		return nil, false, nil
	}

	// Access fence: when InMailbox is set and the mailbox is not owned
	// by the principal, fall back. The store fast path filters by
	// m.principal_id, which excludes ACL-shared mailboxes; the slow
	// path's listAccessibleMailboxes covers them. Same applies to
	// any inMailboxOtherThan id.
	if params.opts.InMailbox != nil {
		if owned, err := mailboxOwnedByPrincipal(ctx, st.Meta(), pid, *params.opts.InMailbox); err != nil {
			return nil, false, err
		} else if !owned {
			return nil, false, nil
		}
	}
	for _, id := range params.opts.InMailboxOtherThan {
		if owned, err := mailboxOwnedByPrincipal(ctx, st.Meta(), pid, id); err != nil {
			return nil, false, err
		} else if !owned {
			return nil, false, nil
		}
	}

	if params.collapseThreads {
		return runFastCollapsed(ctx, st.Meta(), pid, req, params, state)
	}
	return runFastFlat(ctx, st.Meta(), pid, req, params, state)
}

// buildFastEmailQueryParams translates the JMAP request + filter into
// store.EmailQueryFastOpts when every predicate is SQL-pushable.
// Returns ok=false when any unrepresentable predicate is present.
func buildFastEmailQueryParams(req queryRequest, f *emailFilter) (fastEmailQueryParams, bool) {
	if req.Anchor != nil {
		return fastEmailQueryParams{}, false // anchor pagination not pushable
	}
	if req.Position < 0 {
		return fastEmailQueryParams{}, false // negative position needs total
	}
	limit := -1 // -1 means "no client-supplied limit"
	if req.Limit != nil {
		if *req.Limit < 0 {
			return fastEmailQueryParams{}, false
		}
		limit = *req.Limit
	}

	sortBy, sortAsc, ok := pushableSort(req.Sort)
	if !ok {
		return fastEmailQueryParams{}, false
	}

	opts := store.EmailQueryFastOpts{
		SortBy:        sortBy,
		SortAscending: sortAsc,
	}
	if !mergeFilterIntoOpts(f, &opts) {
		return fastEmailQueryParams{}, false
	}

	params := fastEmailQueryParams{
		opts:            opts,
		collapseThreads: req.CollapseThreads,
		limit:           limit,
		position:        req.Position,
		calculateTotal:  req.CalculateTotal,
	}
	return params, true
}

// pushableSort enforces single-comparator numeric sort (the only sort
// the SQL fast path supports). Empty sort defaults to receivedAt DESC.
func pushableSort(comps []comparator) (store.EmailQueryFastSort, bool, bool) {
	if len(comps) == 0 {
		return store.EmailQueryFastSortReceivedAt, false, true
	}
	if len(comps) > 1 {
		return "", false, false
	}
	c := comps[0]
	asc := c.IsAscending != nil && *c.IsAscending
	switch c.Property {
	case "receivedAt":
		return store.EmailQueryFastSortReceivedAt, asc, true
	case "sentAt":
		return store.EmailQueryFastSortSentAt, asc, true
	case "size":
		return store.EmailQueryFastSortSize, asc, true
	case "id":
		return store.EmailQueryFastSortID, asc, true
	}
	return "", false, false
}

// mergeFilterIntoOpts walks f and copies SQL-pushable predicates into
// opts. Returns false when f contains a predicate the SQL fast path
// cannot evaluate (operator nesting, keyword filters, hasAttachment,
// header[], body:, text:, thread-keyword aggregation, FTS text). The
// caller falls back to the Go-side path in those cases.
func mergeFilterIntoOpts(f *emailFilter, opts *store.EmailQueryFastOpts) bool {
	if f == nil {
		return true
	}
	// FilterOperator nesting is not pushable in phase 1; the SQL
	// builder accepts only a single AND-of-conjuncts so any explicit
	// operator (AND/OR/NOT) drops to the slow path.
	if f.Operator != "" {
		return false
	}
	if f.AllInThreadHaveKeyword != nil ||
		f.SomeInThreadHaveKeyword != nil ||
		f.NoneInThreadHaveKeyword != nil {
		return false
	}
	// hasKeyword / notKeyword are SQL-pushable: system keywords map to
	// the message_mailboxes.flags bitmask, custom keywords (e.g.
	// "$important", "$category-foo") match against keywords_csv. See
	// store.SystemKeywordFlag and Metadata.QueryEmailFast for the
	// per-backend translation. Issue #104: the Important tab's
	// `{ hasKeyword: "$important" }` request used to fall through to the
	// slow path's listPrincipalMessages, blowing past the 1s deadline
	// on a 278k-message account.
	if f.HasKeyword != nil {
		opts.HasKeyword = *f.HasKeyword
	}
	if f.NotKeyword != nil {
		opts.NotKeyword = *f.NotKeyword
	}
	if f.HasAttachment != nil {
		return false
	}
	if len(f.Header) > 0 {
		return false
	}
	if f.Text != nil || f.Body != nil ||
		f.From != nil || f.To != nil || f.Cc != nil || f.Bcc != nil ||
		f.Subject != nil {
		return false
	}

	if f.InMailbox != nil {
		id, ok := mailboxIDFromJMAP(*f.InMailbox)
		if !ok {
			return false
		}
		opts.InMailbox = &id
	}
	if len(f.InMailboxOtherThan) > 0 {
		ids := make([]store.MailboxID, 0, len(f.InMailboxOtherThan))
		for _, raw := range f.InMailboxOtherThan {
			id, ok := mailboxIDFromJMAP(raw)
			if !ok {
				return false
			}
			ids = append(ids, id)
		}
		opts.InMailboxOtherThan = ids
	}
	if f.Before != nil {
		t, err := time.Parse(time.RFC3339, *f.Before)
		if err != nil {
			return false
		}
		opts.Before = &t
	}
	if f.After != nil {
		t, err := time.Parse(time.RFC3339, *f.After)
		if err != nil {
			return false
		}
		opts.After = &t
	}
	if f.MinSize != nil {
		v := *f.MinSize
		opts.MinSize = &v
	}
	if f.MaxSize != nil {
		v := *f.MaxSize
		opts.MaxSize = &v
	}
	return true
}

// runFastFlat handles the non-collapseThreads case: SQL evaluates
// position + limit + total directly.
func runFastFlat(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	req queryRequest,
	p fastEmailQueryParams,
	state string,
) (*queryResponse, bool, error) {
	o := p.opts
	o.Position = p.position
	if p.limit >= 0 {
		o.Limit = p.limit
	}
	o.CalculateTotal = p.calculateTotal

	r, err := meta.QueryEmailFast(ctx, pid, o)
	if err != nil {
		return nil, false, err
	}
	resp := &queryResponse{
		AccountID:  req.AccountID,
		QueryState: state,
		CanCalcCh:  true,
		Position:   p.position,
		IDs:        make([]jmapID, 0, len(r.IDs)),
	}
	for _, id := range r.IDs {
		resp.IDs = append(resp.IDs, jmapIDFromMessage(id))
	}
	if p.calculateTotal {
		t := r.Total
		resp.Total = &t
	}
	if p.limit >= 0 {
		l := p.limit
		resp.Limit = &l
	}
	return resp, true, nil
}

// runFastCollapsed handles collapseThreads. SQL returns ids ordered
// by the requested sort; we walk them in Go, keep the first
// occurrence of each thread_id, and apply position + limit on the
// deduped sequence. Pagination is handled by re-issuing the fast
// query with growing OFFSET when the first batch did not yield enough
// distinct threads — this happens for deep pages or for accounts
// whose threads span many messages each. CalculateTotal is not
// pushable in collapsed mode (would require COUNT(DISTINCT thread_id)
// over the same WHERE; the suite never asks for it in this mode, so
// we drop to the slow path when requested).
func runFastCollapsed(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	req queryRequest,
	p fastEmailQueryParams,
	state string,
) (*queryResponse, bool, error) {
	if p.calculateTotal {
		return nil, false, nil
	}

	want := p.position + p.limit
	if p.limit < 0 {
		// "give me everything"; cap at the store's hard limit minus
		// position. The store will clamp to its internal ceiling
		// regardless, so this is just a sane fetch size.
		want = 50_000
	}
	if want < 1 {
		want = 1
	}

	seenThreads := make(map[uint64]struct{}, want)
	reps := make([]store.MessageID, 0, want)
	cursor := 0
	// fetchSize doubles each iteration so a sparse-thread account
	// converges in O(log N) trips.
	fetchSize := want * 2
	if fetchSize < 200 {
		fetchSize = 200
	}
	for len(reps) < want {
		o := p.opts
		o.Position = cursor
		o.Limit = fetchSize
		o.CalculateTotal = false
		r, err := meta.QueryEmailFast(ctx, pid, o)
		if err != nil {
			return nil, false, err
		}
		if len(r.IDs) == 0 {
			break
		}
		for i, id := range r.IDs {
			// Mirror threadIDForMessage's fallback: a row with thread_id
			// == 0 (importer never linked it to an ancestor) is rendered
			// to the JMAP wire as `t<id>`. Two such rows can therefore
			// collide on the wire-side threadId — e.g. a thread-starter
			// with thread_id=0 plus its replies whose thread_id was set
			// to the starter's id at insert time. Without folding the
			// fallback into the dedup key the fast path would emit both
			// (issue #105: collapseThreads conformance failure).
			key := r.ThreadIDs[i]
			if key == 0 {
				key = uint64(id)
			}
			if _, dup := seenThreads[key]; dup {
				continue
			}
			seenThreads[key] = struct{}{}
			reps = append(reps, id)
			if len(reps) >= want {
				break
			}
		}
		if len(r.IDs) < fetchSize {
			break // exhausted underlying result set
		}
		cursor += fetchSize
		fetchSize *= 2
	}

	start := p.position
	if start > len(reps) {
		start = len(reps)
	}
	end := len(reps)
	if p.limit >= 0 && start+p.limit < end {
		end = start + p.limit
	}

	resp := &queryResponse{
		AccountID:  req.AccountID,
		QueryState: state,
		CanCalcCh:  true,
		Position:   p.position,
		IDs:        make([]jmapID, 0, end-start),
	}
	for _, id := range reps[start:end] {
		resp.IDs = append(resp.IDs, jmapIDFromMessage(id))
	}
	if p.limit >= 0 {
		l := p.limit
		resp.Limit = &l
	}
	return resp, true, nil
}

// mailboxOwnedByPrincipal reports whether mailboxID is directly owned
// by pid (not visible only via ACL). The fast path's SQL fence filters
// by m.principal_id, so ACL-shared mailboxes must drop to the slow
// path.
func mailboxOwnedByPrincipal(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	mailboxID store.MailboxID,
) (bool, error) {
	mb, err := meta.GetMailboxByID(ctx, mailboxID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Nonexistent mailbox: fast path can answer "no rows" via
			// the SQL fence, but only when the JMAP layer is happy with
			// "matches nothing" semantics. Returning false here keeps
			// behaviour identical to the slow path (which would also
			// produce zero rows) without us having to special-case this
			// branch downstream.
			return false, nil
		}
		return false, err
	}
	return mb.PrincipalID == pid, nil
}
