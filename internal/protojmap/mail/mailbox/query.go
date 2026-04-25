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
type filterCondition struct {
	ParentID     *jmapID `json:"parentId"`
	Name         *string `json:"name"`
	Role         *string `json:"role"`
	HasAnyRole   *bool   `json:"hasAnyRole"`
	IsSubscribed *bool   `json:"isSubscribed"`
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
	for _, mb := range matched[start:end] {
		resp.IDs = append(resp.IDs, jmapIDFromMailbox(mb.ID))
	}
	return resp, nil
}

func matchMailboxFilter(mb store.Mailbox, f *filterCondition) bool {
	if f == nil {
		return true
	}
	if f.ParentID != nil {
		if *f.ParentID == "" {
			if mb.ParentID != 0 {
				return false
			}
		} else {
			id, ok := mailboxIDFromJMAP(*f.ParentID)
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

// queryChangesHandler — RFC 8621 §2.6 lists Mailbox/queryChanges as
// optional in spirit; we always return cannotCalculateChanges so
// clients fall back to a fresh Mailbox/query.
type queryChangesHandler struct{ h *handlerSet }

func (queryChangesHandler) Method() string { return "Mailbox/queryChanges" }

func (queryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"Mailbox/queryChanges is unsupported; clients re-issue Mailbox/query")
}
