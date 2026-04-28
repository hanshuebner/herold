package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/mailreact"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// emailAddress is the JMAP EmailAddress object used in the create-from-
// properties path (RFC 8621 §4.1.2.3). Mirrors jmapAddress but is used
// in struct fields that are decoded from the create payload; the
// canonical jmapAddress is used only for the response wire form.
type emailAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// emailBodyValue is the JMAP EmailBodyValue (RFC 8621 §4.1.4) in the
// create-from-properties path.
type emailBodyValue struct {
	Value             string `json:"value"`
	IsEncodingProblem bool   `json:"isEncodingProblem"`
	IsTruncated       bool   `json:"isTruncated"`
}

// emailBodyPart is a minimal EmailBodyPart as accepted on create — we
// only need the partId to look up the value in bodyValues.
type emailBodyPart struct {
	PartID string `json:"partId"`
}

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
//
// Two distinct creation paths are supported:
//
//  1. Blob path: BlobID is set. The message body is fetched from the
//     blob store as an already-formed RFC 5322 message (the upload-then-
//     reference flow).
//
//  2. Inline path (RFC 8621 §4.6 "construct from properties"): BlobID
//     is absent; BodyValues plus at least one of TextBody/HtmlBody must
//     be present. The handler assembles an RFC 5322 message server-side
//     and stores the result in the blob store before continuing.
type emailCreateInput struct {
	// Blob path.
	BlobID string `json:"blobId"`

	// Common fields.
	MailboxIDs   map[jmapID]bool `json:"mailboxIds"`
	Keywords     map[string]bool `json:"keywords"`
	ReceivedAt   *string         `json:"receivedAt"`
	Subject      *string         `json:"subject"`
	SnoozedUntil *string         `json:"snoozedUntil"`

	// Inline-body path (RFC 8621 §4.6 "construct from properties").
	From       []emailAddress            `json:"from"`
	To         []emailAddress            `json:"to"`
	Cc         []emailAddress            `json:"cc"`
	Bcc        []emailAddress            `json:"bcc"`
	ReplyTo    []emailAddress            `json:"replyTo"`
	InReplyTo  []string                  `json:"inReplyTo"`
	References []string                  `json:"references"`
	MessageID  []string                  `json:"messageId"`
	SentAt     *string                   `json:"sentAt"`
	BodyValues map[string]emailBodyValue `json:"bodyValues"`
	TextBody   []emailBodyPart           `json:"textBody"`
	HtmlBody   []emailBodyPart           `json:"htmlBody"`
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

// createEmail handles one Email/set create entry. Two paths are
// supported:
//
//  1. Blob path: in.BlobID is non-empty. The message body is fetched
//     directly from the blob store (the client called /jmap/upload
//     first, then referenced the resulting blobId).
//
//  2. Inline path (RFC 8621 §4.6): in.BlobID is empty AND in.BodyValues
//     plus at least one of in.TextBody/in.HtmlBody is present. The
//     handler assembles an RFC 5322 message server-side and stores it
//     before continuing down the shared insertion path.
//
// v1 supports exactly one mailboxId per create; multi-mailbox creation
// requires the per-mailbox row fanout the parallel agent's storage
// extension lands later.
func (h *handlerSet) createEmail(
	ctx context.Context,
	pid store.PrincipalID,
	in emailCreateInput,
) (store.MessageID, jmapEmail, *setError, error) {
	hasBlob := in.BlobID != ""
	hasBodyValues := len(in.BodyValues) > 0 && (len(in.TextBody) > 0 || len(in.HtmlBody) > 0)

	if hasBlob && hasBodyValues {
		// Strict: reject ambiguous inputs. Callers must pick one path.
		// This mirrors the intent of RFC 8621 §4.6: blobId and the
		// inline properties are mutually exclusive on create.
		return 0, jmapEmail{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"blobId", "bodyValues"},
			Description: "cannot set both blobId and bodyValues; provide exactly one",
		}, nil
	}

	if !hasBlob && !hasBodyValues {
		return 0, jmapEmail{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"blobId"},
			Description: "blobId is required, or provide bodyValues + textBody/htmlBody",
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

	// Obtain the raw RFC 5322 message bytes. For the blob path we read
	// from the blob store; for the inline path we assemble the message
	// from the supplied header and body properties (RFC 8621 §4.6).
	var body []byte
	if hasBlob {
		// Blob path: read and re-canonicalise through Blobs.Put (which
		// CRLF-normalises and computes the canonical hash). The
		// uploaded blobId may already be canonical; Put is idempotent.
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
		body, err = io.ReadAll(io.LimitReader(rc, 64<<20))
		if err != nil {
			return 0, jmapEmail{}, nil, fmt.Errorf("email: read blob: %w", err)
		}
	} else {
		// Inline path: assemble an RFC 5322 message from the create
		// properties supplied by the client. The resulting bytes are
		// canonicalised through Blobs.Put below, just like the blob
		// path; from that point the two paths are identical.
		var buildErr error
		body, buildErr = buildEmailFromProperties(in, h.clk.Now(), "")
		if buildErr != nil {
			return 0, jmapEmail{}, nil, fmt.Errorf("email: build from properties: %w", buildErr)
		}
	}
	ref, err := h.store.Blobs().Put(ctx, bytes.NewReader(body))
	if err != nil {
		return 0, jmapEmail{}, nil, fmt.Errorf("email: put blob: %w", err)
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

	// Snooze atomicity (REQ-PROTO-49): on create the same invariant
	// holds. snoozedUntil non-null implies $snoozed; snoozedUntil
	// nil/absent forbids the keyword.
	var snoozedUntil *time.Time
	if in.SnoozedUntil != nil {
		t, perr := time.Parse(time.RFC3339, *in.SnoozedUntil)
		if perr != nil {
			return 0, jmapEmail{}, &setError{
				Type:        "invalidProperties",
				Properties:  []string{"snoozedUntil"},
				Description: "snoozedUntil: " + perr.Error(),
			}, nil
		}
		tu := t.UTC()
		snoozedUntil = &tu
	}
	hasSnoozeKW := false
	for i, k := range customKW {
		if k == "$snoozed" {
			hasSnoozeKW = true
			if snoozedUntil == nil {
				return 0, jmapEmail{}, &setError{
					Type:        "invalidProperties",
					Properties:  []string{"keywords/$snoozed"},
					Description: "$snoozed keyword requires a snoozedUntil value",
				}, nil
			}
			_ = i
		}
	}
	if snoozedUntil != nil && !hasSnoozeKW {
		customKW = append(customKW, "$snoozed")
	}

	msg := store.Message{
		MailboxID:    mailboxID,
		Flags:        flags,
		Keywords:     customKW,
		InternalDate: receivedAt,
		ReceivedAt:   receivedAt,
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     env,
		SnoozedUntil: snoozedUntil,
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
//
// Snooze (REQ-PROTO-49): the snoozedUntil property and the
// "$snoozed" keyword form an atomic pair. The handler enforces:
//   - patch sets snoozedUntil to a non-null value: server adds
//     "$snoozed" (if not present in the patch)
//   - patch sets snoozedUntil to null: server clears "$snoozed"
//   - patch sets keywords/$snoozed=true without a matching
//     snoozedUntil → invalidProperties
//   - patch clears keywords/$snoozed while snoozedUntil is non-null
//     → server also clears snoozedUntil
//
// SetSnooze runs before the residual flag-only update so the two
// pieces commit through one atomic store call each, never leaving a
// half-applied state visible to a concurrent JMAP/IMAP reader.
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

	if newMailboxes, ok := obj["mailboxIds"]; ok {
		// Reject mailbox moves in v1 — see comment above.
		_ = newMailboxes
		return &setError{
			Type:        "invalidProperties",
			Properties:  []string{"mailboxIds"},
			Description: "moving messages between mailboxes via Email/set is not supported in v1",
		}, nil
	}

	// REQ-PROTO-101: handle reactions/<emoji>/<principalId> patch keys.
	reactionAdds, reactionRemoves, serr := decodeReactionPatches(obj, pid)
	if serr != nil {
		return serr, nil
	}
	if len(reactionAdds) > 0 || len(reactionRemoves) > 0 {
		if serr, err := h.applyReactionPatches(ctx, pid, m, reactionAdds, reactionRemoves); err != nil {
			return nil, fmt.Errorf("email: reaction patch: %w", err)
		} else if serr != nil {
			return serr, nil
		}
		if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmail); err != nil {
			return nil, fmt.Errorf("email: bump state after reaction: %w", err)
		}
		// Only reaction keys in the patch — nothing more to do.
		if !hasNonReactionKeys(obj) {
			return nil, nil
		}
	}

	snoozeAct, serr := decodeSnoozeIntent(obj, m)
	if serr != nil {
		return serr, nil
	}

	addFlags, clearFlags, addKW, clearKW, serr := decodeUpdate(obj, m)
	if serr != nil {
		return serr, nil
	}

	// Snooze enforcement: $snoozed in keywords-add without a date is
	// invalid; we reject early so the store never sees the keyword
	// without the matching column. addKW already lower-cased the
	// keyword.
	for _, k := range addKW {
		if k == "$snoozed" && snoozeAct.kind != snoozeSet {
			return &setError{
				Type:        "invalidProperties",
				Properties:  []string{"keywords/$snoozed"},
				Description: "$snoozed keyword requires a snoozedUntil value; pass both in the same patch",
			}, nil
		}
	}
	// If patch clears $snoozed and snoozedUntil isn't already in the
	// patch, force a clear so the column tracks the keyword.
	if snoozeAct.kind == snoozeUnchanged {
		for _, k := range clearKW {
			if k == "$snoozed" && m.SnoozedUntil != nil {
				snoozeAct = snoozeAction{kind: snoozeClear}
				break
			}
		}
	}
	// Drop the $snoozed keyword toggle from the residual delta — the
	// SetSnooze call below owns it. addKW filtering: leave the
	// keyword in the patch when SetSnooze is going to set it (the
	// store dedupes), but for clearKW we drop it because SetSnooze
	// nil already removes it.
	addKW = removeString(addKW, "$snoozed")
	clearKW = removeString(clearKW, "$snoozed")

	switch snoozeAct.kind {
	case snoozeSet:
		if _, err := h.store.Meta().SetSnooze(ctx, m.ID, &snoozeAct.when); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &setError{Type: "notFound"}, nil
			}
			return nil, fmt.Errorf("email: set snooze: %w", err)
		}
	case snoozeClear:
		if _, err := h.store.Meta().SetSnooze(ctx, m.ID, nil); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &setError{Type: "notFound"}, nil
			}
			return nil, fmt.Errorf("email: clear snooze: %w", err)
		}
	}

	if addFlags == 0 && clearFlags == 0 && len(addKW) == 0 && len(clearKW) == 0 {
		if snoozeAct.kind == snoozeUnchanged {
			// Truly empty patch — RFC 8620 §5.3 "succeeds with empty
			// change" so we report Updated=null.
			if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmail); err != nil {
				return nil, fmt.Errorf("email: bump state: %w", err)
			}
			return nil, nil
		}
		// Snooze action already updated the message; bump state and
		// return.
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

// snoozeAction is the resolved snooze intent of a patch. snoozeSet
// carries the wake-up deadline; snoozeClear has no payload;
// snoozeUnchanged means the patch did not name snoozedUntil at all.
type snoozeAction struct {
	kind snoozeKind
	when time.Time
}

type snoozeKind uint8

const (
	snoozeUnchanged snoozeKind = iota
	snoozeSet
	snoozeClear
)

// decodeSnoozeIntent inspects the raw patch object for snoozedUntil.
// Returns a setError when the supplied value is a malformed date.
func decodeSnoozeIntent(obj map[string]json.RawMessage, _ store.Message) (snoozeAction, *setError) {
	raw, ok := obj["snoozedUntil"]
	if !ok {
		return snoozeAction{kind: snoozeUnchanged}, nil
	}
	// Try null first; json.RawMessage trims whitespace on
	// Unmarshal, so a literal "null" matches verbatim.
	if string(raw) == "null" {
		return snoozeAction{kind: snoozeClear}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return snoozeAction{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"snoozedUntil"},
			Description: "snoozedUntil must be a UTC ISO-8601 date string or null",
		}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return snoozeAction{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"snoozedUntil"},
			Description: "snoozedUntil: " + err.Error(),
		}
	}
	return snoozeAction{kind: snoozeSet, when: t.UTC()}, nil
}

// removeString returns s with every occurrence of v removed. Used to
// strip the snooze-owned "$snoozed" token from the residual flag
// patch so SetSnooze can carry it instead.
func removeString(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
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

// reactionPatch is a decoded reactions/<emoji>/<principalId> patch.
type reactionPatch struct {
	emoji       string
	principalID store.PrincipalID
}

// decodeReactionPatches scans obj for "reactions/<emoji>/<pid>" keys.
// Returns the add set (value true) and remove set (value false/null).
// Returns forbidden when the patch principal does not match the
// authenticated principal (REQ-PROTO-101).
func decodeReactionPatches(
	obj map[string]json.RawMessage,
	authedPID store.PrincipalID,
) (adds []reactionPatch, removes []reactionPatch, serr *setError) {
	const prefix = "reactions/"
	for k, v := range obj {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		// rest should be "<emoji>/<principalId>"
		slash := strings.LastIndex(rest, "/")
		if slash < 0 {
			return nil, nil, &setError{
				Type:        "invalidProperties",
				Properties:  []string{k},
				Description: "reactions patch key must be reactions/<emoji>/<principalId>",
			}
		}
		emoji := rest[:slash]
		pidStr := rest[slash+1:]
		if emoji == "" || pidStr == "" {
			return nil, nil, &setError{
				Type:        "invalidProperties",
				Properties:  []string{k},
				Description: "reactions patch key: emoji and principalId must be non-empty",
			}
		}
		// Parse the principal id from the wire string.
		pidUint, err := parseUintPrincipalID(pidStr)
		if err != nil {
			return nil, nil, &setError{
				Type:        "invalidProperties",
				Properties:  []string{k},
				Description: "reactions patch key: principalId is not a valid id",
			}
		}
		if store.PrincipalID(pidUint) != authedPID {
			return nil, nil, &setError{
				Type:        "forbidden",
				Description: "reactions patch: principalId must match the authenticated principal",
			}
		}
		p := reactionPatch{emoji: emoji, principalID: store.PrincipalID(pidUint)}
		// value: true = add; null / false = remove.
		isAdd := false
		if string(v) == "true" {
			isAdd = true
		} else if string(v) != "null" && string(v) != "false" {
			// Try JSON bool decode for robustness.
			var b bool
			if json.Unmarshal(v, &b) == nil {
				isAdd = b
			}
		}
		if isAdd {
			adds = append(adds, p)
		} else {
			removes = append(removes, p)
		}
	}
	return adds, removes, nil
}

// parseUintPrincipalID parses a decimal string into a uint64 principal id.
func parseUintPrincipalID(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty principal id")
	}
	v := uint64(0)
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit in principal id: %c", c)
		}
		v = v*10 + uint64(c-'0')
	}
	if v == 0 {
		return 0, fmt.Errorf("principal id must be > 0")
	}
	return v, nil
}

// hasNonReactionKeys reports whether obj has any key that is NOT a
// "reactions/<emoji>/<pid>" patch key. Used to detect a purely-reaction
// patch that can return early after reacting.
func hasNonReactionKeys(obj map[string]json.RawMessage) bool {
	for k := range obj {
		if !strings.HasPrefix(k, "reactions/") {
			return true
		}
	}
	return false
}

// applyReactionPatches writes add/remove rows to the email_reactions
// table and, for adds, triggers outbound cross-server dispatch.
func (h *handlerSet) applyReactionPatches(
	ctx context.Context,
	pid store.PrincipalID,
	m store.Message,
	adds []reactionPatch,
	removes []reactionPatch,
) (*setError, error) {
	now := h.clk.Now()
	for _, p := range adds {
		if err := h.store.Meta().AddEmailReaction(ctx, m.ID, p.emoji, p.principalID, now); err != nil {
			return nil, fmt.Errorf("add reaction: %w", err)
		}
		// Fire-and-forget outbound dispatch (REQ-FLOW-100..103).
		if h.reactionMailer != nil {
			go h.dispatchOutboundReaction(context.WithoutCancel(ctx), pid, m, p.emoji)
		}
	}
	for _, p := range removes {
		if err := h.store.Meta().RemoveEmailReaction(ctx, m.ID, p.emoji, p.principalID); err != nil {
			return nil, fmt.Errorf("remove reaction: %w", err)
		}
		// REQ-FLOW-103: removal does NOT propagate cross-server.
	}
	return nil, nil
}

// dispatchOutboundReaction looks up the reactor's principal info and
// the original message metadata, then delegates to the reactionMailer.
// Runs in a goroutine; logs errors but does not surface them.
func (h *handlerSet) dispatchOutboundReaction(
	ctx context.Context,
	pid store.PrincipalID,
	m store.Message,
	emoji string,
) {
	p, err := h.store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		h.logger.WarnContext(ctx, "email: reaction dispatch: principal lookup failed",
			slog.String("err", err.Error()))
		return
	}
	orig := mailreact.OriginalEmailInfo{
		MessageID:  m.Envelope.MessageID,
		Subject:    m.Envelope.Subject,
		References: "", // not cached — fine, References falls back to In-Reply-To only
	}
	// Build the flat recipient list from the cached envelope.
	for _, list := range []string{m.Envelope.To, m.Envelope.Cc, m.Envelope.Bcc} {
		for _, addr := range splitAddressList(list) {
			if addr != "" {
				orig.AllRecipients = append(orig.AllRecipients, addr)
			}
		}
	}
	reactor := mailreact.ReactorInfo{
		PrincipalID: pid,
		Address:     p.CanonicalEmail,
		DisplayName: p.DisplayName,
		Domain:      domainOf(p.CanonicalEmail),
	}
	if _, err := h.reactionMailer.BuildAndEnqueue(ctx, reactor, emoji, orig); err != nil {
		h.logger.WarnContext(ctx, "email: reaction dispatch: enqueue failed",
			slog.String("err", err.Error()))
	}
}

// domainOf returns the lowercased domain portion of an email address.
func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}

// splitAddressList splits a comma-separated address list into a flat
// slice of raw addresses, stripping display names and angle brackets.
func splitAddressList(raw string) []string {
	if raw == "" {
		return nil
	}
	// Use a simple heuristic: each comma-separated segment; trim whitespace
	// and angle brackets.
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Strip "Display Name <addr>" down to addr.
		if lt := strings.Index(p, "<"); lt >= 0 {
			if gt := strings.LastIndex(p, ">"); gt > lt {
				p = p[lt+1 : gt]
			}
		}
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
