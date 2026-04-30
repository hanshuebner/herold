package protojmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// blobCopyHandler implements Blob/copy (RFC 8620 §6.3). v1 supports
// only the minimal compliant subset: copying blobs between different
// accounts. Copying within the same account is rejected as
// "invalidArguments" per RFC 8620 §6.3.
type blobCopyHandler struct {
	store store.Store
}

func (blobCopyHandler) Method() string { return "Blob/copy" }

// blobCopyRequest is the wire-form Blob/copy request.
type blobCopyRequest struct {
	FromAccountID Id   `json:"fromAccountId"`
	AccountID     Id   `json:"accountId"`
	BlobIDs       []Id `json:"blobIds"`
}

// blobCopyResponse is the wire-form Blob/copy response.
type blobCopyResponse struct {
	FromAccountID Id                   `json:"fromAccountId"`
	AccountID     Id                   `json:"accountId"`
	Copied        map[Id]Id            `json:"copied,omitempty"`
	NotCopied     map[Id]blobNotCopied `json:"notCopied,omitempty"`
}

type blobNotCopied struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

func (h blobCopyHandler) Execute(ctx context.Context, args json.RawMessage) (any, *MethodError) {
	var req blobCopyRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, NewMethodError("invalidArguments", err.Error())
		}
	}
	// RFC 8620 §6.3: same-account copy is rejected.
	if req.FromAccountID == req.AccountID {
		return nil, NewMethodError("invalidArguments",
			"fromAccountId and accountId must differ; same-account blob copy is not permitted")
	}
	// v1: cross-account blob copy between different principals is not
	// implemented (each principal has an isolated blob namespace).
	// Return notCopied for all blobIds.
	resp := blobCopyResponse{
		FromAccountID: req.FromAccountID,
		AccountID:     req.AccountID,
	}
	if len(req.BlobIDs) > 0 {
		resp.NotCopied = make(map[Id]blobNotCopied, len(req.BlobIDs))
		for _, bid := range req.BlobIDs {
			resp.NotCopied[bid] = blobNotCopied{
				Type:        "blobNotFound",
				Description: "cross-account blob copy is not supported in v1",
			}
		}
	}
	return resp, nil
}

// uploadResponse is the body returned by POST /jmap/upload (RFC 8620
// §6.1).
type uploadResponse struct {
	AccountID Id     `json:"accountId"`
	BlobID    string `json:"blobId"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
}

// handleUpload accepts a body of bytes and stores them in the blob
// store, returning the BLAKE3 hash as the blob id. The accountId path
// segment must match the authenticated principal's account id; cross-
// account uploads (impersonation) are out of scope for v1.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		WriteJMAPError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	accountID := r.PathValue("accountId")
	pid, ok := principalIDFromAccountID(accountID)
	if !ok || pid != p.ID {
		WriteJMAPError(w, http.StatusForbidden, "accountNotFound",
			"account does not match authenticated principal")
		return
	}
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	body := http.MaxBytesReader(w, r.Body, s.opts.MaxSizeUpload)
	defer body.Close()
	ref, err := s.store.Blobs().Put(r.Context(), body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			WriteJMAPError(w, http.StatusRequestEntityTooLarge,
				"limitTooLarge", "upload exceeds maxSizeUpload")
			return
		}
		s.log.Warn("upload.put_failed", "err", err)
		WriteJMAPError(w, http.StatusInternalServerError,
			"serverFail", "blob put failed")
		return
	}
	resp := uploadResponse{
		AccountID: accountID,
		BlobID:    ref.Hash,
		Type:      contentType,
		Size:      ref.Size,
	}
	log := loggerFromContext(r.Context(), s.log)
	log.Info("upload",
		"activity", "user",
		"principal_id", uint64(p.ID),
		"account_id", accountID,
		"blob_id", ref.Hash,
		"size_bytes", ref.Size,
		"content_type", contentType,
	)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// isLikelyBlobHash reports whether s looks like a hex-encoded BLAKE3
// digest (64 lowercase hex chars). Used as a cheap sanity guard
// before passing a candidate hash through to a substring scan in
// the metadata store; rejects URL-injection patterns like "%" or
// path-traversal segments.
func isLikelyBlobHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(('0' <= c && c <= '9') || ('a' <= c && c <= 'f')) {
			return false
		}
	}
	return true
}

// partBlobID reports whether blobID is a synthetic per-part blob reference of
// the form "<msgBlobHash>/p<N>" produced by render.go's walkParts. When true it
// returns the message blob hash and the 1-based part index.
func partBlobID(blobID string) (msgHash string, partIdx int, ok bool) {
	slash := strings.LastIndex(blobID, "/")
	if slash < 0 {
		return "", 0, false
	}
	suffix := blobID[slash+1:]
	if !strings.HasPrefix(suffix, "p") {
		return "", 0, false
	}
	n, err := strconv.Atoi(suffix[1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return blobID[:slash], n, true
}

// partBlobResult carries the decoded content extracted from a MIME part.
type partBlobResult struct {
	contentType string
	data        []byte
}

// resolvePartBlob fetches message blob msgHash from blobs, parses the MIME
// tree, and returns the decoded bytes of the part at 1-based pre-order DFS
// index partIdx (the same enumeration walkParts uses when assigning blobId
// values to each EmailBodyPart).
func resolvePartBlob(ctx context.Context, blobs store.Blobs, msgHash string, partIdx int) (*partBlobResult, error) {
	rc, err := blobs.Get(ctx, msgHash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("resolvePartBlob: read: %w", err)
	}
	parsed, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		return nil, fmt.Errorf("resolvePartBlob: parse: %w", err)
	}
	// Walk the part tree in the same pre-order DFS as walkParts so that the
	// index here matches the partId assigned during rendering.
	idx := 0
	var found *partBlobResult
	var walk func(p mailparse.Part)
	walk = func(p mailparse.Part) {
		if found != nil {
			return
		}
		idx++
		if idx == partIdx {
			ct := p.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			data := p.Bytes
			if len(data) == 0 && p.Text != "" {
				data = []byte(p.Text)
			}
			found = &partBlobResult{contentType: ct, data: data}
			return
		}
		for _, c := range p.Children {
			walk(c)
		}
	}
	walk(parsed.Body)
	if found == nil {
		return nil, store.ErrNotFound
	}
	return found, nil
}

// handleDownload streams a blob back to the client. The path bears the
// accountId, blobId, content type, and human-friendly filename per the
// JMAP downloadUrl template.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		WriteJMAPError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	accountID := r.PathValue("accountId")
	ownerPID, ownerOK := principalIDFromAccountID(accountID)
	if !ownerOK {
		WriteJMAPError(w, http.StatusForbidden, "accountNotFound",
			"account does not match authenticated principal")
		return
	}
	blobID := r.PathValue("blobId")
	contentType := r.PathValue("type")
	// Cross-account access via the chat fan-out path: the suite embeds
	// `<img src="/jmap/download/{senderAccountId}/...">` into chat
	// messages, so a recipient (different accountId from the sender)
	// must be allowed through if they are a member of any conversation
	// referencing this blob hash. Mail / contact / calendar blobs keep
	// the strict same-account check — there is no cross-account fan-out
	// path for those.
	if ownerPID != p.ID {
		hashCandidate := blobID
		if msgHash, _, isPartBlob := partBlobID(blobID); isPartBlob {
			hashCandidate = msgHash
		}
		if !isLikelyBlobHash(hashCandidate) {
			WriteJMAPError(w, http.StatusForbidden, "accountNotFound",
				"account does not match authenticated principal")
			return
		}
		canRead, err := s.store.Meta().ChatPrincipalCanReadBlob(r.Context(), p.ID, hashCandidate)
		if err != nil {
			s.log.Warn("download.chat_auth_failed", "err", err.Error(), "blob", blobID)
			WriteJMAPError(w, http.StatusInternalServerError,
				"serverFail", "chat-blob auth failed")
			return
		}
		if !canRead {
			WriteJMAPError(w, http.StatusForbidden, "accountNotFound",
				"account does not match authenticated principal")
			return
		}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	name := r.PathValue("name")
	// Disposition: clients render in-browser previews (PDF iframe, image
	// lightbox) by appending `?disposition=inline`. Anything else — and
	// the empty default — maps to `attachment` so a plain download link
	// behaves the same as before. Browsers refuse to render an iframe
	// whose response carries `attachment`, which produced the original
	// "white lightbox + download prompt" symptom on PDF preview.
	disposition := "attachment"
	if v := r.URL.Query().Get("disposition"); v == "inline" {
		disposition = "inline"
	}

	// Synthetic per-part blob IDs have the form "<msgBlobHash>/p<N>".
	// The blob store holds only full message blobs; resolve these by
	// re-parsing the message and extracting the indicated MIME part.
	if msgHash, partIdx, isPartBlob := partBlobID(blobID); isPartBlob {
		part, err := resolvePartBlob(r.Context(), s.store.Blobs(), msgHash, partIdx)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				WriteJMAPError(w, http.StatusNotFound, "blobNotFound", blobID)
				return
			}
			s.log.Warn("download.part_resolve_failed", "err", err, "blob", blobID)
			WriteJMAPError(w, http.StatusInternalServerError,
				"serverFail", "part blob resolve failed")
			return
		}
		// Rate-limit by decoded part size (REQ-STORE-20..25).
		size := int64(len(part.data))
		bucket := s.dlBucket(p.ID)
		if bucket != nil && !p.Flags.Has(store.PrincipalFlagIgnoreDownloadLimits) {
			ok, retryAfter := bucket.tryConsume(size)
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
				WriteJMAPError(w, http.StatusTooManyRequests,
					"rateLimited",
					fmt.Sprintf("download bandwidth budget exhausted; retry after %s", retryAfter))
				return
			}
		}
		// Use the MIME part's own content-type when the client does not
		// override it (the URL template always substitutes {type}, so
		// contentType is always set; keeping the part's own type here is
		// still useful when the caller passes "application/octet-stream"
		// as the generic fallback).
		if contentType == "application/octet-stream" && part.contentType != "" {
			contentType = part.contentType
		}
		log := loggerFromContext(r.Context(), s.log)
		log.Info("download",
			"activity", "user",
			"principal_id", uint64(p.ID),
			"account_id", accountID,
			"blob_id", blobID,
			"size_bytes", size,
			"content_type", contentType,
		)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		if name != "" {
			w.Header().Set("Content-Disposition",
				fmt.Sprintf(`%s; filename=%q`, disposition, name))
		}
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, bytes.NewReader(part.data)); err != nil {
			s.log.Warn("download.copy_failed", "err", err, "blob", blobID)
		}
		return
	}

	size, _, err := s.store.Blobs().Stat(r.Context(), blobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			WriteJMAPError(w, http.StatusNotFound, "blobNotFound", blobID)
			return
		}
		s.log.Warn("download.stat_failed", "err", err, "blob", blobID)
		WriteJMAPError(w, http.StatusInternalServerError,
			"serverFail", "stat failed")
		return
	}
	// Per-principal download throttle (REQ-STORE-20..25). We perform
	// a non-blocking pre-check so over-budget downloads surface 429
	// immediately rather than after a partial body has been written.
	// PrincipalFlagIgnoreDownloadLimits exempts service principals
	// (REQ-STORE-24).
	bucket := s.dlBucket(p.ID)
	if bucket != nil && !p.Flags.Has(store.PrincipalFlagIgnoreDownloadLimits) {
		ok, retryAfter := bucket.tryConsume(size)
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			WriteJMAPError(w, http.StatusTooManyRequests,
				"rateLimited",
				fmt.Sprintf("download bandwidth budget exhausted; retry after %s", retryAfter))
			return
		}
	}
	rc, err := s.store.Blobs().Get(r.Context(), blobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			WriteJMAPError(w, http.StatusNotFound, "blobNotFound", blobID)
			return
		}
		s.log.Warn("download.get_failed", "err", err, "blob", blobID)
		WriteJMAPError(w, http.StatusInternalServerError,
			"serverFail", "blob get failed")
		return
	}
	defer rc.Close()
	log := loggerFromContext(r.Context(), s.log)
	log.Info("download",
		"activity", "user",
		"principal_id", uint64(p.ID),
		"account_id", accountID,
		"blob_id", blobID,
		"size_bytes", size,
		"content_type", contentType,
	)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if name != "" {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`%s; filename=%q`, disposition, name))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		s.log.Warn("download.copy_failed", "err", err, "blob", blobID)
	}
}
