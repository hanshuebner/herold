package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	if merr := requireAccount(req.AccountID, pid); merr != nil {
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
	}

	newState, err := currentState(ctx, i.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
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
	if len(in.MailboxIDs) > 1 {
		return jmapEmail{}, &setError{
			Type: "invalidProperties", Properties: []string{"mailboxIds"},
			Description: "v1 supports a single mailbox per import",
		}, nil
	}
	var mailboxID store.MailboxID
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
		mailboxID = id
	}
	mb, err := i.h.store.Meta().GetMailboxByID(ctx, mailboxID)
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
			Description: "v1 does not allow importing into a non-owned mailbox",
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
	env := buildEnvelopeFromParsed(parsed)

	receivedAt := i.h.clk.Now()
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
	uid, modseq, err := i.h.store.Meta().InsertMessage(ctx, msg)
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
	return renderEmailMetadata(msg), nil, nil
}
