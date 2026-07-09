package email

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// -- Email/setByQuery ----------------------------------------------------

// setByQueryHandler implements the vendor extension Email/setByQuery
// (issue #149/#161, REQ-PROTO-40..48, capability
// https://netzhansa.com/jmap/email-bulk-mutation). It resolves `filter`
// to a match estimate ONLY (an indexed SQL COUNT on the SQL-pushable
// fast path, or 0) and acks immediately with a jobId; the actual
// per-message patch application happens in the background
// (bulkjob_worker.go). This is the whole point: a synchronous
// Email/set over an unbounded id list already demonstrated it blows
// the 1s method deadline on a real ~666-message label
// (REQ-PERF-DEADLINE).
type setByQueryHandler struct{ h *handlerSet }

func (setByQueryHandler) Method() string { return "Email/setByQuery" }

func (s setByQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req setByQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if req.AccountID == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if req.AccountID != protojmap.AccountIDForPrincipal(pid) {
		// v1 scope: whole-mailbox bulk jobs run as the account owner
		// only; no cross-account/ACL-shared-mailbox bulk jobs.
		return nil, protojmap.NewMethodError("accountNotFound",
			"Email/setByQuery only operates on the caller's own account")
	}
	if len(req.Patch) == 0 || string(req.Patch) == "null" {
		return nil, protojmap.NewMethodError("invalidArguments", "patch is required")
	}
	var patchObj map[string]json.RawMessage
	if err := json.Unmarshal(req.Patch, &patchObj); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments",
			"patch must be a JSON object: "+err.Error())
	}
	filter, ferr := decodeFilter(req.Filter)
	if ferr != nil {
		return nil, protojmap.NewMethodError("invalidArguments", ferr.Error())
	}

	// REQ-PERF-DEADLINE: only an indexed SQL COUNT runs on the request
	// path — never a per-row walk of the match set. Filters the SQL
	// fast path cannot represent get estimate 0; the background worker
	// resolves the authoritative Total on its first tick regardless.
	estimate, _, err := fastMatchCount(ctx, s.h.store, pid, filter)
	if err != nil {
		return nil, serverFail(err)
	}

	filterJSON := "null"
	if req.Filter != nil {
		filterJSON = string(*req.Filter)
	}
	job, err := s.h.store.Meta().CreateEmailBulkJob(ctx, store.EmailBulkJobCreate{
		PrincipalID:     pid,
		FilterJSON:      filterJSON,
		PatchJSON:       string(req.Patch),
		MatchedEstimate: estimate,
	})
	if err != nil {
		return nil, serverFail(err)
	}
	return setByQueryResponse{
		AccountID:       req.AccountID,
		JobID:           jmapIDFromBulkJob(job.ID),
		MatchedEstimate: estimate,
	}, nil
}

// fastMatchCount returns an indexed SQL COUNT for filter when it is
// SQL-pushable (mergeFilterIntoOpts / tryFastEmailQuery's translation),
// with the same ACL access fence tryFastEmailQuery applies (an
// ACL-shared — not directly owned — mailbox falls back to
// pushable=false rather than under- or over-counting). Returns
// pushable=false, estimate=0 for any filter shape the fast path cannot
// represent (OR/NOT, text/body/hasAttachment predicates, thread-keyword
// aggregation); the caller treats that as "resolve in the background".
func fastMatchCount(
	ctx context.Context,
	st store.Store,
	pid store.PrincipalID,
	filter *emailFilter,
) (estimate int64, pushable bool, err error) {
	opts := store.EmailQueryFastOpts{}
	if !mergeFilterIntoOpts(filter, &opts) {
		return 0, false, nil
	}
	if opts.InMailbox != nil {
		owned, oerr := mailboxOwnedByPrincipal(ctx, st.Meta(), pid, *opts.InMailbox)
		if oerr != nil {
			return 0, false, oerr
		}
		if !owned {
			return 0, false, nil
		}
	}
	for _, id := range opts.InMailboxOtherThan {
		owned, oerr := mailboxOwnedByPrincipal(ctx, st.Meta(), pid, id)
		if oerr != nil {
			return 0, false, oerr
		}
		if !owned {
			return 0, false, nil
		}
	}
	opts.Limit = 1
	opts.CalculateTotal = true
	res, qerr := st.Meta().QueryEmailFast(ctx, pid, opts)
	if qerr != nil {
		return 0, false, qerr
	}
	return int64(res.Total), true, nil
}

// resolveAllMatchingIDs returns the full, deterministically ordered
// (ascending message id) set of messages filter matches for pid. Called
// only from the background worker — never on a JMAP request path — so
// walking the full match set here is fine; there is no response
// deadline to honour.
func resolveAllMatchingIDs(
	ctx context.Context,
	st store.Store,
	pid store.PrincipalID,
	filter *emailFilter,
) ([]store.MessageID, error) {
	const pageSize = 2000

	opts := store.EmailQueryFastOpts{}
	if mergeFilterIntoOpts(filter, &opts) {
		fastOK := true
		if opts.InMailbox != nil {
			owned, err := mailboxOwnedByPrincipal(ctx, st.Meta(), pid, *opts.InMailbox)
			if err != nil {
				return nil, err
			}
			fastOK = owned
		}
		if fastOK {
			for _, id := range opts.InMailboxOtherThan {
				owned, err := mailboxOwnedByPrincipal(ctx, st.Meta(), pid, id)
				if err != nil {
					return nil, err
				}
				if !owned {
					fastOK = false
					break
				}
			}
		}
		if fastOK {
			opts.SortBy = store.EmailQueryFastSortID
			opts.SortAscending = true
			opts.CalculateTotal = false
			var ids []store.MessageID
			pos := 0
			for {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				opts.Position = pos
				opts.Limit = pageSize
				res, err := st.Meta().QueryEmailFast(ctx, pid, opts)
				if err != nil {
					return nil, err
				}
				ids = append(ids, res.IDs...)
				if len(res.IDs) < pageSize {
					break
				}
				pos += pageSize
			}
			return ids, nil
		}
	}

	// Slow path: the same full-account-scan + Go-side filter Email/query
	// falls back to for filters the SQL layer cannot represent (OR/NOT,
	// text/body/hasAttachment, thread-keyword aggregation). No deadline
	// pressure here since this only runs off the request path.
	candidates, err := gatherCandidatesRaw(ctx, st, pid, pid, filter)
	if err != nil {
		return nil, err
	}
	matched := filterMessages(candidates, filter)
	ids := make([]store.MessageID, len(matched))
	for i, m := range matched {
		ids[i] = m.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// -- EmailBulkJob/get ------------------------------------------------

// bulkJobGetHandler implements EmailBulkJob/get.
type bulkJobGetHandler struct{ h *handlerSet }

func (bulkJobGetHandler) Method() string { return "EmailBulkJob/get" }

func (g bulkJobGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req bulkJobGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if req.AccountID == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if req.AccountID != protojmap.AccountIDForPrincipal(pid) {
		return nil, protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	if req.IDs == nil {
		return nil, protojmap.NewMethodError("invalidArguments",
			"ids is required: EmailBulkJob/get does not support listing every job")
	}
	state, err := bulkJobState(ctx, g.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := bulkJobGetResponse{
		AccountID: req.AccountID,
		State:     state,
		List:      []jmapBulkJob{},
		NotFound:  []jmapID{},
	}
	for _, raw := range *req.IDs {
		id, ok := bulkJobIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		job, jerr := g.h.store.Meta().GetEmailBulkJob(ctx, pid, id)
		if jerr != nil {
			if errors.Is(jerr, store.ErrNotFound) {
				resp.NotFound = append(resp.NotFound, raw)
				continue
			}
			return nil, serverFail(jerr)
		}
		resp.List = append(resp.List, renderBulkJob(job))
	}
	return resp, nil
}

// bulkJobState returns the JMAP state string for the EmailBulkJob type:
// the decimal of jmap_states.email_bulk_job_state, bumped once per
// batch by the background worker so EventSource push tells the client
// to re-fetch EmailBulkJob/get.
func bulkJobState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.EmailBulkJob, 10), nil
}
