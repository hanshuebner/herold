package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// importEntry is one row inside an Email/import request's "emails"
// object.
type importEntry struct {
	BlobID     string          `json:"blobId"`
	MailboxIDs map[jmapID]bool `json:"mailboxIds"`
	Keywords   map[string]bool `json:"keywords"`
	ReceivedAt *string         `json:"receivedAt"`
}

// importRequest is the wire-form Email/import request (RFC 8621 §4.8).
type importRequest struct {
	AccountID jmapID                 `json:"accountId"`
	IfInState *string                `json:"ifInState"`
	Emails    map[string]importEntry `json:"emails"`
}

// importResponse is the wire-form response.
type importResponse struct {
	AccountID  jmapID               `json:"accountId"`
	OldState   string               `json:"oldState"`
	NewState   string               `json:"newState"`
	Created    map[string]jmapEmail `json:"created"`
	NotCreated map[string]setError  `json:"notCreated"`
}

// importHandler implements Email/import. We deliberately pick the
// "minimal" path: blob -> parse -> InsertMessage, skipping the
// SMTP-side mail-auth checks (DKIM/SPF/DMARC) and the Sieve pipeline
// because Email/import does not flow through a real SMTP delivery.
// Operators who need the rule pipeline should accept inbound mail via
// SMTP submission instead. RFC 8621 permits this — §4.8 says the
// server "MAY" run filters and notes import is "an alternative to
// SMTP".
type importHandler struct{ h *handlerSet }

func (i *importHandler) Method() string { return "Email/import" }

func (i *importHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req importRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireOwnAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentState(ctx, i.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch",
			"ifInState does not match current state")
	}

	resp := importResponse{
		AccountID:  req.AccountID,
		OldState:   state,
		NewState:   state,
		Created:    map[string]jmapEmail{},
		NotCreated: map[string]setError{},
	}

	var createdIDs []store.MessageID
	for key, entry := range req.Emails {
		jm, serr, err := i.importOne(ctx, pid, entry)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		resp.Created[key] = jm
		// Track the created MessageID for post-import thread re-linking.
		mid, _ := emailIDFromJMAP(jm.ID)
		if mid != 0 {
			createdIDs = append(createdIDs, mid)
		}
	}

	// Post-import thread re-linking: when emails are imported out of order
	// (replies before originals), thread_id resolution at InsertMessage time
	// fails because the ancestor is not yet present. After all emails are
	// inserted, walk the created messages and re-link any whose InReplyTo
	// ancestor now exists.
	if err := i.relinkThreads(ctx, pid, createdIDs); err != nil {
		return nil, serverFail(err)
	}

	newState, err := currentState(ctx, i.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

// relinkThreads walks createdIDs and, for each message whose InReplyTo
// or References points to an ancestor that now exists in the store,
// updates the thread_id to match the ancestor's thread. This fixes
// threading when replies are imported before their originals.
//
// We check both In-Reply-To and References (RFC 5256 sec 2.2) so that
// messages that reference an ancestor only via References (common when
// three or more messages form a chain and intermediate messages are
// absent) still land in the same thread.
func (i *importHandler) relinkThreads(ctx context.Context, pid store.PrincipalID, createdIDs []store.MessageID) error {
	if len(createdIDs) == 0 {
		return nil
	}
	for _, mid := range createdIDs {
		msg, err := i.h.store.Meta().GetMessage(ctx, mid)
		if err != nil {
			continue // silently skip; best-effort
		}
		if msg.Envelope.InReplyTo == "" && msg.Envelope.References == "" {
			continue
		}
		if msg.ThreadID != 0 && msg.ThreadID != uint64(msg.ID) {
			continue // already properly threaded (ancestor was present at import time)
		}
		// Union InReplyTo and References, InReplyTo first.
		refs := mailparse.ParseReferences(msg.Envelope.InReplyTo)
		seen := make(map[string]struct{}, len(refs))
		for _, r := range refs {
			seen[r] = struct{}{}
		}
		for _, r := range mailparse.ParseReferences(msg.Envelope.References) {
			if _, dup := seen[r]; !dup {
				refs = append(refs, r)
				seen[r] = struct{}{}
			}
		}
		for _, ref := range refs {
			ancestor, err := i.h.store.Meta().GetMessageByMessageIDHeader(ctx, pid, ref)
			if err != nil {
				continue
			}
			// Determine the thread key for the ancestor.
			var ancestorThread uint64
			if ancestor.ThreadID != 0 {
				ancestorThread = ancestor.ThreadID
			} else {
				ancestorThread = uint64(ancestor.ID)
			}
			if ancestorThread == uint64(mid) {
				continue // would create a self-link
			}
			if err := i.h.store.Meta().UpdateMessageThreadID(ctx, mid, ancestorThread); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// importOne is the per-entry workhorse. Mirrors createEmail but with
// the import-specific input shape (a fully-formed RFC 5322 blob is
// the source of truth, not a synthetic envelope payload).
func (i *importHandler) importOne(
	ctx context.Context,
	pid store.PrincipalID,
	in importEntry,
) (jmapEmail, *setError, error) {
	if in.BlobID == "" {
		return jmapEmail{}, &setError{
			Type: "invalidProperties", Properties: []string{"blobId"},
			Description: "blobId is required",
		}, nil
	}
	if len(in.MailboxIDs) == 0 {
		return jmapEmail{}, &setError{
			Type: "invalidProperties", Properties: []string{"mailboxIds"},
			Description: "mailboxIds must list at least one mailbox",
		}, nil
	}
	// JMAP Email/import accepts a {mailboxId: true} map identifying every
	// mailbox the imported message should be linked into. Migration 0024
	// (multi-mailbox membership) lets the store represent that natively
	// via message_mailboxes; we walk the input map, validate each id is
	// real and owned by the requesting principal, and pass the full set
	// through to InsertMessage.
	mailboxIDs := make([]store.MailboxID, 0, len(in.MailboxIDs))
	for raw, present := range in.MailboxIDs {
		if !present {
			continue
		}
		id, ok := mailboxIDFromJMAP(raw)
		if !ok {
			return jmapEmail{}, &setError{
				Type: "invalidProperties", Properties: []string{"mailboxIds"},
				Description: "mailboxIds carries an unparseable id",
			}, nil
		}
		mb, err := i.h.store.Meta().GetMailboxByID(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return jmapEmail{}, &setError{
					Type: "invalidProperties", Properties: []string{"mailboxIds"},
					Description: "mailbox does not exist",
				}, nil
			}
			return jmapEmail{}, nil, err
		}
		if mb.PrincipalID != pid {
			return jmapEmail{}, &setError{
				Type:        "forbidden",
				Description: "mailboxIds includes a mailbox not owned by the requesting principal",
			}, nil
		}
		mailboxIDs = append(mailboxIDs, id)
	}
	if len(mailboxIDs) == 0 {
		return jmapEmail{}, &setError{
			Type: "invalidProperties", Properties: []string{"mailboxIds"},
			Description: "mailboxIds must list at least one mailbox set to true",
		}, nil
	}

	body, err := readBlobBody(ctx, i.h.store.Blobs(), in.BlobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return jmapEmail{}, &setError{
				Type: "invalidProperties", Properties: []string{"blobId"},
				Description: "blob not found",
			}, nil
		}
		return jmapEmail{}, nil, err
	}
	ref, err := putCanonical(ctx, i.h.store.Blobs(), body)
	if err != nil {
		return jmapEmail{}, nil, fmt.Errorf("email: re-put blob: %w", err)
	}
	parser := i.h.parseFn
	if parser == nil {
		parser = defaultParseFn
	}
	parsed, parseErr := parser(bytes.NewReader(body))
	if parseErr != nil {
		return jmapEmail{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"blobId"},
			Description: "blob is not a valid RFC 5322 message: " + parseErr.Error(),
		}, nil
	}
	// RFC 5322 requires at minimum a Date and From header.  A blob with
	// no headers at all (e.g. arbitrary binary data) is not a valid
	// message even if the parser did not reject it outright.
	if len(parsed.Headers.Keys()) == 0 {
		return jmapEmail{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"blobId"},
			Description: "blob has no RFC 5322 headers; not a valid message",
		}, nil
	}
	env := buildEnvelopeFromParsed(parsed)

	receivedAt := i.h.clk.Now()
	if in.ReceivedAt != nil {
		if t, perr := time.Parse(time.RFC3339, *in.ReceivedAt); perr == nil {
			receivedAt = t
		}
	}
	flags, customKW := flagsAndKeywordsFromJMAP(in.Keywords)
	msg := store.Message{
		PrincipalID:  pid,
		InternalDate: receivedAt,
		ReceivedAt:   receivedAt,
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     env,
	}
	memberships := make([]store.MessageMailbox, 0, len(mailboxIDs))
	for _, mid := range mailboxIDs {
		memberships = append(memberships, store.MessageMailbox{
			MailboxID: mid,
			Flags:     flags,
			Keywords:  customKW,
		})
	}
	uid, modseq, err := i.h.store.Meta().InsertMessage(ctx, msg, memberships)
	if err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			return jmapEmail{}, &setError{
				Type: "overQuota", Description: "principal is over quota",
			}, nil
		}
		return jmapEmail{}, nil, fmt.Errorf("email: import insert: %w", err)
	}
	msg.UID = uid
	msg.ModSeq = modseq
	mid, err := mostRecentEmailCreatedID(ctx, i.h.store.Meta(), pid)
	if err != nil {
		return jmapEmail{}, nil, fmt.Errorf("email: resolve id: %w", err)
	}
	msg.ID = mid

	if _, err := i.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmail); err != nil {
		return jmapEmail{}, nil, fmt.Errorf("email: bump state: %w", err)
	}
	// Thread membership changed: bump Thread state.
	if _, err := i.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindThread); err != nil {
		return jmapEmail{}, nil, fmt.Errorf("email: bump thread state: %w", err)
	}
	return renderEmailMetadata(msg), nil, nil
}
