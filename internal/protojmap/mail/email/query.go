package email

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// emailFilter is the union of FilterCondition and FilterOperator per
// RFC 8621 §4.4. The two shapes are distinguished by the presence of
// the "operator" field. We decode via json.RawMessage and pick the
// right struct in decodeFilter.
type emailFilter struct {
	// FilterOperator fields (RFC 8621 §4.4.2)
	Operator   string            `json:"operator"`
	Conditions []json.RawMessage `json:"conditions"`

	// FilterCondition fields (RFC 8621 §4.4.1)
	InMailbox               *jmapID  `json:"inMailbox"`
	InMailboxOtherThan      []jmapID `json:"inMailboxOtherThan"`
	Before                  *string  `json:"before"`
	After                   *string  `json:"after"`
	MinSize                 *int64   `json:"minSize"`
	MaxSize                 *int64   `json:"maxSize"`
	AllInThreadHaveKeyword  *string  `json:"allInThreadHaveKeyword"`
	SomeInThreadHaveKeyword *string  `json:"someInThreadHaveKeyword"`
	NoneInThreadHaveKeyword *string  `json:"noneInThreadHaveKeyword"`
	HasKeyword              *string  `json:"hasKeyword"`
	NotKeyword              *string  `json:"notKeyword"`
	HasAttachment           *bool    `json:"hasAttachment"`
	Text                    *string  `json:"text"`
	From                    *string  `json:"from"`
	To                      *string  `json:"to"`
	Cc                      *string  `json:"cc"`
	Bcc                     *string  `json:"bcc"`
	Subject                 *string  `json:"subject"`
	Body                    *string  `json:"body"`
	Header                  []string `json:"header"`
}

// emailFilterCondition is a type alias kept for backward compatibility
// with callers that import the type; the actual filter struct is emailFilter.
type emailFilterCondition = emailFilter

// queryRequest is the wire-form Email/query request.
type queryRequest struct {
	AccountID       jmapID           `json:"accountId"`
	Filter          *json.RawMessage `json:"filter"`
	Sort            []comparator     `json:"sort"`
	Position        int              `json:"position"`
	Anchor          *jmapID          `json:"anchor"`
	AnchorOffset    int              `json:"anchorOffset"`
	Limit           *int             `json:"limit"`
	CalculateTotal  bool             `json:"calculateTotal"`
	CollapseThreads bool             `json:"collapseThreads"`
}

// comparator is the wire-form sort spec.
type comparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending"`
	Collation   string `json:"collation,omitempty"`
}

// queryResponse is the wire-form response.
type queryResponse struct {
	AccountID  jmapID   `json:"accountId"`
	QueryState string   `json:"queryState"`
	CanCalcCh  bool     `json:"canCalculateChanges"`
	Position   int      `json:"position"`
	IDs        []jmapID `json:"ids"`
	Total      *int     `json:"total,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
}

// decodeFilter parses a raw JSON filter into an *emailFilter. Returns
// nil when raw is nil or null (match all).
func decodeFilter(raw *json.RawMessage) (*emailFilter, error) {
	if raw == nil || string(*raw) == "null" {
		return nil, nil
	}
	var f emailFilter
	if err := json.Unmarshal(*raw, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// queryHandler implements Email/query.
type queryHandler struct{ h *handlerSet }

func (q *queryHandler) Method() string { return "Email/query" }

// Execute applies the filter, then sorts, then pages.
func (q *queryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req queryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentState(ctx, q.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	filter, ferr := decodeFilter(req.Filter)
	if ferr != nil {
		return nil, protojmap.NewMethodError("invalidArguments", ferr.Error())
	}

	// For thread keyword filters we need all messages to do cross-thread
	// aggregation. Gather all principal messages regardless.
	var allMessages []store.Message
	var gatherErr error
	needAllForThread := filter != nil && filterNeedsThreadAgg(filter)
	if needAllForThread {
		allMessages, gatherErr = listPrincipalMessages(ctx, q.h.store.Meta(), pid)
	} else {
		allMessages, gatherErr = gatherCandidatesRaw(ctx, q.h.store, pid, filter)
	}
	if gatherErr != nil {
		return nil, serverFail(gatherErr)
	}

	matched := filterMessagesWithCtx(allMessages, filter, allMessages)
	sortMessages(matched, req.Sort)

	// collapseThreads: keep only the sort-order representative of each thread.
	if req.CollapseThreads {
		matched = collapseByThread(matched)
	}

	resp := queryResponse{
		AccountID:  req.AccountID,
		QueryState: state,
		CanCalcCh:  true,
		IDs:        []jmapID{},
	}
	total := len(matched)
	if req.CalculateTotal {
		t := total
		resp.Total = &t
	}

	// RFC 8620 §5.5 paging — anchor takes priority over position.
	if req.Anchor != nil {
		anchorID := *req.Anchor
		anchorIdx := -1
		for i, m := range matched {
			if jmapIDFromMessage(m.ID) == anchorID {
				anchorIdx = i
				break
			}
		}
		if anchorIdx < 0 {
			return nil, protojmap.NewMethodError("anchorNotFound",
				"anchor id not found in query results")
		}
		start := anchorIdx + req.AnchorOffset
		if start < 0 {
			start = 0
		}
		if start > total {
			start = total
		}
		end := total
		if req.Limit != nil && *req.Limit >= 0 {
			l := *req.Limit
			if start+l < end {
				end = start + l
			}
			resp.Limit = req.Limit
		}
		resp.Position = start
		for _, m := range matched[start:end] {
			resp.IDs = append(resp.IDs, jmapIDFromMessage(m.ID))
		}
		return resp, nil
	}

	start := req.Position
	if start < 0 {
		// RFC 8620 §5.5: negative position counts from end.
		start = total + start
		if start < 0 {
			start = 0
		}
	}
	if start > total {
		start = total
	}
	end := total
	if req.Limit != nil && *req.Limit >= 0 {
		l := *req.Limit
		if start+l < end {
			end = start + l
		}
		resp.Limit = req.Limit
	}
	resp.Position = start
	for _, m := range matched[start:end] {
		resp.IDs = append(resp.IDs, jmapIDFromMessage(m.ID))
	}
	return resp, nil
}

// filterNeedsThreadAgg reports whether the filter requires cross-thread
// aggregation (someInThreadHaveKeyword / noneInThreadHaveKeyword).
func filterNeedsThreadAgg(f *emailFilter) bool {
	if f == nil {
		return false
	}
	if f.Operator != "" {
		for _, raw := range f.Conditions {
			var sub emailFilter
			if err := json.Unmarshal(raw, &sub); err == nil {
				if filterNeedsThreadAgg(&sub) {
					return true
				}
			}
		}
		return false
	}
	return f.SomeInThreadHaveKeyword != nil || f.NoneInThreadHaveKeyword != nil
}

// filterMessages applies a filter to candidates with the full message set
// for thread-keyword aggregation. When thread aggregation is not needed
// allMessages may equal candidates.
func filterMessages(candidates []store.Message, f *emailFilter) []store.Message {
	return filterMessagesWithCtx(candidates, f, candidates)
}

// filterMessagesWithCtx applies a filter; allMessages is the full set
// used for thread-aware predicates.
func filterMessagesWithCtx(candidates []store.Message, f *emailFilter, allMessages []store.Message) []store.Message {
	out := make([]store.Message, 0, len(candidates))
	for _, m := range candidates {
		if matchFilter(m, f, allMessages) {
			out = append(out, m)
		}
	}
	return out
}

// matchFilter evaluates f against m. allMessages is passed for thread
// aggregation predicates.
func matchFilter(m store.Message, f *emailFilter, all []store.Message) bool {
	if f == nil {
		return true
	}
	// FilterOperator: operator + conditions
	if f.Operator != "" {
		return matchOperator(m, f, all)
	}
	return matchCondition(m, f, all)
}

// matchOperator handles FilterOperator (AND / OR / NOT).
func matchOperator(m store.Message, f *emailFilter, all []store.Message) bool {
	op := strings.ToUpper(f.Operator)
	switch op {
	case "AND":
		for _, raw := range f.Conditions {
			var sub emailFilter
			if err := json.Unmarshal(raw, &sub); err != nil {
				return false
			}
			if !matchFilter(m, &sub, all) {
				return false
			}
		}
		return true
	case "OR":
		for _, raw := range f.Conditions {
			var sub emailFilter
			if err := json.Unmarshal(raw, &sub); err != nil {
				continue
			}
			if matchFilter(m, &sub, all) {
				return true
			}
		}
		return false
	case "NOT":
		// NOT is defined as "not ANY of the conditions" (logical NOR)
		// per RFC 8621 §4.4.2: "This MUST NOT match if any of the
		// conditions match." In practice clients use a single condition.
		for _, raw := range f.Conditions {
			var sub emailFilter
			if err := json.Unmarshal(raw, &sub); err != nil {
				return false
			}
			if matchFilter(m, &sub, all) {
				return false
			}
		}
		return true
	}
	return false
}

// matchCondition evaluates a FilterCondition against m.
func matchCondition(m store.Message, f *emailFilter, all []store.Message) bool {
	if f.InMailbox != nil {
		want, ok := mailboxIDFromJMAP(*f.InMailbox)
		if !ok || m.MailboxID != want {
			return false
		}
	}
	if len(f.InMailboxOtherThan) > 0 {
		for _, raw := range f.InMailboxOtherThan {
			if id, ok := mailboxIDFromJMAP(raw); ok && id == m.MailboxID {
				return false
			}
		}
	}
	if f.Before != nil {
		t, err := time.Parse(time.RFC3339, *f.Before)
		if err == nil && !m.ReceivedAt.Before(t) {
			return false
		}
	}
	if f.After != nil {
		t, err := time.Parse(time.RFC3339, *f.After)
		if err == nil && !m.ReceivedAt.After(t) && !m.ReceivedAt.Equal(t) {
			return false
		}
	}
	if f.MinSize != nil && m.Size < *f.MinSize {
		return false
	}
	if f.MaxSize != nil && m.Size > *f.MaxSize {
		return false
	}
	if f.HasKeyword != nil {
		if !messageHasKeyword(m, *f.HasKeyword) {
			return false
		}
	}
	if f.NotKeyword != nil {
		if messageHasKeyword(m, *f.NotKeyword) {
			return false
		}
	}
	// Thread-aware keyword predicates: aggregate across thread siblings.
	if f.AllInThreadHaveKeyword != nil {
		kw := *f.AllInThreadHaveKeyword
		tid := threadIDForMessage(m)
		for _, sibling := range all {
			if threadIDForMessage(sibling) == tid {
				if !messageHasKeyword(sibling, kw) {
					return false
				}
			}
		}
	}
	if f.SomeInThreadHaveKeyword != nil {
		kw := *f.SomeInThreadHaveKeyword
		tid := threadIDForMessage(m)
		found := false
		for _, sibling := range all {
			if threadIDForMessage(sibling) == tid && messageHasKeyword(sibling, kw) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.NoneInThreadHaveKeyword != nil {
		kw := *f.NoneInThreadHaveKeyword
		tid := threadIDForMessage(m)
		for _, sibling := range all {
			if threadIDForMessage(sibling) == tid && messageHasKeyword(sibling, kw) {
				return false
			}
		}
	}
	if f.HasAttachment != nil {
		// hasAttachment requires parsing the MIME structure. In the
		// metadata-only path the FTS index carries the flag; here we
		// check the store's HasAttachment flag (set at ingest time via
		// the FTS indexer) and fall back to false for messages not yet
		// indexed. This is best-effort in v1; a post-ingest migration
		// can backfill the flag.
		//
		// For now: messages imported via Email/import go through the FTS
		// indexer which currently always writes false. Until the indexer
		// populates the flag from the parsed MIME tree, hasAttachment
		// filter will exclude messages that truly have attachments. This
		// is noted as a known gap.
		_ = f.HasAttachment
		// Pass through without filtering — avoids false-negative exclusion.
	}
	// Header filter: name-only or name+value against the envelope cache.
	if len(f.Header) > 0 {
		name := strings.ToLower(strings.TrimSpace(f.Header[0]))
		var wantValue string
		if len(f.Header) > 1 {
			wantValue = strings.ToLower(strings.TrimSpace(f.Header[1]))
		}
		got := envelopeHeader(m, name)
		if got == "" {
			return false
		}
		if wantValue != "" && !strings.Contains(strings.ToLower(got), wantValue) {
			return false
		}
	}
	// Body / text predicates: when we reach here via the non-FTS path these
	// were not pre-filtered; fall back to envelope-header substring match.
	if f.Body != nil {
		// Body search requires FTS; in the metadata path we skip.
		// The gatherCandidatesRaw already routes body queries through FTS.
	}
	return true
}

// gatherCandidatesRaw returns the candidate message set for filter f
// without thread aggregation. Text predicates are routed through FTS.
func gatherCandidatesRaw(
	ctx context.Context,
	st store.Store,
	pid store.PrincipalID,
	f *emailFilter,
) ([]store.Message, error) {
	if f != nil && filterHasTextPredicate(f) {
		fts := buildFTSQuery(f)
		hits, err := st.FTS().Query(ctx, pid, fts)
		if err != nil {
			return nil, err
		}
		out := make([]store.Message, 0, len(hits))
		for _, h := range hits {
			m, err := loadMessageForPrincipal(ctx, st.Meta(), pid, h.MessageID)
			if err != nil {
				continue
			}
			out = append(out, m)
		}
		return out, nil
	}
	return listPrincipalMessages(ctx, st.Meta(), pid)
}

// gatherCandidates is the legacy entry point used by queryChanges.
func gatherCandidates(
	ctx context.Context,
	st store.Store,
	pid store.PrincipalID,
	f *emailFilter,
) ([]store.Message, error) {
	return gatherCandidatesRaw(ctx, st, pid, f)
}

// filterHasTextPredicate reports whether f (or any nested condition) has
// a text-bearing predicate that should be routed through FTS.
func filterHasTextPredicate(f *emailFilter) bool {
	if f == nil {
		return false
	}
	if f.Operator != "" {
		for _, raw := range f.Conditions {
			var sub emailFilter
			if err := json.Unmarshal(raw, &sub); err == nil {
				if filterHasTextPredicate(&sub) {
					return true
				}
			}
		}
		return false
	}
	return f.Text != nil || f.From != nil || f.To != nil || f.Cc != nil ||
		f.Bcc != nil || f.Subject != nil || f.Body != nil
}

// buildFTSQuery projects the text-bearing predicates onto the
// store.Query envelope. Non-text predicates are applied later in
// matchCondition; the FTS pass narrows the candidate set.
func buildFTSQuery(f *emailFilter) store.Query {
	q := store.Query{}
	if f.InMailbox != nil {
		if id, ok := mailboxIDFromJMAP(*f.InMailbox); ok {
			q.MailboxID = id
		}
	}
	if f.Text != nil {
		q.Text = *f.Text
	}
	if f.Subject != nil {
		q.Subject = []string{*f.Subject}
	}
	if f.From != nil {
		q.From = []string{*f.From}
	}
	if f.To != nil {
		q.To = []string{*f.To}
	}
	if f.Body != nil {
		q.Body = []string{*f.Body}
	}
	if f.Cc != nil {
		q.To = append(q.To, *f.Cc)
	}
	return q
}

func messageHasKeyword(m store.Message, kw string) bool {
	kw = strings.ToLower(kw)
	switch kw {
	case "$seen":
		return m.Flags&store.MessageFlagSeen != 0
	case "$answered":
		return m.Flags&store.MessageFlagAnswered != 0
	case "$flagged":
		return m.Flags&store.MessageFlagFlagged != 0
	case "$draft":
		return m.Flags&store.MessageFlagDraft != 0
	}
	for _, k := range m.Keywords {
		if strings.EqualFold(k, kw) {
			return true
		}
	}
	return false
}

// envelopeHeader returns the cached envelope value for name, or "" when
// not present. Covers the canonical RFC 5322 header fields cached on
// store.Message.Envelope.
func envelopeHeader(m store.Message, name string) string {
	switch strings.ToLower(name) {
	case "subject":
		return m.Envelope.Subject
	case "from":
		return m.Envelope.From
	case "to":
		return m.Envelope.To
	case "cc":
		return m.Envelope.Cc
	case "bcc":
		return m.Envelope.Bcc
	case "reply-to":
		return m.Envelope.ReplyTo
	case "message-id":
		return m.Envelope.MessageID
	case "in-reply-to":
		return m.Envelope.InReplyTo
	}
	return ""
}

// sortMessages applies the comparator chain.
func sortMessages(xs []store.Message, comps []comparator) {
	if len(comps) == 0 {
		comps = []comparator{{Property: "receivedAt"}}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		for _, c := range comps {
			asc := c.IsAscending != nil && *c.IsAscending
			cmp := compareMessage(xs[i], xs[j], c.Property)
			if cmp == 0 {
				continue
			}
			if asc {
				return cmp < 0
			}
			return cmp > 0
		}
		return xs[i].ID > xs[j].ID
	})
}

func compareMessage(a, b store.Message, property string) int {
	switch property {
	case "receivedAt":
		return compareTime(a.ReceivedAt, b.ReceivedAt)
	case "sentAt":
		return compareTime(a.Envelope.Date, b.Envelope.Date)
	case "size":
		return compareInt64(a.Size, b.Size)
	case "from":
		return strings.Compare(strings.ToLower(a.Envelope.From), strings.ToLower(b.Envelope.From))
	case "to":
		return strings.Compare(strings.ToLower(a.Envelope.To), strings.ToLower(b.Envelope.To))
	case "subject":
		return strings.Compare(strings.ToLower(a.Envelope.Subject), strings.ToLower(b.Envelope.Subject))
	}
	return 0
}

func compareTime(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	default:
		return 0
	}
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// collapseByThread keeps only the first occurrence of each thread in
// the already-sorted slice, preserving sort order. The first message
// seen for each thread is the "representative" per RFC 8621 §4.4.3.
func collapseByThread(xs []store.Message) []store.Message {
	seen := make(map[jmapID]struct{}, len(xs))
	out := xs[:0]
	for _, m := range xs {
		tid := threadIDForMessage(m)
		if _, dup := seen[tid]; dup {
			continue
		}
		seen[tid] = struct{}{}
		out = append(out, m)
	}
	return out
}

// matchEmailFilter is kept for backward compatibility with callers in
// this package that predate the operator-aware rewrite.
func matchEmailFilter(m store.Message, f *emailFilter) bool {
	return matchFilter(m, f, nil)
}

// queryChangesRequest is the wire-form Email/queryChanges request (RFC 8620 §5.6).
type queryChangesRequest struct {
	AccountID       jmapID           `json:"accountId"`
	Filter          *json.RawMessage `json:"filter"`
	Sort            []comparator     `json:"sort"`
	SinceQueryState string           `json:"sinceQueryState"`
	MaxChanges      *int             `json:"maxChanges"`
	UpToID          *jmapID          `json:"upToId"`
	CalculateTotal  bool             `json:"calculateTotal"`
	CollapseThreads bool             `json:"collapseThreads"`
}

// queryChangesAddedItem is a (id, index) pair in the added list.
type queryChangesAddedItem struct {
	ID    jmapID `json:"id"`
	Index int    `json:"index"`
}

// queryChangesResponse is the wire-form Email/queryChanges response.
type queryChangesResponse struct {
	AccountID     jmapID                  `json:"accountId"`
	OldQueryState string                  `json:"oldQueryState"`
	NewQueryState string                  `json:"newQueryState"`
	Total         *int                    `json:"total,omitempty"`
	Removed       []jmapID                `json:"removed"`
	Added         []queryChangesAddedItem `json:"added"`
}

// queryChangesHandler implements Email/queryChanges (RFC 8620 §5.6).
type queryChangesHandler struct{ h *handlerSet }

func (queryChangesHandler) Method() string { return "Email/queryChanges" }

func (qc queryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}

	var req queryChangesRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	filter, ferr := decodeFilter(req.Filter)
	if ferr != nil {
		return nil, protojmap.NewMethodError("invalidArguments", ferr.Error())
	}

	since, ok := parseState(req.SinceQueryState)
	if !ok {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "unparseable sinceQueryState")
	}

	newSeq, err := qc.h.store.Meta().GetMaxChangeSeqForKind(ctx, pid, store.EntityKindEmail)
	if err != nil {
		return nil, serverFail(err)
	}
	newQueryState := stateFromSeq(newSeq)

	resp := queryChangesResponse{
		AccountID:     req.AccountID,
		OldQueryState: req.SinceQueryState,
		NewQueryState: newQueryState,
		Removed:       []jmapID{},
		Added:         []queryChangesAddedItem{},
	}

	if since > newSeq {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceQueryState is in the future")
	}

	// Collect changed IDs from the feed.
	const page = 1000
	var cursor store.ChangeSeq = since
	created := map[store.MessageID]struct{}{}
	updated := map[store.MessageID]struct{}{}
	destroyed := map[store.MessageID]struct{}{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, serverFail(err)
		}
		batch, ferr := qc.h.store.Meta().ReadChangeFeed(ctx, pid, cursor, page)
		if ferr != nil {
			return nil, serverFail(ferr)
		}
		for _, entry := range batch {
			cursor = entry.Seq
			if entry.Kind != store.EntityKindEmail {
				continue
			}
			id := store.MessageID(entry.EntityID)
			switch entry.Op {
			case store.ChangeOpCreated:
				delete(destroyed, id)
				created[id] = struct{}{}
			case store.ChangeOpUpdated:
				if _, isCreated := created[id]; isCreated {
					continue
				}
				if _, gone := destroyed[id]; gone {
					continue
				}
				updated[id] = struct{}{}
			case store.ChangeOpDestroyed:
				if _, isCreated := created[id]; isCreated {
					delete(created, id)
					continue
				}
				delete(updated, id)
				destroyed[id] = struct{}{}
			}
		}
		if len(batch) < page {
			break
		}
	}

	if since == newSeq {
		if req.CalculateTotal {
			candidates, err := gatherCandidates(ctx, qc.h.store, pid, filter)
			if err != nil {
				return nil, serverFail(err)
			}
			matched := filterMessages(candidates, filter)
			t := len(matched)
			resp.Total = &t
		}
		return resp, nil
	}

	// Build the current filtered, sorted result set.
	candidates, err := gatherCandidates(ctx, qc.h.store, pid, filter)
	if err != nil {
		return nil, serverFail(err)
	}
	matched := filterMessages(candidates, filter)
	sortMessages(matched, req.Sort)

	// Build an id→position map for the current result set.
	posMap := make(map[store.MessageID]int, len(matched))
	for i, m := range matched {
		posMap[m.ID] = i
	}

	// Destroyed: unconditionally removed from the query result.
	for id := range destroyed {
		resp.Removed = append(resp.Removed, jmapIDFromMessage(id))
	}

	// Updated: remove + re-add if still in result.
	for id := range updated {
		jid := jmapIDFromMessage(id)
		resp.Removed = append(resp.Removed, jid)
		if pos, inResult := posMap[id]; inResult {
			resp.Added = append(resp.Added, queryChangesAddedItem{ID: jid, Index: pos})
		}
	}

	// Created: add if they match the current filter.
	for id := range created {
		if pos, inResult := posMap[id]; inResult {
			jid := jmapIDFromMessage(id)
			resp.Added = append(resp.Added, queryChangesAddedItem{ID: jid, Index: pos})
		}
	}

	if req.CalculateTotal {
		t := len(matched)
		resp.Total = &t
	}

	return resp, nil
}
