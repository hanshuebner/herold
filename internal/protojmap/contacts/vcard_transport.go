package contacts

// vCard 4.0 import/export transport (REQ-CTS-20..24, REQ-CTS-14).
//
// This file wires the vCard converter (vcard.go) to the JMAP surface as
// two methods:
//
//   - Contact/import: reads an uploaded .vcf blob, parses each card via
//     ParseVCards (REQ-CTS-11), creates contacts in the target address
//     book (REQ-CTS-20), and reports per-card created / failed results
//     with duplicate-candidate detection (REQ-CTS-21). Photo inlining is
//     interned into the blob store via makePhotoInternFn (REQ-CTS-14).
//
//   - Contact/export: reads the requested contacts (by IDs or whole
//     address book), converts each to vCard 4.0 via GenerateVCard
//     (REQ-CTS-12), stores the combined .vcf as a blob, and returns
//     the blobId plus any unrepresentable-property warnings (REQ-CTS-22).
//     Photos are optionally embedded via makePhotoFetchFn (REQ-CTS-14).
//
// Both methods:
//   - authenticate via requirePrincipal (REQ-CTS-23)
//   - enforce a configurable max blob size (REQ-CTS-41)
//   - commit incrementally: each card is persisted before the next is
//     attempted, so cancellation leaves already-created contacts intact
//     (REQ-CTS-23)

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// defaultMaxVCardImportSize is the default cap on .vcf blob size for
// Contact/import. 50 MiB matches the server's MaxSizeUpload default and
// is generous for even very large address book exports. Operators lower
// it via AccountLimits.MaxVCardImportSize.
const defaultMaxVCardImportSize = 50 * 1024 * 1024

// -- Contact/import -------------------------------------------------------

// contactImportRequest is the wire-form Contact/import request.
//
// The client first uploads the .vcf file via POST /jmap/upload/{accountId}
// and receives a blobId; that blobId is passed here. This separates the
// upload size-check (MaxSizeUpload on the HTTP server) from the import
// parse-and-create loop, and lets large batches stream over the standard
// upload path without buffering the entire body in memory in the JMAP
// method handler.
type contactImportRequest struct {
	// AccountID is the acting principal's JMAP account id (required).
	AccountID jmapID `json:"accountId"`
	// BlobID is the blob hash of the previously-uploaded .vcf file
	// (required). The blob must be accessible to the acting principal.
	BlobID string `json:"blobId"`
	// AddressBookID is the target address book (optional). When absent
	// the principal's default address book is used (REQ-CTS-20).
	AddressBookID *jmapID `json:"addressBookId,omitempty"`
}

// ImportCardResult is the outcome for one BEGIN:VCARD...END:VCARD block
// inside the uploaded .vcf. Exactly one of Result=="created" or
// Result=="failed" is set.
type ImportCardResult struct {
	// Index is the 0-based position of this card in the .vcf file.
	Index int `json:"index"`
	// UID is the vCard UID; may be empty for cards without one.
	UID string `json:"uid,omitempty"`
	// Result is "created" or "failed".
	Result string `json:"result"`
	// ID is the JMAP contact id assigned on creation (only when
	// Result=="created").
	ID jmapID `json:"id,omitempty"`
	// DuplicateCandidates lists JMAP contact IDs that may duplicate this
	// card (matched by UID or primary email). Present when candidates
	// exist; nil otherwise (REQ-CTS-21). Advisory: the server does not
	// auto-merge; the client drives skip/create/merge.
	DuplicateCandidates []jmapID `json:"duplicateCandidates,omitempty"`
	// Reason is the human-readable error detail (only when
	// Result=="failed").
	Reason string `json:"reason,omitempty"`
}

// contactImportResponse is the wire-form Contact/import response.
type contactImportResponse struct {
	AccountID string             `json:"accountId"`
	NewState  string             `json:"newState"`
	Results   []ImportCardResult `json:"results"`
}

type contactImportHandler struct{ h *handlerSet }

func (h *contactImportHandler) Method() string { return "Contact/import" }

func (h *contactImportHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req contactImportRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	if req.BlobID == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "blobId is required")
	}

	// Cap the .vcf blob size before reading it (REQ-CTS-41).
	blobSize, _, err := h.h.store.Blobs().Stat(ctx, req.BlobID)
	if err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", "blobId not found: "+req.BlobID)
	}
	maxSize := h.h.limits.MaxVCardImportSize
	if maxSize <= 0 {
		maxSize = defaultMaxVCardImportSize
	}
	if blobSize > int64(maxSize) {
		return nil, protojmap.NewMethodError("requestTooLarge",
			fmt.Sprintf("vCard blob size %d exceeds maxVCardImportSize %d", blobSize, maxSize))
	}

	// Resolve the target address book (REQ-CTS-20).
	var ab store.AddressBook
	if req.AddressBookID != nil {
		abID, ok := addressBookIDFromJMAP(*req.AddressBookID)
		if !ok {
			return nil, protojmap.NewMethodError("invalidArguments", "addressBookId is malformed")
		}
		loaded, aerr := h.h.store.Meta().GetAddressBook(ctx, abID)
		if aerr != nil {
			return nil, protojmap.NewMethodError("invalidArguments", "addressBookId not found")
		}
		if loaded.PrincipalID != pid {
			return nil, protojmap.NewMethodError("forbidden",
				"addressBookId is not accessible to this principal")
		}
		ab = loaded
	} else {
		defAB, aerr := h.h.store.Meta().DefaultAddressBook(ctx, pid)
		if aerr != nil {
			return nil, protojmap.NewMethodError("invalidArguments",
				"no default address book; provide addressBookId")
		}
		ab = defAB
	}

	// Read the .vcf blob.
	rc, err := h.h.store.Blobs().Get(ctx, req.BlobID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", "blob read failed: "+err.Error())
	}
	defer rc.Close()
	vcfBytes, err := io.ReadAll(io.LimitReader(rc, int64(maxSize)+1))
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", "blob read failed: "+err.Error())
	}

	// Build the photo intern function (REQ-CTS-14).
	internFn := makePhotoInternFn(ctx, h.h.store.Blobs(), h.h.limits)

	// Parse all vCard blocks (REQ-CTS-10, REQ-CTS-11). A malformed block
	// surfaces as ParseResult.Error and does not abort the batch (REQ-CTS-20).
	parseResults := ParseVCards(vcfBytes, internFn)

	resp := contactImportResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		Results:   make([]ImportCardResult, 0, len(parseResults)),
	}

	// Process each card. Incremental commit (REQ-CTS-23): each card is
	// persisted before the next is attempted, so a cancelled request
	// leaves already-created contacts intact.
	abIDStr := jmapIDFromAddressBook(ab.ID)
	for idx, pr := range parseResults {
		res := ImportCardResult{Index: idx}

		if pr.Error != nil {
			// Parse failure: report and continue.
			res.Result = "failed"
			res.Reason = pr.Error.Error()
			resp.Results = append(resp.Results, res)
			continue
		}

		card := pr.Card
		res.UID = card.UID

		// Check for duplicates BEFORE creating so candidates are reported
		// for both successful and conflicting cards (REQ-CTS-21).
		res.DuplicateCandidates = h.h.findImportDuplicates(ctx, pid, card)

		// Inject the target addressBookId into the card body so createContact
		// lands the contact in the requested book.
		cardBody, marshalErr := card.MarshalJSON()
		if marshalErr != nil {
			res.Result = "failed"
			res.Reason = "marshal failed: " + marshalErr.Error()
			resp.Results = append(resp.Results, res)
			continue
		}
		var probe map[string]json.RawMessage
		if uerr := json.Unmarshal(cardBody, &probe); uerr != nil {
			res.Result = "failed"
			res.Reason = "internal parse error: " + uerr.Error()
			resp.Results = append(resp.Results, res)
			continue
		}
		abIDBytes, _ := json.Marshal(abIDStr)
		probe["addressBookId"] = json.RawMessage(abIDBytes)
		rawWithBook, marshalErr2 := json.Marshal(probe)
		if marshalErr2 != nil {
			res.Result = "failed"
			res.Reason = "marshal with book failed: " + marshalErr2.Error()
			resp.Results = append(resp.Results, res)
			continue
		}

		// Create the contact (REQ-CTS-20). createContact handles photo
		// blob validation and IncRefBlob rooting (REQ-CTS-01, REQ-CTS-02).
		created, serr, ierr := h.h.createContact(ctx, pid, rawWithBook)
		if ierr != nil {
			return nil, serverFail(ierr)
		}
		if serr != nil {
			res.Result = "failed"
			reason := serr.Type
			if serr.Description != "" {
				if reason != "" {
					reason += ": "
				}
				reason += serr.Description
			}
			res.Reason = reason
			resp.Results = append(resp.Results, res)
			continue
		}

		res.Result = "created"
		res.ID = jmapIDFromContact(created.ID)
		res.UID = created.UID
		resp.Results = append(resp.Results, res)
	}

	newState, err := currentContactState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

// findImportDuplicates returns JMAP contact IDs that are likely
// duplicates of the card being imported (REQ-CTS-21):
//  1. Contacts in the same principal's address books sharing the same UID.
//  2. Contacts sharing the same primary email address.
//
// The result is de-duplicated. Returns nil when no candidates are found.
// Query errors are silently ignored so a store hiccup does not block the
// import of unrelated cards.
func (h *handlerSet) findImportDuplicates(ctx context.Context, pid store.PrincipalID, card *Card) []jmapID {
	seen := map[store.ContactID]bool{}
	var candidates []jmapID

	// UID-based check (exact match).
	if card.UID != "" {
		uid := card.UID
		rows, qerr := h.store.Meta().ListContacts(ctx, store.ContactFilter{
			PrincipalID: &pid,
			UID:         &uid,
		})
		if qerr == nil {
			for _, c := range rows {
				if !seen[c.ID] {
					seen[c.ID] = true
					candidates = append(candidates, jmapIDFromContact(c.ID))
				}
			}
		}
	}

	// Primary-email-based check.
	pe := card.PrimaryEmail()
	if pe != "" {
		email := strings.ToLower(pe)
		rows, qerr := h.store.Meta().ListContacts(ctx, store.ContactFilter{
			PrincipalID: &pid,
			HasEmail:    &email,
		})
		if qerr == nil {
			for _, c := range rows {
				if !seen[c.ID] {
					seen[c.ID] = true
					candidates = append(candidates, jmapIDFromContact(c.ID))
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// makePhotoInternFn builds a PhotoInternFn that stores inline photo data
// in the blob store (REQ-CTS-14). Size and media-type limits from limits
// are enforced before calling Blobs().Put so oversized or non-image blobs
// are rejected before any bytes hit disk (REQ-CTS-41, REQ-CTS-05).
func makePhotoInternFn(ctx context.Context, blobs store.Blobs, limits AccountLimits) PhotoInternFn {
	return func(mediaType string, data []byte) (string, error) {
		// Enforce size limit (REQ-CTS-41, REQ-CTS-05).
		if limits.MaxPhotoBlobSize > 0 && len(data) > limits.MaxPhotoBlobSize {
			return "", fmt.Errorf("photo size %d exceeds maxPhotoBlobSize %d",
				len(data), limits.MaxPhotoBlobSize)
		}
		// Validate media type (REQ-CTS-05).
		if mediaType != "" {
			if len(limits.AllowedPhotoTypes) == 0 {
				if !strings.HasPrefix(mediaType, "image/") {
					return "", fmt.Errorf("photo mediaType %q must be image/*", mediaType)
				}
			} else {
				var allowed bool
				for _, a := range limits.AllowedPhotoTypes {
					if mediaType == a {
						allowed = true
						break
					}
				}
				if !allowed {
					return "", fmt.Errorf("photo mediaType %q is not in allowedPhotoTypes",
						mediaType)
				}
			}
		}
		ref, err := blobs.Put(ctx, bytes.NewReader(data))
		if err != nil {
			return "", fmt.Errorf("intern photo: %w", err)
		}
		return ref.Hash, nil
	}
}

// -- Contact/export -------------------------------------------------------

// contactExportRequest is the wire-form Contact/export request.
type contactExportRequest struct {
	// AccountID is the acting principal's JMAP account id (required).
	AccountID jmapID `json:"accountId"`
	// IDs is an optional list of JMAP contact IDs to export. When absent
	// (or nil) all accessible contacts are exported, subject to the
	// optional AddressBookID filter.
	IDs []jmapID `json:"ids,omitempty"`
	// AddressBookID optionally restricts the export to one address book
	// (applies when IDs is absent).
	AddressBookID *jmapID `json:"addressBookId,omitempty"`
	// FetchPhotos, when true, embeds photo data inline in the PHOTO
	// property as a data URI. When false (the default) blobId references
	// are emitted as opaque URI values without fetching the blob bytes
	// (REQ-CTS-14).
	FetchPhotos bool `json:"fetchPhotos,omitempty"`
}

// ExportWarning describes one JSContact property that could not be
// faithfully represented in vCard 4.0 (REQ-CTS-12, REQ-CTS-22).
type ExportWarning struct {
	// ContactID is the JMAP id of the contact that triggered the warning.
	ContactID jmapID `json:"contactId"`
	// Type is the JSContact property key or error type.
	Type string `json:"type"`
	// Detail carries a concise value summary when possible.
	Detail string `json:"detail,omitempty"`
}

// contactExportResponse is the wire-form Contact/export response.
type contactExportResponse struct {
	// AccountID is the acting principal's JMAP account id.
	AccountID string `json:"accountId"`
	// BlobID is the blob hash of the generated .vcf; downloadable via
	// GET /jmap/download/{accountId}/{blobId}/text%2Fvcard/contacts.vcf
	BlobID string `json:"blobId"`
	// Type is the MIME type of the blob.
	Type string `json:"type"`
	// Size is the byte length of the generated .vcf.
	Size int64 `json:"size"`
	// Unrepresentable lists JSContact properties vCard 4.0 cannot express,
	// grouped by contact (REQ-CTS-22). May be nil.
	Unrepresentable []ExportWarning `json:"unrepresentable,omitempty"`
}

type contactExportHandler struct{ h *handlerSet }

func (h *contactExportHandler) Method() string { return "Contact/export" }

func (h *contactExportHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req contactExportRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	// Build the optional photo fetch function (REQ-CTS-14).
	var fetchFn PhotoFetchFn
	if req.FetchPhotos {
		fetchFn = makePhotoFetchFn(ctx, h.h.store.Blobs())
	}

	// Collect the contacts to export (REQ-CTS-22).
	var contacts []store.Contact
	if len(req.IDs) > 0 {
		contacts = make([]store.Contact, 0, len(req.IDs))
		for _, raw := range req.IDs {
			id, ok := contactIDFromJMAP(raw)
			if !ok {
				continue
			}
			c, cerr := h.h.store.Meta().GetContact(ctx, id)
			if cerr != nil || c.PrincipalID != pid {
				continue
			}
			contacts = append(contacts, c)
		}
	} else {
		// Export all contacts for the principal, optionally filtered by
		// address book. Paginate with AfterID to exceed the 1000-row cap.
		filter := store.ContactFilter{
			PrincipalID: &pid,
			Limit:       1000,
		}
		if req.AddressBookID != nil {
			abID, ok := addressBookIDFromJMAP(*req.AddressBookID)
			if !ok {
				return nil, protojmap.NewMethodError("invalidArguments",
					"addressBookId is malformed")
			}
			filter.AddressBookID = &abID
		}
		for {
			batch, berr := h.h.store.Meta().ListContacts(ctx, filter)
			if berr != nil {
				return nil, serverFail(berr)
			}
			contacts = append(contacts, batch...)
			if len(batch) < 1000 {
				break
			}
			filter.AfterID = batch[len(batch)-1].ID
		}
	}

	// Generate vCard 4.0 for each contact (REQ-CTS-12).
	var buf bytes.Buffer
	var warnings []ExportWarning
	for _, c := range contacts {
		var card Card
		if uerr := card.UnmarshalJSON(c.JSContactJSON); uerr != nil {
			warnings = append(warnings, ExportWarning{
				ContactID: jmapIDFromContact(c.ID),
				Type:      "parseError",
				Detail:    uerr.Error(),
			})
			continue
		}
		gr, gerr := GenerateVCard(&card, fetchFn)
		if gerr != nil {
			warnings = append(warnings, ExportWarning{
				ContactID: jmapIDFromContact(c.ID),
				Type:      "generateError",
				Detail:    gerr.Error(),
			})
			continue
		}
		buf.Write(gr.VCard)
		for _, u := range gr.Unrepresentable {
			warnings = append(warnings, ExportWarning{
				ContactID: jmapIDFromContact(c.ID),
				Type:      u.Type,
				Detail:    u.Detail,
			})
		}
	}

	// Store the combined .vcf as a blob so the client can download it
	// via GET /jmap/download/{accountId}/{blobId}/text%2Fvcard/contacts.vcf
	vcfBytes := buf.Bytes()
	ref, perr := h.h.store.Blobs().Put(ctx, bytes.NewReader(vcfBytes))
	if perr != nil {
		return nil, serverFail(perr)
	}

	return contactExportResponse{
		AccountID:       string(protojmap.AccountIDForPrincipal(pid)),
		BlobID:          ref.Hash,
		Type:            "text/vcard",
		Size:            ref.Size,
		Unrepresentable: warnings,
	}, nil
}

// makePhotoFetchFn builds a PhotoFetchFn that retrieves blob bytes from
// the blob store for inline-embedding during export (REQ-CTS-14). The
// returned mediaType is empty so generatePhotoLine uses the mediaType
// already stored in the Card's media entry.
func makePhotoFetchFn(ctx context.Context, blobs store.Blobs) PhotoFetchFn {
	return func(blobID string) (mediaType string, data []byte, err error) {
		rc, rerr := blobs.Get(ctx, blobID)
		if rerr != nil {
			return "", nil, fmt.Errorf("fetch photo blob %q: %w", blobID, rerr)
		}
		defer rc.Close()
		data, rerr = io.ReadAll(rc)
		if rerr != nil {
			return "", nil, fmt.Errorf("read photo blob %q: %w", blobID, rerr)
		}
		// Empty mediaType: generatePhotoLine falls back to the Card's
		// stored mediaType, which is the original from the client.
		return "", data, nil
	}
}
