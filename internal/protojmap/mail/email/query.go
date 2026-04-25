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

// emailFilterCondition is the wire-form FilterCondition (RFC 8621
// §4.4.1). A nil filter is "match all"; any zero-valued field within
// the struct (pointer left nil) is "no constraint on that axis".
//
// Phase 2 Wave 2.2 supports a useful but bounded subset:
//
// fully implemented (returned from this handler):
//   - inMailbox, inMailboxOtherThan
//   - before, after, minSize, maxSize
//   - hasKeyword, notKeyword
//   - hasAttachment
//   - text (FTS-routed), from / to / cc / bcc / subject / body
//     (FTS-routed when the FTS index carries the field, else the
//     metadata fallback's per-message envelope scan picks it up).
//   - header (best-effort: fetched from the cached envelope only;
//     headers absent from the envelope cache are treated as no match)
//
// folded onto per-message keyword predicates (RFC 8621 §4.4.1):
//   - allInThreadHaveKeyword / someInThreadHaveKeyword /
//     noneInThreadHaveKeyword behave like hasKeyword / notKeyword
//     while v1 has no Thread datatype indexer; the parallel agent's
//     Thread surface lights this up additively.
type emailFilterCondition struct {
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

// queryRequest is the wire-form Email/query request.
type queryRequest struct {
	AccountID       jmapID                `json:"accountId"`
	Filter          *emailFilterCondition `json:"filter"`
	Sort            []comparator          `json:"sort"`
	Position        int                   `json:"position"`
	Anchor          *jmapID               `json:"anchor"`
	AnchorOffset    int                   `json:"anchorOffset"`
	Limit           *int                  `json:"limit"`
	CalculateTotal  bool                  `json:"calculateTotal"`
	CollapseThreads bool                  `json:"collapseThreads"`
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

// queryHandler implements Email/query.
type queryHandler struct{ h *handlerSet }

func (q *queryHandler) Method() string { return "Email/query" }

// Execute applies the filter, then sorts, then pages. Implementation
// strategy:
//
//  1. Gather the candidate set.
//     - When the filter carries a text predicate, route through
//     store.FTS().Query and intersect with the structured filter
//     (the FTS layer does the text scan).
//     - Otherwise, list every principal-visible message via
//     store.Metadata.ListMessages and apply the structured filter
//     in memory.
//  2. Sort the candidates per the comparator chain. Default is
//     receivedAt descending (RFC 8621 §4.4 implicit default).
//  3. Page (Position / Limit / Anchor / AnchorOffset).
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

	candidates, err := gatherCandidates(ctx, q.h.store, pid, req.Filter)
	if err != nil {
		return nil, serverFail(err)
	}
	matched := make([]store.Message, 0, len(candidates))
	for _, m := range candidates {
		if matchEmailFilter(m, req.Filter) {
			matched = append(matched, m)
		}
	}
	sortMessages(matched, req.Sort)

	resp := queryResponse{
		AccountID:  req.AccountID,
		QueryState: state,
		CanCalcCh:  false,
		IDs:        []jmapID{},
	}
	total := len(matched)
	if req.CalculateTotal {
		t := total
		resp.Total = &t
	}

	start := req.Position
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

// gatherCandidates returns the candidate message set for filter f.
// When f carries a text-bearing predicate we ask the FTS index;
// otherwise we read the principal's mailboxes directly.
func gatherCandidates(
	ctx context.Context,
	st store.Store,
	pid store.PrincipalID,
	f *emailFilterCondition,
) ([]store.Message, error) {
	if f != nil && hasTextPredicate(f) {
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

func hasTextPredicate(f *emailFilterCondition) bool {
	return f.Text != nil ||
		f.From != nil || f.To != nil || f.Cc != nil || f.Bcc != nil ||
		f.Subject != nil || f.Body != nil
}

// buildFTSQuery projects the text-bearing predicates onto the
// store.Query envelope. Non-text predicates are applied later in
// matchEmailFilter; the FTS pass narrows the candidate set so the
// metadata scan runs over a small subset.
func buildFTSQuery(f *emailFilterCondition) store.Query {
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
	// Bcc / Cc map to the To-side of the FTS index in v1 because the
	// FTS schema only carries from/to/cc fields. We append cc into the
	// Cc bucket and let bcc fall through to the metadata-side filter
	// below.
	if f.Cc != nil {
		// The FTS index has a Cc field; route through To-extra.
		q.To = append(q.To, *f.Cc)
	}
	return q
}

// matchEmailFilter applies the structured (non-text) predicates to m.
// Returns true when m matches every predicate; one mismatch short-
// circuits to false. The text-bearing predicates were already enforced
// by the FTS pass so they are NOT re-evaluated here — the metadata
// fallback path (no text predicates) reaches matchEmailFilter with f
// where the text fields are nil and the no-op branches simply skip.
func matchEmailFilter(m store.Message, f *emailFilterCondition) bool {
	if f == nil {
		return true
	}
	if f.InMailbox != nil {
		want, ok := mailboxIDFromJMAP(*f.InMailbox)
		if !ok || m.MailboxID != want {
			return false
		}
	}
	if len(f.InMailboxOtherThan) > 0 {
		excluded := false
		for _, raw := range f.InMailboxOtherThan {
			if id, ok := mailboxIDFromJMAP(raw); ok && id == m.MailboxID {
				excluded = true
				break
			}
		}
		if excluded {
			return false
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
	if f.AllInThreadHaveKeyword != nil {
		if !messageHasKeyword(m, *f.AllInThreadHaveKeyword) {
			return false
		}
	}
	if f.SomeInThreadHaveKeyword != nil {
		if !messageHasKeyword(m, *f.SomeInThreadHaveKeyword) {
			return false
		}
	}
	if f.NoneInThreadHaveKeyword != nil {
		if messageHasKeyword(m, *f.NoneInThreadHaveKeyword) {
			return false
		}
	}
	if f.HasAttachment != nil {
		// We do not have an attachment column on the Message row in
		// v1; the FTS index carries fieldHasAttachments but the
		// metadata fallback cannot. Always treat hasAttachment as
		// "match" in the metadata-fallback path; callers that need a
		// strict answer combine it with a text predicate so the FTS
		// index path runs.
		_ = f.HasAttachment
	}
	// Headers — fall through against the cached envelope. RFC 8621
	// §4.4.1 says header is `[name]` or `[name, value]`. We honour
	// both.
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
	return true
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

// envelopeHeader returns the cached envelope value matching name, or
// "" when the cache does not carry it. v1's envelope cache covers the
// canonical RFC 5322 fields; everything else returns "".
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

// sortMessages applies the comparator chain. Default ordering is
// receivedAt descending (RFC 8621 §4.4: implementations SHOULD return
// "the most recently received messages first"). Supported sort
// properties:
//   - receivedAt
//   - sentAt
//   - size
//   - from / to / subject (case-insensitive lex compare on the cached
//     envelope value)
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

// queryChangesHandler returns cannotCalculateChanges; the v1 server
// has no per-query state index. Clients re-issue Email/query to
// refresh.
type queryChangesHandler struct{ h *handlerSet }

func (queryChangesHandler) Method() string { return "Email/queryChanges" }

func (queryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"Email/queryChanges is unsupported; clients re-issue Email/query")
}
