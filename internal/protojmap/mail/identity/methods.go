package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// getRequest is the inbound shape of Identity/get (RFC 8620 §5.1).
type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties []string  `json:"properties,omitempty"`
}

// getResponse is the response shape (RFC 8620 §5.1).
type getResponse struct {
	AccountID string         `json:"accountId"`
	State     string         `json:"state"`
	List      []jmapIdentity `json:"list"`
	NotFound  []jmapID       `json:"notFound"`
}

// changesRequest mirrors the RFC 8620 §5.2 envelope.
type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges,omitempty"`
}

// changesResponse is the RFC 8620 §5.2 response.
type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []jmapID `json:"created"`
	Updated        []jmapID `json:"updated"`
	Destroyed      []jmapID `json:"destroyed"`
}

// setRequest is the RFC 8620 §5.3 inbound envelope. Identity has no
// destroyable default; destroys for the "default" id are rejected with
// a SetError.
type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[string]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
}

// setResponse is the response envelope.
type setResponse struct {
	AccountID    string                   `json:"accountId"`
	OldState     string                   `json:"oldState,omitempty"`
	NewState     string                   `json:"newState"`
	Created      map[string]jmapIdentity  `json:"created,omitempty"`
	Updated      map[jmapID]*jmapIdentity `json:"updated,omitempty"`
	Destroyed    []jmapID                 `json:"destroyed,omitempty"`
	NotCreated   map[string]setError      `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]setError      `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]setError      `json:"notDestroyed,omitempty"`
}

// setError is the per-key error envelope (RFC 8620 §5.3).
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// handlerSet bundles the methods for one Identity capability.
type handlerSet struct {
	store    store.Store
	identity *Store
	domains  func(ctx context.Context) (map[string]struct{}, error)
}

// makeDomainsFn returns a closure that lists the locally-hosted domains.
func makeDomainsFn(st store.Store) func(ctx context.Context) (map[string]struct{}, error) {
	return func(ctx context.Context) (map[string]struct{}, error) {
		ds, err := st.Meta().ListLocalDomains(ctx)
		if err != nil {
			return nil, fmt.Errorf("identity: list local domains: %w", err)
		}
		out := make(map[string]struct{}, len(ds))
		for _, d := range ds {
			out[d.Name] = struct{}{}
		}
		return out, nil
	}
}

// stateString stringifies the per-principal Identity state counter to
// the JMAP wire form.
func stateString(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

// currentState returns the principal's current Identity state.
func (h *handlerSet) currentState(ctx context.Context, p store.Principal) (string, error) {
	st, err := h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return "", err
	}
	return stateString(st.Identity), nil
}

// accountIDForPrincipal returns the canonical wire-form accountId for p.
func accountIDForPrincipal(p store.Principal) string {
	return string(protojmap.AccountIDForPrincipal(p.ID))
}

// validateAccountID checks the inbound accountId against the
// authenticated principal.
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

// -- Identity/get -----------------------------------------------------

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "Identity/get" }

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
	state, err := g.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	all := g.h.identity.snapshot(ctx, p)
	resp := getResponse{
		AccountID: accountIDForPrincipal(p),
		State:     state,
		List:      []jmapIdentity{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		for _, rec := range all {
			resp.List = append(resp.List, rec.toJMAP())
		}
		return resp, nil
	}
	byID := make(map[uint64]identityRecord, len(all))
	for _, r := range all {
		byID[r.ID] = r
	}
	for _, id := range *req.IDs {
		v, ok := parseID(id)
		if !ok {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		rec, found := byID[v]
		if !found {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		resp.List = append(resp.List, rec.toJMAP())
	}
	return resp, nil
}

// -- Identity/changes -------------------------------------------------

type changesHandler struct{ h *handlerSet }

func (changesHandler) Method() string { return "Identity/changes" }

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
	now, err := c.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	if req.SinceState == now {
		return changesResponse{
			AccountID: accountIDForPrincipal(p),
			OldState:  req.SinceState,
			NewState:  now,
			Created:   []jmapID{},
			Updated:   []jmapID{},
			Destroyed: []jmapID{},
		}, nil
	}
	resp := changesResponse{
		AccountID: accountIDForPrincipal(p),
		OldState:  req.SinceState,
		NewState:  now,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	for _, rec := range c.h.identity.snapshot(ctx, p) {
		resp.Updated = append(resp.Updated, renderID(rec.ID))
	}
	return resp, nil
}

// -- Identity/set -----------------------------------------------------

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "Identity/set" }

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
	oldState, err := s.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}
	resp := setResponse{
		AccountID: accountIDForPrincipal(p),
		OldState:  oldState,
	}
	mutated := false
	// Process creates.
	for clientID, raw := range req.Create {
		var in struct {
			Name          string         `json:"name"`
			Email         string         `json:"email"`
			ReplyTo       []emailAddress `json:"replyTo,omitempty"`
			Bcc           []emailAddress `json:"bcc,omitempty"`
			TextSignature string         `json:"textSignature,omitempty"`
			HTMLSignature string         `json:"htmlSignature,omitempty"`
			Signature     *string        `json:"signature,omitempty"`
			// AvatarBlobId and XFaceEnabled are herold extensions (REQ-SET-03b).
			AvatarBlobId *string `json:"avatarBlobId,omitempty"`
			XFaceEnabled bool    `json:"xFaceEnabled,omitempty"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{Type: "invalidProperties", Description: err.Error()}
			continue
		}
		_, dom, ok := localPartAndDomain(in.Email)
		if !ok {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"email"},
				Description: "email must be a valid addr-spec",
			}
			continue
		}
		domains, derr := s.h.domains(ctx)
		if derr != nil {
			return nil, protojmap.NewMethodError("serverFail", derr.Error())
		}
		if _, hosted := domains[dom]; !hosted {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "forbiddenFrom",
				Description: fmt.Sprintf("domain %q is not hosted by this server", dom),
				Properties:  []string{"email"},
			}
			continue
		}
		// Validate and resolve avatarBlobId if supplied.
		var avatarHash string
		var avatarSize int64
		if in.AvatarBlobId != nil && *in.AvatarBlobId != "" {
			hash, sz, serr := validateAvatarBlob(ctx, s.h.store, *in.AvatarBlobId)
			if serr != nil {
				if resp.NotCreated == nil {
					resp.NotCreated = make(map[string]setError)
				}
				resp.NotCreated[clientID] = *serr
				continue
			}
			avatarHash = hash
			avatarSize = sz
		}
		rec := identityRecord{
			Name:           in.Name,
			Email:          in.Email,
			ReplyTo:        in.ReplyTo,
			Bcc:            in.Bcc,
			TextSignature:  in.TextSignature,
			HTMLSignature:  in.HTMLSignature,
			AvatarBlobHash: avatarHash,
			AvatarBlobSize: avatarSize,
			XFaceEnabled:   in.XFaceEnabled,
		}
		if in.Signature != nil {
			v := *in.Signature
			rec.Signature = &v
		}
		created := s.h.identity.create(ctx, p, rec)
		// incRef the avatar blob after the row is committed.
		if avatarHash != "" {
			_ = s.h.store.Meta().IncRefBlob(ctx, avatarHash, avatarSize)
		}
		if resp.Created == nil {
			resp.Created = make(map[string]jmapIdentity)
		}
		resp.Created[clientID] = created.toJMAP()
		mutated = true
	}
	// Process updates.
	for id, raw := range req.Update {
		v, ok := parseID(id)
		if !ok {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "notFound"}
			continue
		}
		patch, perr := decodePatch(ctx, s.h.store, raw)
		if perr != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = *perr
			continue
		}
		// Snapshot old avatar hash before applying so we can manage
		// refcounts after a successful update.
		oldAvatarHash := s.h.identity.snapshotAvatarHash(ctx, p, v)
		rec, ok := s.h.identity.update(ctx, p, v, patch)
		if !ok {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "notFound"}
			continue
		}
		// Manage refcounts: incRef new avatar first (never transiently
		// zero), then decRef old.
		if patch.hasAvatarBlobId {
			if rec.AvatarBlobHash != "" {
				_ = s.h.store.Meta().IncRefBlob(ctx, rec.AvatarBlobHash, rec.AvatarBlobSize)
			}
			if oldAvatarHash != "" {
				_ = s.h.store.Meta().DecRefBlob(ctx, oldAvatarHash)
			}
		}
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapIdentity)
		}
		j := rec.toJMAP()
		resp.Updated[id] = &j
		mutated = true
	}
	// Process destroys.
	for _, id := range req.Destroy {
		v, ok := parseID(id)
		if !ok {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = make(map[jmapID]setError)
			}
			resp.NotDestroyed[id] = setError{Type: "notFound"}
			continue
		}
		if v == 0 {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = make(map[jmapID]setError)
			}
			resp.NotDestroyed[id] = setError{
				Type: "forbidden", Description: "default identity is not deletable"}
			continue
		}
		// Snapshot the avatar hash before destroying so we can decRef.
		oldAvatarHash := s.h.identity.snapshotAvatarHash(ctx, p, v)
		if !s.h.identity.destroy(ctx, p, v) {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = make(map[jmapID]setError)
			}
			resp.NotDestroyed[id] = setError{Type: "notFound"}
			continue
		}
		if oldAvatarHash != "" {
			_ = s.h.store.Meta().DecRefBlob(ctx, oldAvatarHash)
		}
		resp.Destroyed = append(resp.Destroyed, id)
		mutated = true
	}
	// Bump JMAP state on any mutation.
	if mutated {
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, p.ID,
			store.JMAPStateKindIdentity); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	newState, err := s.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = newState
	return resp, nil
}

// decodePatch reads an Identity/set "update" object into the Store's
// patch shape, distinguishing missing fields from cleared ones.
// ctx and st are needed to validate avatarBlobId when present.
func decodePatch(ctx context.Context, st store.Store, raw json.RawMessage) (identityPatch, *setError) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return identityPatch{}, &setError{Type: "invalidProperties", Description: err.Error()}
	}
	var out identityPatch
	for k, v := range m {
		switch k {
		case "name":
			out.hasName = true
			if err := json.Unmarshal(v, &out.name); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("name: %v", err)}
			}
		case "replyTo":
			out.hasReplyTo = true
			if err := json.Unmarshal(v, &out.replyTo); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("replyTo: %v", err)}
			}
		case "bcc":
			out.hasBcc = true
			if err := json.Unmarshal(v, &out.bcc); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("bcc: %v", err)}
			}
		case "textSignature":
			out.hasTextSignature = true
			if err := json.Unmarshal(v, &out.textSignature); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("textSignature: %v", err)}
			}
		case "htmlSignature":
			out.hasHTMLSignature = true
			if err := json.Unmarshal(v, &out.htmlSignature); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("htmlSignature: %v", err)}
			}
		case "signature":
			out.hasSignature = true
			if string(v) == "null" {
				out.signature = nil
				continue
			}
			var sig string
			if err := json.Unmarshal(v, &sig); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("signature: %v", err)}
			}
			out.signature = &sig
		case "avatarBlobId":
			out.hasAvatarBlobId = true
			if string(v) == "null" {
				out.avatarBlobHash = ""
				out.avatarBlobSize = 0
				continue
			}
			var blobID string
			if err := json.Unmarshal(v, &blobID); err != nil {
				return identityPatch{}, &setError{
					Type:        "invalidProperties",
					Properties:  []string{"avatarBlobId"},
					Description: fmt.Sprintf("avatarBlobId: %v", err),
				}
			}
			if blobID == "" {
				return identityPatch{}, &setError{
					Type:        "invalidProperties",
					Properties:  []string{"avatarBlobId"},
					Description: "avatarBlobId must be a non-empty string or null",
				}
			}
			hash, sz, serr := validateAvatarBlob(ctx, st, blobID)
			if serr != nil {
				return identityPatch{}, serr
			}
			out.avatarBlobHash = hash
			out.avatarBlobSize = sz
		case "xFaceEnabled":
			out.hasXFaceEnabled = true
			if err := json.Unmarshal(v, &out.xFaceEnabled); err != nil {
				return identityPatch{}, &setError{
					Type:        "invalidProperties",
					Properties:  []string{"xFaceEnabled"},
					Description: fmt.Sprintf("xFaceEnabled: %v", err),
				}
			}
		case "email":
			return identityPatch{}, &setError{
				Type:        "invalidProperties",
				Description: "email is immutable",
				Properties:  []string{"email"},
			}
		case "id", "mayDelete":
			return identityPatch{}, &setError{
				Type:        "invalidProperties",
				Description: k + " is read-only",
				Properties:  []string{k},
			}
		default:
			return identityPatch{}, &setError{
				Type:        "invalidProperties",
				Description: fmt.Sprintf("unknown property %q", k),
				Properties:  []string{k},
			}
		}
	}
	return out, nil
}

// validateAvatarBlob checks that blobID exists in the blob store and
// that its detected content-type starts with "image/". Returns the hash
// and size on success, or a setError for invalidProperties.
func validateAvatarBlob(ctx context.Context, st store.Store, blobID string) (hash string, size int64, serr *setError) {
	rc, err := st.Blobs().Get(ctx, blobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), "not found") {
			return "", 0, &setError{
				Type:        "invalidProperties",
				Properties:  []string{"avatarBlobId"},
				Description: "avatarBlobId: blob not found",
			}
		}
		return "", 0, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"avatarBlobId"},
			Description: fmt.Sprintf("avatarBlobId: blob lookup failed: %v", err),
		}
	}
	defer rc.Close()
	var buf [512]byte
	n, _ := io.ReadFull(rc, buf[:])
	ct := http.DetectContentType(buf[:n])
	if !strings.HasPrefix(ct, "image/") {
		return "", 0, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"avatarBlobId"},
			Description: fmt.Sprintf("avatarBlobId: blob content-type %q is not an image", ct),
		}
	}
	rest, _ := io.Copy(io.Discard, rc)
	totalSize := int64(n) + rest
	return blobID, totalSize, nil
}
