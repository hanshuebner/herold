package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// setRequest is the wire-form Email/set request (RFC 8620 §5.3).
type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// setResponse is the wire-form Email/set response.
type setResponse struct {
	AccountID    jmapID               `json:"accountId"`
	OldState     string               `json:"oldState"`
	NewState     string               `json:"newState"`
	Created      map[string]jmapEmail `json:"created"`
	Updated      map[jmapID]any       `json:"updated"`
	Destroyed    []jmapID             `json:"destroyed"`
	NotCreated   map[string]setError  `json:"notCreated"`
	NotUpdated   map[jmapID]setError  `json:"notUpdated"`
	NotDestroyed map[jmapID]setError  `json:"notDestroyed"`
}

// emailCreateInput is the per-create payload schema. RFC 8621 §4.6
// permits a wide set of properties on create (any of the canonical
// Email properties); v1 honours the operator-relevant subset and
// returns "invalidProperties" for unrecognised keys at strict-decode
// time.
type emailCreateInput struct {
	BlobID     string          `json:"blobId"`
	MailboxIDs map[jmapID]bool `json:"mailboxIds"`
	Keywords   map[string]bool `json:"keywords"`
	ReceivedAt *string         `json:"receivedAt"`
	Subject    *string         `json:"subject"`
}

// setHandler implements Email/set.
type setHandler struct{ h *handlerSet }

func (s *setHandler) Method() string { return "Email/set" }

func (s *setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}

	var req setRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentState(ctx, s.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch",
			"ifInState does not match current state")
	}
	resp := setResponse{
		AccountID:    req.AccountID,
		OldState:     state,
		NewState:     state,
		Created:      map[string]jmapEmail{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	creationRefs := make(map[string]store.MessageID, len(req.Create))

	for key, raw := range req.Create {
		var in emailCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		mid, jm, serr, err := s.h.createEmail(ctx, pid, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = mid
		resp.Created[key] = jm
	}

	for raw, payload := range req.Update {
		mid, ok := emailIDFromJMAP(raw)
		if !ok {
			if rid, refOK := creationRefs[strings.TrimPrefix(raw, "#")]; refOK && strings.HasPrefix(raw, "#") {
				mid = rid
			} else {
				resp.NotUpdated[raw] = setError{Type: "notFound"}
				continue
			}
		}
		serr, err := s.h.updateEmail(ctx, pid, mid, payload)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotUpdated[raw] = *serr
			continue
		}
		resp.Updated[raw] = nil
	}

	for _, raw := range req.Destroy {
		mid, ok := emailIDFromJMAP(raw)
		if !ok {
			if rid, refOK := creationRefs[strings.TrimPrefix(raw, "#")]; refOK && strings.HasPrefix(raw, "#") {
				mid = rid
			} else {
				resp.NotDestroyed[raw] = setError{Type: "notFound"}
				continue
			}
		}
		serr, err := s.h.destroyEmail(ctx, pid, mid)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentState(ctx, s.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

// createEmail handles one Email/set create entry. The body is fetched
// from the blob store (a prior /jmap/upload landed it) and inserted
// into the named mailbox. v1 supports exactly one mailboxId per
// create; multi-mailbox creation requires the per-mailbox row fanout
// the parallel agent's storage extension lands later.
func (h *handlerSet) createEmail(
	ctx context.Context,
	pid store.PrincipalID,
	in emailCreateInput,
) (store.MessageID, jmapEmail, *setError, error) {
	if in.BlobID == "" {
		return 0, jmapEmail{}, &setError{
			Type: "invalidProperties", Properties: []string{"blobId"},
			Description: "blobId is required",
		}, nil
	}
	if len(in.MailboxIDs) == 0 {
		return 0, jmapEmail{}, &setError{
			Type: "invalidProperties", Properties: []string{"mailboxIds"},
			Description: "mailboxIds must list at least one mailbox",
		}, nil
	}
	if len(in.MailboxIDs) > 1 {
		return 0, jmapEmail{}, &setError{
			Type: "invalidProperties", Properties: []string{"mailboxIds"},
			Description: "v1 supports a single mailbox per Email; multi-mailbox lands with the storage extension",
		}, nil
	}
	var mailboxID store.MailboxID
	for raw, present := range in.MailboxIDs {
		if !present {
			continue
		}
		id, ok := mailboxIDFromJMAP(raw)
		if !ok {
			return 0, jmapEmail{}, &setError{
				Type: "invalidProperties", Properties: []string{"mailboxIds"},
				Description: "mailboxIds carries an unparseable id",
			}, nil
		}
		mailboxID = id
	}
	mb, err := h.store.Meta().GetMailboxByID(ctx, mailboxID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, jmapEmail{}, &setError{
				Type: "invalidProperties", Properties: []string{"mailboxIds"},
				Description: "mailbox does not exist",
			}, nil
		}
		return 0, jmapEmail{}, nil, err
	}
	if mb.PrincipalID != pid {
		return 0, jmapEmail{}, &setError{
			Type:        "forbidden",
			Description: "v1 does not allow inserting into a non-owned mailbox via Email/set",
		}, nil
	}

	// Read the blob so we can re-canonicalise it through Blobs.Put
	// (which CRLF-normalises and computes the canonical hash). The
	// uploaded blobId may already be canonical, in which case Put is
	// a no-op idempotent re-write; we still call it so the Message's
	// Blob.Hash matches the canonical form deterministically.
	rc, err := h.store.Blobs().Get(ctx, in.BlobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, jmapEmail{}, &setError{
				Type: "invalidProperties", Properties: []string{"blobId"},
				Description: "blob not found",
			}, nil
		}
		return 0, jmapEmail{}, nil, err
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return 0, jmapEmail{}, nil, fmt.Errorf("email: read blob: %w", err)
	}
	ref, err := h.store.Blobs().Put(ctx, bytes.NewReader(body))
	if err != nil {
		return 0, jmapEmail{}, nil, fmt.Errorf("email: re-put blob: %w", err)
	}

	// Parse just enough to populate the envelope cache.
	parser := h.parseFn
	if parser == nil {
		parser = defaultParseFn
	}
	parsed, _ := parser(bytes.NewReader(body))
	env := buildEnvelopeFromParsed(parsed)

	receivedAt := h.clk.Now()
	if in.ReceivedAt != nil {
		if t, perr := time.Parse(time.RFC3339, *in.ReceivedAt); perr == nil {
			receivedAt = t
		}
	}

	flags, customKW := flagsAndKeywordsFromJMAP(in.Keywords)
	msg := store.Message{
		MailboxID:    mailboxID,
		Flags:        flags,
		Keywords:     customKW,
		InternalDate: receivedAt,
		ReceivedAt:   receivedAt,
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     env,
	}
	uid, modseq, err := h.store.Meta().InsertMessage(ctx, msg)
	if err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			return 0, jmapEmail{}, &setError{
				Type: "overQuota", Description: "principal is over quota",
			}, nil
		}
		return 0, jmapEmail{}, nil, fmt.Errorf("email: insert: %w", err)
	}
	msg.UID = uid
	msg.ModSeq = modseq
	// InsertMessage does not return the assigned MessageID. We resolve
	// it from the change feed: the most recent EntityKindEmail /
	// ChangeOpCreated entry for this principal carries it.
	mid, err := mostRecentEmailCreatedID(ctx, h.store.Meta(), pid)
	if err != nil {
		return 0, jmapEmail{}, nil, fmt.Errorf("email: resolve id: %w", err)
	}
	msg.ID = mid

	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmail); err != nil {
		return 0, jmapEmail{}, nil, fmt.Errorf("email: bump state: %w", err)
	}
	return mid, renderEmailMetadata(msg), nil, nil
}

// updateEmail applies a flags / keywords / mailboxIds delta. v1
// supports keyword and flag mutation through UpdateMessageFlags;
// mailboxIds change requires re-insert + expunge which the simpler
// store interface does not yet expose, so we reject it.
func (h *handlerSet) updateEmail(
	ctx context.Context,
	pid store.PrincipalID,
	id store.MessageID,
	raw json.RawMessage,
) (*setError, error) {
	m, err := loadMessageForPrincipal(ctx, h.store.Meta(), pid, id)
	if err != nil {
		if errors.Is(err, errMessageMissing) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}

	// Decode a generic object so we can find both the structural
	// fields (mailboxIds, keywords) and the patch-style keys
	// (keywords/$seen).
	var obj map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
	}

	addFlags, clearFlags, addKW, clearKW, serr := decodeUpdate(obj, m)
	if serr != nil {
		return serr, nil
	}

	if newMailboxes, ok := obj["mailboxIds"]; ok {
		// Reject mailbox moves in v1 — see comment above.
		_ = newMailboxes
		return &setError{
			Type:        "invalidProperties",
			Properties:  []string{"mailboxIds"},
			Description: "moving messages between mailboxes via Email/set is not supported in v1",
		}, nil
	}

	if addFlags == 0 && clearFlags == 0 && len(addKW) == 0 && len(clearKW) == 0 {
		// No-op update — RFC 8620 §5.3 says "succeeds with empty
		// change" so we report Updated=null.
		if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmail); err != nil {
			return nil, fmt.Errorf("email: bump state: %w", err)
		}
		return nil, nil
	}

	if _, err := h.store.Meta().UpdateMessageFlags(ctx, m.ID, addFlags, clearFlags, addKW, clearKW, 0); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("email: update flags: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmail); err != nil {
		return nil, fmt.Errorf("email: bump state: %w", err)
	}
	return nil, nil
}

// decodeUpdate translates the RFC 8620 §5.3 patch / structural
// payload into add / clear flag and keyword deltas relative to the
// existing message m. RFC 8621 §4.6 lists "keywords" and the
// patch-syntax "keywords/<token>" as the supported axes.
func decodeUpdate(
	obj map[string]json.RawMessage,
	m store.Message,
) (
	addFlags, clearFlags store.MessageFlags,
	addKW, clearKW []string,
	serr *setError,
) {
	// Structural keywords map: replace whole-cloth.
	if rawKW, ok := obj["keywords"]; ok {
		var kws map[string]bool
		if err := json.Unmarshal(rawKW, &kws); err != nil {
			return 0, 0, nil, nil, &setError{
				Type: "invalidProperties", Properties: []string{"keywords"},
				Description: err.Error(),
			}
		}
		newFlags, newCustom := flagsAndKeywordsFromJMAP(kws)
		oldFlags := m.Flags &^ store.MessageFlagRecent // \Recent is not a keyword
		// add = newFlags &^ oldFlags ; clear = oldFlags &^ newFlags
		addFlags = newFlags &^ oldFlags
		clearFlags = oldFlags &^ newFlags
		// Keyword set diff.
		oldSet := map[string]struct{}{}
		for _, k := range m.Keywords {
			oldSet[strings.ToLower(k)] = struct{}{}
		}
		newSet := map[string]struct{}{}
		for _, k := range newCustom {
			newSet[strings.ToLower(k)] = struct{}{}
		}
		for k := range newSet {
			if _, has := oldSet[k]; !has {
				addKW = append(addKW, k)
			}
		}
		for k := range oldSet {
			if _, has := newSet[k]; !has {
				clearKW = append(clearKW, k)
			}
		}
	}
	// Patch-syntax: keywords/<token>: bool (true=add, false=clear).
	for k, v := range obj {
		const prefix = "keywords/"
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(k, prefix))
		if name == "" {
			continue
		}
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return 0, 0, nil, nil, &setError{
				Type: "invalidProperties", Properties: []string{k},
				Description: err.Error(),
			}
		}
		applyPatch(name, b, &addFlags, &clearFlags, &addKW, &clearKW)
	}
	return addFlags, clearFlags, addKW, clearKW, nil
}

func applyPatch(
	name string,
	add bool,
	addFlags, clearFlags *store.MessageFlags,
	addKW, clearKW *[]string,
) {
	switch name {
	case "$seen":
		if add {
			*addFlags |= store.MessageFlagSeen
		} else {
			*clearFlags |= store.MessageFlagSeen
		}
	case "$answered":
		if add {
			*addFlags |= store.MessageFlagAnswered
		} else {
			*clearFlags |= store.MessageFlagAnswered
		}
	case "$flagged":
		if add {
			*addFlags |= store.MessageFlagFlagged
		} else {
			*clearFlags |= store.MessageFlagFlagged
		}
	case "$draft":
		if add {
			*addFlags |= store.MessageFlagDraft
		} else {
			*clearFlags |= store.MessageFlagDraft
		}
	default:
		if add {
			*addKW = append(*addKW, name)
		} else {
			*clearKW = append(*clearKW, name)
		}
	}
}

// destroyEmail expunges m from its mailbox and bumps the Email state.
func (h *handlerSet) destroyEmail(
	ctx context.Context,
	pid store.PrincipalID,
	id store.MessageID,
) (*setError, error) {
	m, err := loadMessageForPrincipal(ctx, h.store.Meta(), pid, id)
	if err != nil {
		if errors.Is(err, errMessageMissing) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	if err := h.store.Meta().ExpungeMessages(ctx, m.MailboxID, []store.MessageID{m.ID}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("email: expunge: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmail); err != nil {
		return nil, fmt.Errorf("email: bump state: %w", err)
	}
	return nil, nil
}

// mostRecentEmailCreatedID walks the principal's change feed
// backwards and returns the EntityID of the most recent
// EntityKindEmail / ChangeOpCreated entry. Used to recover the
// MessageID assigned by InsertMessage (whose return shape is
// (UID, ModSeq) only). The store guarantees the change-feed entry
// is appended atomically with the insert so this read is safe to
// perform immediately afterwards.
func mostRecentEmailCreatedID(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) (store.MessageID, error) {
	const page = 1000
	var cursor store.ChangeSeq
	var last store.MessageID
	for {
		batch, err := meta.ReadChangeFeed(ctx, pid, cursor, page)
		if err != nil {
			return 0, err
		}
		for _, e := range batch {
			cursor = e.Seq
			if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
				last = store.MessageID(e.EntityID)
			}
		}
		if len(batch) < page {
			break
		}
	}
	if last == 0 {
		return 0, fmt.Errorf("email: no email-created entry in feed")
	}
	return last, nil
}
