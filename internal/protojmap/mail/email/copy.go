package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// copyRequest is the wire-form Email/copy request (RFC 8621 §4.7).
type copyRequest struct {
	FromAccountID jmapID                     `json:"fromAccountId"`
	AccountID     jmapID                     `json:"accountId"`
	Create        map[string]json.RawMessage `json:"create"`
	OnSuccess     *bool                      `json:"onSuccessDestroyOriginal"`
	IfInState     *string                    `json:"ifInState"`
}

// copyResponse is the wire-form response.
type copyResponse struct {
	FromAccountID jmapID               `json:"fromAccountId"`
	AccountID     jmapID               `json:"accountId"`
	OldState      string               `json:"oldState"`
	NewState      string               `json:"newState"`
	Created       map[string]jmapEmail `json:"created"`
	NotCreated    map[string]setError  `json:"notCreated"`
}

// copyHandler implements Email/copy.
type copyHandler struct{ h *handlerSet }

func (c *copyHandler) Method() string { return "Email/copy" }

// Execute copies emails from fromAccountId to accountId. Both source
// and destination may be foreign accounts the caller can access via
// ACL; the caller is the auth principal, while the email row is
// attributed to the destination account's owner.
//
// RFC 8621 §4.7: fromAccountId defaults to accountId when absent.
// RFC 8620 §6: fromAccountNotFound when the source account is not
// accessible; accountNotFound when the destination is not accessible.
func (c *copyHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req copyRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	// Destination account: must be the caller's own OR a foreign account
	// where the caller holds at least lookup. Per-mailbox insert rights
	// are enforced by createEmail's mb.PrincipalID == destPID gate plus
	// the caller having had to acquire the destination mailbox id from
	// a Mailbox/get on the foreign account in the first place.
	destPID, merr := resolveAccount(ctx, c.h.store.Meta(), req.AccountID, pid)
	if merr != nil {
		return nil, merr
	}
	// Source account: default to destination if absent, otherwise resolve
	// via ACL (cross-account copy from a shared account).
	fromAccountID := req.FromAccountID
	if fromAccountID == "" {
		fromAccountID = req.AccountID
	}
	sourcePID, fromMerr := resolveAccount(ctx, c.h.store.Meta(), fromAccountID, pid)
	if fromMerr != nil {
		// RFC 8620 §6: use fromAccountNotFound for source-account errors.
		return nil, protojmap.NewMethodError("fromAccountNotFound", fromMerr.Description)
	}

	// ifInState applies to the DESTINATION account state.
	state, err := currentState(ctx, c.h.store.Meta(), destPID)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch",
			"ifInState does not match current state")
	}

	resp := copyResponse{
		FromAccountID: req.FromAccountID,
		AccountID:     req.AccountID,
		OldState:      state,
		NewState:      state,
		Created:       map[string]jmapEmail{},
		NotCreated:    map[string]setError{},
	}

	type copyEntry struct {
		ID         jmapID          `json:"id"`
		MailboxIDs map[jmapID]bool `json:"mailboxIds"`
		Keywords   map[string]bool `json:"keywords"`
		ReceivedAt *string         `json:"receivedAt"`
	}
	for key, raw := range req.Create {
		var entry copyEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
			continue
		}
		mid, ok := emailIDFromJMAP(entry.ID)
		if !ok {
			resp.NotCreated[key] = setError{Type: "notFound"}
			continue
		}
		// Use pid (callerPID) for ACL enforcement when reading the source message,
		// so the ACL check reflects the actual caller's permissions.
		src, err := loadMessageForPrincipal(ctx, c.h.store.Meta(), pid, mid)
		if err != nil {
			if errors.Is(err, errMessageMissing) {
				resp.NotCreated[key] = setError{Type: "notFound"}
				continue
			}
			return nil, serverFail(err)
		}
		// Re-insert into the destination account via createEmail, attributing
		// the row to the destination owner (destPID) so per-account state and
		// mailbox-ownership invariants hold.
		in := emailCreateInput{
			BlobID:     src.Blob.Hash,
			MailboxIDs: entry.MailboxIDs,
			Keywords:   entry.Keywords,
			ReceivedAt: entry.ReceivedAt,
		}
		_, jm, serr, err := c.h.createEmail(ctx, destPID, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		resp.Created[key] = jm

		// onSuccessDestroyOriginal only works when source == caller's own
		// account (the ACL model doesn't grant us rights to expunge foreign
		// messages unless the caller explicitly has the expunge right, which
		// is separate from the read right used above).
		if req.OnSuccess != nil && *req.OnSuccess && sourcePID == pid {
			if err := c.h.store.Meta().ExpungeMessages(ctx, src.MailboxID, []store.MessageID{src.ID}); err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, serverFail(fmt.Errorf("email: copy onSuccess expunge: %w", err))
			}
			if _, err := c.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmail); err != nil {
				return nil, serverFail(err)
			}
		}
	}

	newState, err := currentState(ctx, c.h.store.Meta(), destPID)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

// readBlobBody reads the blob body fully into memory, capped to
// 64 MiB so a malicious upload cannot exhaust the heap. v1's largest
// supported message size is 50 MiB (default Options.MaxSizeUpload),
// so the cap is 28 % over the wire limit and never bites a legitimate
// upload.
func readBlobBody(ctx context.Context, blobs store.Blobs, hash string) ([]byte, error) {
	rc, err := blobs.Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// putCanonical writes body back through Blobs.Put (which CRLF-
// canonicalises and re-hashes). Used by /import / /set / /copy paths
// so the on-disk hash always matches the stored Message row.
func putCanonical(ctx context.Context, blobs store.Blobs, body []byte) (store.BlobRef, error) {
	return blobs.Put(ctx, bytes.NewReader(body))
}
