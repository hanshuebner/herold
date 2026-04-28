package mailbox

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// filterCondition is the wire-form Mailbox FilterCondition (RFC 8621
// §2.3). Pointer fields encode "absent" so a zero-valued struct is
// "match all".
//
// ParentID uses json.RawMessage so we can distinguish three cases:
//   - absent (nil RawMessage): no filter on parentId
//   - JSON null: filter for top-level mailboxes (parentId == 0)
//   - JSON string: filter for mailboxes with that specific parent
type filterCondition struct {
	ParentID     json.RawMessage `json:"parentId"`
	Name         *string         `json:"name"`
	Role         *string         `json:"role"`
	HasAnyRole   *bool           `json:"hasAnyRole"`
	IsSubscribed *bool           `json:"isSubscribed"`
}

// comparator is the wire-form sort spec (RFC 8620 §5.5).
type comparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending"`
	Collation   string `json:"collation,omitempty"`
}

// queryRequest is the wire-form Mailbox/query request.
type queryRequest struct {
	AccountID      jmapID           `json:"accountId"`
	Filter         *filterCondition `json:"filter"`
	Sort           []comparator     `json:"sort"`
	Position       int              `json:"position"`
	Anchor         *jmapID          `json:"anchor"`
	AnchorOffset   int              `json:"anchorOffset"`
	Limit          *int             `json:"limit"`
	CalculateTotal bool             `json:"calculateTotal"`
	SortAsTree     bool             `json:"sortAsTree"`
	FilterAsTree   bool             `json:"filterAsTree"`
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

// queryHandler implements Mailbox/query.
type queryHandler struct{ h *handlerSet }

func (q *queryHandler) Method() string { return "Mailbox/query" }

// Execute materializes the principal-visible mailbox set, filters and
// sorts in memory, then pages. The mailbox count per principal is
// bounded (~tens) so the in-memory pass fits.
func (q *queryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
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
	all, err := listAccessibleMailboxes(ctx, q.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	matched := make([]store.Mailbox, 0, len(all))
	for _, mb := range all {
		if matchMailboxFilter(mb, req.Filter) {
			matched = append(matched, mb)
		}
	}
	sortMailboxes(matched, req.Sort)

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
	for _, mb := range matched[start:end] {
		resp.IDs = append(resp.IDs, jmapIDFromMailbox(mb.ID))
	}
	return resp, nil
}

func matchMailboxFilter(mb store.Mailbox, f *filterCondition) bool {
	if f == nil {
		return true
	}
	// ParentID is json.RawMessage; three-way decode:
	//   absent (nil/empty): no filter
	//   JSON null: top-level mailboxes only (parentId == 0)
	//   JSON string: exact parent match
	if len(f.ParentID) > 0 {
		raw := strings.TrimSpace(string(f.ParentID))
		if raw == "null" {
			// Filter for top-level mailboxes only.
			if mb.ParentID != 0 {
				return false
			}
		} else {
			// Strip surrounding quotes and compare as a mailbox ID.
			var idStr string
			if err := json.Unmarshal(f.ParentID, &idStr); err != nil {
				// Unparseable parentId; skip the filter rather than crashing.
				return false
			}
			id, ok := mailboxIDFromJMAP(idStr)
			if !ok || mb.ParentID != id {
				return false
			}
		}
	}
	if f.Name != nil {
		if !strings.EqualFold(mb.Name, *f.Name) {
			return false
		}
	}
	if f.Role != nil {
		role := roleFromAttributes(mb.Attributes)
		want := *f.Role
		if want == "" {
			if role != nil {
				return false
			}
		} else {
			if role == nil || *role != want {
				return false
			}
		}
	}
	if f.HasAnyRole != nil {
		role := roleFromAttributes(mb.Attributes)
		if *f.HasAnyRole && role == nil {
			return false
		}
		if !*f.HasAnyRole && role != nil {
			return false
		}
	}
	if f.IsSubscribed != nil {
		isSub := mb.Attributes&store.MailboxAttrSubscribed != 0
		if isSub != *f.IsSubscribed {
			return false
		}
	}
	return true
}

func sortMailboxes(xs []store.Mailbox, comps []comparator) {
	if len(comps) == 0 {
		comps = []comparator{{Property: "name"}}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		for _, c := range comps {
			asc := true
			if c.IsAscending != nil {
				asc = *c.IsAscending
			}
			cmp := compareMailbox(xs[i], xs[j], c.Property)
			if cmp == 0 {
				continue
			}
			if asc {
				return cmp < 0
			}
			return cmp > 0
		}
		return xs[i].ID < xs[j].ID
	})
}

func compareMailbox(a, b store.Mailbox, property string) int {
	switch property {
	case "name", "sortOrder":
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	}
	return 0
}

// queryChangesRequest is the wire-form Mailbox/queryChanges request
// (RFC 8620 §5.6).
type queryChangesRequest struct {
	AccountID      jmapID           `json:"accountId"`
	Filter         *filterCondition `json:"filter"`
	Sort           []comparator     `json:"sort"`
	SinceQueryState string          `json:"sinceQueryState"`
	MaxChanges     *int             `json:"maxChanges"`
	UpToID         *jmapID          `json:"upToId"`
	CalculateTotal bool             `json:"calculateTotal"`
}

// queryChangesAddedItem is a (id, index) pair in the added list.
type queryChangesAddedItem struct {
	ID    jmapID `json:"id"`
	Index int    `json:"index"`
}

// queryChangesResponse is the wire-form Mailbox/queryChanges response.
type queryChangesResponse struct {
	AccountID       jmapID                  `json:"accountId"`
	OldQueryState   string                  `json:"oldQueryState"`
	NewQueryState   string                  `json:"newQueryState"`
	Total           *int                    `json:"total,omitempty"`
	Removed         []jmapID                `json:"removed"`
	Added           []queryChangesAddedItem `json:"added"`
}

// queryChangesHandler implements Mailbox/queryChanges (RFC 8620 §5.6).
// Because the mailbox set per principal is small (tens of rows), we
// replay the diff by:
//
//  1. Collecting created/updated/destroyed IDs from the change feed
//     since sinceQueryState.
//  2. Building the current query result set.
//  3. For each destroyed ID: add to removed.
//  4. For each created ID that passes the current filter: add to added
//     at its current index position.
//  5. For each updated ID: if it is now in the result set, treat it as
//     a re-add (remove + add) because its sort position may have
//     changed. If it is no longer in the set, treat it as removed.
//
// RFC 8620 §5.6 permits this "re-add" strategy for updated items whose
// filter membership before the update is unknown.
type queryChangesHandler struct{ h *handlerSet }

func (queryChangesHandler) Method() string { return "Mailbox/queryChanges" }

func (qc queryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
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

	since, ok := parseState(req.SinceQueryState)
	if !ok {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "unparseable sinceQueryState")
	}

	newSeq, err := qc.h.store.Meta().GetMaxChangeSeqForKind(ctx, pid, store.EntityKindMailbox)
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

	if since == newSeq {
		// No changes: return empty diff.
		if req.CalculateTotal {
			all, err := listAccessibleMailboxes(ctx, qc.h.store.Meta(), pid)
			if err != nil {
				return nil, serverFail(err)
			}
			var matched []store.Mailbox
			for _, mb := range all {
				if matchMailboxFilter(mb, req.Filter) {
					matched = append(matched, mb)
				}
			}
			t := len(matched)
			resp.Total = &t
		}
		return resp, nil
	}

	// Collect the IDs that changed since sinceQueryState by walking the
	// change feed from seq > since.
	const page = 1000
	var cursor store.ChangeSeq = since
	created := map[store.MailboxID]struct{}{}
	updated := map[store.MailboxID]struct{}{}
	destroyed := map[store.MailboxID]struct{}{}
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
			if entry.Kind != store.EntityKindMailbox {
				continue
			}
			id := store.MailboxID(entry.EntityID)
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

	// Build the current filtered, sorted result set.
	all, err := listAccessibleMailboxes(ctx, qc.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	matched := make([]store.Mailbox, 0, len(all))
	for _, mb := range all {
		if matchMailboxFilter(mb, req.Filter) {
			matched = append(matched, mb)
		}
	}
	sortMailboxes(matched, req.Sort)

	// Build an id→position map for the current result set.
	posMap := make(map[store.MailboxID]int, len(matched))
	for i, mb := range matched {
		posMap[mb.ID] = i
	}

	// Destroyed IDs: unconditionally removed from the query result.
	for id := range destroyed {
		resp.Removed = append(resp.Removed, jmapIDFromMailbox(id))
	}

	// Updated IDs: RFC 8620 §5.6 says if the server cannot determine
	// membership before the update, include the ID in removed (so the
	// client drops it) and, if it is currently in the result, in added
	// (so the client re-inserts it at its new position).
	for id := range updated {
		jid := jmapIDFromMailbox(id)
		resp.Removed = append(resp.Removed, jid)
		if pos, inResult := posMap[id]; inResult {
			resp.Added = append(resp.Added, queryChangesAddedItem{ID: jid, Index: pos})
		}
	}

	// Created IDs: add to result if they match the current filter.
	for id := range created {
		if pos, inResult := posMap[id]; inResult {
			jid := jmapIDFromMailbox(id)
			resp.Added = append(resp.Added, queryChangesAddedItem{ID: jid, Index: pos})
		}
	}

	if req.CalculateTotal {
		t := len(matched)
		resp.Total = &t
	}

	return resp, nil
}
