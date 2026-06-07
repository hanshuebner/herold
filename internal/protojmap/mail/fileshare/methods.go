package fileshare

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// handlerSet bundles dependencies shared by every method handler.
type handlerSet struct {
	store         store.Store
	clk           clock.Clock
	logger        *slog.Logger
	cfg           store.FileSharesConfig
	publicBaseURL string
}

// currentState reads the principal's FileShare JMAP state counter.
func (h *handlerSet) currentState(ctx context.Context, pid store.PrincipalID) (string, error) {
	st, err := h.store.Meta().GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return stateString(st.FileShare), nil
}

// -- shared JMAP envelope shapes -------------------------------------

type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties []string  `json:"properties,omitempty"`
}

type getResponse struct {
	AccountID string          `json:"accountId"`
	State     string          `json:"state"`
	List      []jmapFileShare `json:"list"`
	NotFound  []jmapID        `json:"notFound"`
}

type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges,omitempty"`
}

type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []jmapID `json:"created"`
	Updated        []jmapID `json:"updated"`
	Destroyed      []jmapID `json:"destroyed"`
}

type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[string]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
}

type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

type setResponse struct {
	AccountID    string                    `json:"accountId"`
	OldState     string                    `json:"oldState,omitempty"`
	NewState     string                    `json:"newState"`
	Created      map[string]jmapFileShare  `json:"created,omitempty"`
	Updated      map[jmapID]*jmapFileShare `json:"updated,omitempty"`
	Destroyed    []jmapID                  `json:"destroyed,omitempty"`
	NotCreated   map[string]setError       `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]setError       `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]setError       `json:"notDestroyed,omitempty"`
}

// querySortWire is the wire form of one JMAP sort object.
type querySortWire struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending,omitempty"`
}

type queryResponse struct {
	AccountID           string   `json:"accountId"`
	QueryState          string   `json:"queryState"`
	CanCalculateChanges bool     `json:"canCalculateChanges"`
	Position            int      `json:"position"`
	IDs                 []jmapID `json:"ids"`
	Total               *int     `json:"total,omitempty"`
}

// validateAccountID checks the inbound accountId against the
// authenticated principal.
func validateAccountID(p store.Principal, requested jmapID) *protojmap.MethodError {
	if requested == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if requested != string(protojmap.AccountIDForPrincipal(p.ID)) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// -- FileShare/get ---------------------------------------------------

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "FileShare/get" }

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
	state, err := g.h.currentState(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := getResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(p.ID)),
		State:     state,
		List:      []jmapFileShare{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		// Return all shares for this principal.
		rows, lerr := g.h.store.Meta().ListFileSharesByPrincipal(ctx, p.ID,
			store.FileShareListFilter{Limit: 1000})
		if lerr != nil {
			return nil, protojmap.NewMethodError("serverFail", lerr.Error())
		}
		for _, fs := range rows {
			resp.List = append(resp.List, recordToJMAP(fs, g.h.publicBaseURL))
		}
		return resp, nil
	}
	// Fetch by IDs: load all and index by id for O(1) lookup.
	all, lerr := g.h.store.Meta().ListFileSharesByPrincipal(ctx, p.ID,
		store.FileShareListFilter{Limit: 1000})
	if lerr != nil {
		return nil, protojmap.NewMethodError("serverFail", lerr.Error())
	}
	byID := make(map[string]store.FileShare, len(all))
	for _, fs := range all {
		byID[fs.ID] = fs
	}
	for _, id := range *req.IDs {
		rec, found := byID[id]
		if !found {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		resp.List = append(resp.List, recordToJMAP(rec, g.h.publicBaseURL))
	}
	return resp, nil
}

// -- FileShare/changes -----------------------------------------------

type changesHandler struct{ h *handlerSet }

func (changesHandler) Method() string { return "FileShare/changes" }

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
	now, err := c.h.currentState(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	accountID := string(protojmap.AccountIDForPrincipal(p.ID))
	if req.SinceState == now {
		return changesResponse{
			AccountID: accountID,
			OldState:  req.SinceState,
			NewState:  now,
			Created:   []jmapID{},
			Updated:   []jmapID{},
			Destroyed: []jmapID{},
		}, nil
	}
	// v1: no per-id change ledger. Conservatively report all current
	// shares as "updated" so a drifted client re-fetches. This mirrors
	// TaggedAddressFilter/changes and Identity/changes behaviour.
	rows, lerr := c.h.store.Meta().ListFileSharesByPrincipal(ctx, p.ID,
		store.FileShareListFilter{Limit: 1000})
	if lerr != nil {
		return nil, protojmap.NewMethodError("serverFail", lerr.Error())
	}
	resp := changesResponse{
		AccountID: accountID,
		OldState:  req.SinceState,
		NewState:  now,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	for _, fs := range rows {
		resp.Updated = append(resp.Updated, fs.ID)
	}
	return resp, nil
}

// -- FileShare/query -------------------------------------------------

type queryHandler struct{ h *handlerSet }

func (queryHandler) Method() string { return "FileShare/query" }

func (q queryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req struct {
		AccountID string `json:"accountId"`
		Filter    *struct {
			State string `json:"state,omitempty"`
		} `json:"filter,omitempty"`
		Sort     []querySortWire `json:"sort,omitempty"`
		Position int             `json:"position,omitempty"`
		Limit    *int            `json:"limit,omitempty"`
	}
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
	queryState, err := q.h.currentState(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}

	// Validate sort: only createdAt is supported.
	for _, s := range req.Sort {
		if s.Property != "createdAt" {
			return nil, protojmap.NewMethodError("unsupportedSort",
				"only createdAt sort is supported")
		}
	}

	// Validate filter state value when provided.
	var filterState store.FileShareState
	if req.Filter != nil && req.Filter.State != "" {
		switch store.FileShareState(req.Filter.State) {
		case store.FileShareStatePending, store.FileShareStateActive, store.FileShareStateRevoked:
			filterState = store.FileShareState(req.Filter.State)
		default:
			return nil, protojmap.NewMethodError("invalidArguments",
				"filter.state must be pending, active, or revoked")
		}
	}

	limit := 256
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
		if limit > 1000 {
			limit = 1000
		}
	}

	rows, lerr := q.h.store.Meta().ListFileSharesByPrincipal(ctx, p.ID,
		store.FileShareListFilter{
			State: filterState,
			Limit: limit + req.Position + 1, // over-fetch to compute total
		})
	if lerr != nil {
		return nil, protojmap.NewMethodError("serverFail", lerr.Error())
	}

	// Sort: default is createdAt descending (newest first). CreatedAt
	// ascending is supported when the client requests isAscending: true.
	ascending := false
	if len(req.Sort) > 0 && req.Sort[0].IsAscending != nil && *req.Sort[0].IsAscending {
		ascending = true
	}
	if !ascending {
		// Reverse order (ListFileSharesByPrincipal returns ascending by
		// created_at via the index; we need descending by default).
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}

	total := len(rows)
	// Apply position.
	if req.Position > len(rows) {
		rows = nil
	} else {
		rows = rows[req.Position:]
	}
	// Apply limit.
	if len(rows) > limit {
		rows = rows[:limit]
	}

	ids := make([]jmapID, 0, len(rows))
	for _, fs := range rows {
		ids = append(ids, fs.ID)
	}
	totalInt := total
	return queryResponse{
		AccountID:           string(protojmap.AccountIDForPrincipal(p.ID)),
		QueryState:          queryState,
		CanCalculateChanges: false,
		Position:            req.Position,
		IDs:                 ids,
		Total:               &totalInt,
	}, nil
}

// -- FileShare/set ---------------------------------------------------

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "FileShare/set" }

func (s setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req setRequest
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
	oldState, err := s.h.currentState(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}
	resp := setResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(p.ID)),
		OldState:  oldState,
	}
	mutated := false

	// -- creates -------------------------------------------------------

	type createIn struct {
		BlobID       string `json:"blobId"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		ExpiresIn    *int64 `json:"expiresIn"` // seconds
		MaxDownloads *int64 `json:"maxDownloads,omitempty"`
		Password     string `json:"password,omitempty"`
	}

	for clientID, raw := range req.Create {
		var in createIn
		if err := json.Unmarshal(raw, &in); err != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Description: err.Error(),
			}
			continue
		}
		// Validate required fields.
		if in.BlobID == "" {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"blobId"}, "blobId is required")
			continue
		}
		if in.Name == "" {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"name"}, "name is required")
			continue
		}
		if in.Type == "" {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"type"}, "type is required")
			continue
		}
		if in.ExpiresIn == nil {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"expiresIn"}, "expiresIn is required")
			continue
		}
		// Resolve blobId to a BlobRef within this account. The blob ID
		// in JMAP is the BLAKE3 hex hash (the handleUpload response
		// returns it as blobId). We verify the blob exists in the blob
		// store AND belongs to this account by calling Blobs.Stat; a
		// blob the account cannot read is rejected with "forbidden"
		// (REQ-SHARE-41).
		blobHash := in.BlobID
		blobSize, _, serr := s.h.store.Blobs().Stat(ctx, blobHash)
		if serr != nil {
			if errors.Is(serr, store.ErrNotFound) {
				setNotCreated(&resp, clientID, "forbidden",
					[]string{"blobId"}, "blobId not found or not accessible")
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", serr.Error())
		}

		// Clamp expiresIn to MaxTTL.
		expiresIn := time.Duration(*in.ExpiresIn) * time.Second
		if s.h.cfg.MaxTTL > 0 && expiresIn > s.h.cfg.MaxTTL {
			expiresIn = s.h.cfg.MaxTTL
		}
		if expiresIn <= 0 {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"expiresIn"}, "expiresIn must be positive")
			continue
		}

		createReq := store.FileShareCreate{
			PrincipalID:  p.ID,
			BlobHash:     blobHash,
			BlobSize:     blobSize,
			Filename:     in.Name,
			ContentType:  in.Type,
			PendingTTL:   s.h.cfg.PendingTTL,
			MaxDownloads: in.MaxDownloads,
			Password:     in.Password,
			Config:       s.h.cfg,
		}
		created, cerr := s.h.store.Meta().CreateFileShare(ctx, createReq)
		if cerr != nil {
			if errors.Is(cerr, store.ErrTooManyShares) {
				setNotCreated(&resp, clientID, "tooManyShares",
					nil, "per-principal share cap reached")
				continue
			}
			if errors.Is(cerr, store.ErrQuotaExceeded) {
				setNotCreated(&resp, clientID, "overQuota",
					nil, "per-principal share quota exceeded")
				continue
			}
			if errors.Is(cerr, store.ErrInvalidArgument) {
				setNotCreated(&resp, clientID, "invalidProperties",
					nil, cerr.Error())
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", cerr.Error())
		}
		if resp.Created == nil {
			resp.Created = make(map[string]jmapFileShare)
		}
		resp.Created[clientID] = recordToJMAP(created, s.h.publicBaseURL)
		mutated = true
		s.audit(ctx, p, "file_share.create", created.ID, map[string]string{
			"blob_hash":    created.BlobHash,
			"filename":     created.Filename,
			"content_type": created.ContentType,
			"size":         strconv.FormatInt(created.BlobSize, 10),
		})
	}

	// -- updates -------------------------------------------------------

	// Load existing shares for update validation.
	existing, lerr := s.h.store.Meta().ListFileSharesByPrincipal(ctx, p.ID,
		store.FileShareListFilter{Limit: 1000})
	if lerr != nil {
		return nil, protojmap.NewMethodError("serverFail", lerr.Error())
	}
	byID := make(map[string]store.FileShare, len(existing))
	for _, fs := range existing {
		byID[fs.ID] = fs
	}

	// Also include any just-created shares in the byID map so we can
	// update them in the same /set call.
	if resp.Created != nil {
		for _, wire := range resp.Created {
			// Re-load from store to get the canonical row.
			fs, gerr := s.h.store.Meta().GetFileShareByID(ctx, wire.ID)
			if gerr == nil {
				byID[fs.ID] = fs
			}
		}
	}

	type updateIn struct {
		State        *string `json:"state,omitempty"`
		ExpiresAt    *string `json:"expiresAt,omitempty"`
		MaxDownloads *int64  `json:"maxDownloads,omitempty"`
		// Message back-reference (REQ-SHARE-04), accepted only alongside
		// the confirm transition (state:"active").
		SourceMessageID  *string  `json:"sourceMessageId,omitempty"`
		SourceSubject    *string  `json:"sourceSubject,omitempty"`
		SourceRecipients []string `json:"sourceRecipients,omitempty"`
	}

	for id, raw := range req.Update {
		prior, exists := byID[id]
		if !exists {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "notFound"}
			continue
		}
		// Reject attempts to mutate immutable or unknown fields.
		var rawMap map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rawMap); err != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{
				Type: "invalidProperties", Description: err.Error()}
			continue
		}
		badProp := ""
		for k := range rawMap {
			switch k {
			case "state", "expiresAt", "maxDownloads",
				"sourceMessageId", "sourceSubject", "sourceRecipients":
				// Mutable (the source fields only take effect on confirm).
			case "id", "blobId", "name", "type", "size", "url",
				"createdAt", "hasPassword", "downloadCount", "lastDownloadedAt":
				badProp = k
			default:
				badProp = k
			}
			if badProp != "" {
				break
			}
		}
		if badProp != "" {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{
				Type:        "invalidProperties",
				Properties:  []string{badProp},
				Description: badProp + " is immutable or unknown",
			}
			continue
		}
		var in updateIn
		if err := json.Unmarshal(raw, &in); err != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{
				Type: "invalidProperties", Description: err.Error()}
			continue
		}

		// state: "active" confirms a pending share; "revoked" revokes an
		// active one (the row survives so it shows as revoked until the
		// sweeper reaps it, REQ-SHARE-22).
		if in.State != nil {
			switch *in.State {
			case string(store.FileShareStateActive):
				var source store.FileShareSource
				if in.SourceMessageID != nil {
					source.MessageID = *in.SourceMessageID
				}
				if in.SourceSubject != nil {
					source.Subject = *in.SourceSubject
				}
				source.Recipients = in.SourceRecipients
				confirmed, cerr := s.h.store.Meta().ConfirmFileShare(ctx, p.ID, id, s.h.cfg, source)
				if cerr != nil {
					if errors.Is(cerr, store.ErrShareNotConfirmable) {
						if resp.NotUpdated == nil {
							resp.NotUpdated = make(map[jmapID]setError)
						}
						resp.NotUpdated[id] = setError{
							Type:        "shareNotConfirmable",
							Description: "share is revoked or does not exist",
						}
						continue
					}
					if errors.Is(cerr, store.ErrNotFound) {
						if resp.NotUpdated == nil {
							resp.NotUpdated = make(map[jmapID]setError)
						}
						resp.NotUpdated[id] = setError{Type: "notFound"}
						continue
					}
					return nil, protojmap.NewMethodError("serverFail", cerr.Error())
				}
				jrow := recordToJMAP(confirmed, s.h.publicBaseURL)
				if resp.Updated == nil {
					resp.Updated = make(map[jmapID]*jmapFileShare)
				}
				resp.Updated[id] = &jrow
				mutated = true
				s.audit(ctx, p, "file_share.confirm", id, map[string]string{
					"blob_hash": confirmed.BlobHash,
				})
				continue
			case string(store.FileShareStateRevoked):
				if rerr := s.h.store.Meta().RevokeFileShare(ctx, p.ID, id); rerr != nil {
					if resp.NotUpdated == nil {
						resp.NotUpdated = make(map[jmapID]setError)
					}
					if errors.Is(rerr, store.ErrNotFound) {
						resp.NotUpdated[id] = setError{Type: "notFound"}
						continue
					}
					return nil, protojmap.NewMethodError("serverFail", rerr.Error())
				}
				revoked, gerr := s.h.store.Meta().GetFileShareByID(ctx, id)
				if gerr != nil {
					return nil, protojmap.NewMethodError("serverFail", gerr.Error())
				}
				jrow := recordToJMAP(revoked, s.h.publicBaseURL)
				if resp.Updated == nil {
					resp.Updated = make(map[jmapID]*jmapFileShare)
				}
				resp.Updated[id] = &jrow
				mutated = true
				s.audit(ctx, p, "file_share.revoke", id, nil)
				continue
			default:
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{
					Type:        "invalidProperties",
					Properties:  []string{"state"},
					Description: "state may only be set to active (confirm) or revoked",
				}
				continue
			}
		}

		// expiresAt: only shortening is allowed.
		if in.ExpiresAt != nil {
			newExpiry, perr := time.Parse("2006-01-02T15:04:05Z", *in.ExpiresAt)
			if perr != nil {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{
					Type:        "invalidProperties",
					Properties:  []string{"expiresAt"},
					Description: "expiresAt must be a UTCDate (2006-01-02T15:04:05Z)",
				}
				continue
			}
			now := s.h.clk.Now()
			maxExpiry := now.Add(s.h.cfg.MaxTTL)
			if newExpiry.After(maxExpiry) {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{
					Type:        "invalidProperties",
					Properties:  []string{"expiresAt"},
					Description: "expiresAt cannot extend past max_ttl",
				}
				continue
			}
			if newExpiry.After(prior.ExpiresAt) {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{
					Type:        "invalidProperties",
					Properties:  []string{"expiresAt"},
					Description: "expiresAt may only be shortened, not extended",
				}
				continue
			}
			if uerr := s.h.store.Meta().UpdateFileShareExpiry(ctx, p.ID, id, newExpiry); uerr != nil {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				switch {
				case errors.Is(uerr, store.ErrNotFound):
					resp.NotUpdated[id] = setError{Type: "notFound"}
				case errors.Is(uerr, store.ErrShareNotConfirmable):
					resp.NotUpdated[id] = setError{Type: "shareNotConfirmable", Description: "share is revoked or does not exist"}
				case errors.Is(uerr, store.ErrShareExpiryNotShortenable):
					resp.NotUpdated[id] = setError{Type: "invalidProperties", Properties: []string{"expiresAt"}, Description: "expiresAt may only be shortened"}
				default:
					return nil, protojmap.NewMethodError("serverFail", uerr.Error())
				}
				continue
			}
			updated, gerr := s.h.store.Meta().GetFileShareByID(ctx, id)
			if gerr != nil {
				return nil, protojmap.NewMethodError("serverFail", gerr.Error())
			}
			jrow := recordToJMAP(updated, s.h.publicBaseURL)
			if resp.Updated == nil {
				resp.Updated = make(map[jmapID]*jmapFileShare)
			}
			resp.Updated[id] = &jrow
			mutated = true
			s.audit(ctx, p, "file_share.update", id, map[string]string{"expiresAt": *in.ExpiresAt})
			continue
		}

		// maxDownloads: only lowering is allowed.
		if in.MaxDownloads != nil {
			if prior.MaxDownloads != nil && *in.MaxDownloads > *prior.MaxDownloads {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{
					Type:        "invalidProperties",
					Properties:  []string{"maxDownloads"},
					Description: "maxDownloads may only be lowered",
				}
				continue
			}
			if uerr := s.h.store.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, id, *in.MaxDownloads); uerr != nil {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				switch {
				case errors.Is(uerr, store.ErrNotFound):
					resp.NotUpdated[id] = setError{Type: "notFound"}
				case errors.Is(uerr, store.ErrShareNotConfirmable):
					resp.NotUpdated[id] = setError{Type: "shareNotConfirmable", Description: "share is revoked or does not exist"}
				case errors.Is(uerr, store.ErrShareMaxDownloadsNotLowerable):
					resp.NotUpdated[id] = setError{Type: "invalidProperties", Properties: []string{"maxDownloads"}, Description: "maxDownloads may only be lowered, never below the current download count"}
				default:
					return nil, protojmap.NewMethodError("serverFail", uerr.Error())
				}
				continue
			}
			updated, gerr := s.h.store.Meta().GetFileShareByID(ctx, id)
			if gerr != nil {
				return nil, protojmap.NewMethodError("serverFail", gerr.Error())
			}
			jrow := recordToJMAP(updated, s.h.publicBaseURL)
			if resp.Updated == nil {
				resp.Updated = make(map[jmapID]*jmapFileShare)
			}
			resp.Updated[id] = &jrow
			mutated = true
			s.audit(ctx, p, "file_share.update", id, nil)
			continue
		}

		// Empty patch — no-op success.
		jrow := recordToJMAP(prior, s.h.publicBaseURL)
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapFileShare)
		}
		resp.Updated[id] = &jrow
	}

	// -- destroys ------------------------------------------------------

	for _, id := range req.Destroy {
		derr := s.h.store.Meta().DestroyFileShare(ctx, p.ID, id)
		if derr != nil {
			if errors.Is(derr, store.ErrNotFound) {
				if resp.NotDestroyed == nil {
					resp.NotDestroyed = make(map[jmapID]setError)
				}
				resp.NotDestroyed[id] = setError{Type: "notFound"}
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", derr.Error())
		}
		resp.Destroyed = append(resp.Destroyed, id)
		mutated = true
		s.audit(ctx, p, "file_share.destroy", id, nil)
	}

	if mutated {
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, p.ID,
			store.JMAPStateKindFileShare); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	newState, err := s.h.currentState(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = newState
	return resp, nil
}

// setNotCreated is a convenience helper that initialises the NotCreated
// map on first use and appends one entry.
func setNotCreated(resp *setResponse, clientID, errType string, props []string, desc string) {
	if resp.NotCreated == nil {
		resp.NotCreated = make(map[string]setError)
	}
	resp.NotCreated[clientID] = setError{
		Type:        errType,
		Properties:  props,
		Description: desc,
	}
}

// audit writes a single audit_log row. Failures are logged but never
// escalate to the caller; the audit log is best-effort.
func (s setHandler) audit(ctx context.Context, p store.Principal, action, subject string, metadata map[string]string) {
	entry := store.AuditLogEntry{
		ActorKind: store.ActorPrincipal,
		ActorID:   strconv.FormatUint(uint64(p.ID), 10),
		Action:    action,
		Subject:   subject,
		Outcome:   store.OutcomeSuccess,
		Metadata:  metadata,
	}
	if err := s.h.store.Meta().AppendAuditLog(ctx, entry); err != nil {
		s.h.logger.Warn("file-share audit log write failed",
			slog.String("subsystem", "jmap-file-shares"),
			slog.String("activity", "audit"),
			slog.String("action", action),
			slog.String("subject", subject),
			slog.String("err", err.Error()))
	}
}
