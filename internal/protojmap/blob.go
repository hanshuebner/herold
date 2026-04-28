package protojmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

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
		s.log.Warn("protojmap.upload.put_failed", "err", err)
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
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
	pid, ok := principalIDFromAccountID(accountID)
	if !ok || pid != p.ID {
		WriteJMAPError(w, http.StatusForbidden, "accountNotFound",
			"account does not match authenticated principal")
		return
	}
	blobID := r.PathValue("blobId")
	contentType := r.PathValue("type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	name := r.PathValue("name")
	size, _, err := s.store.Blobs().Stat(r.Context(), blobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			WriteJMAPError(w, http.StatusNotFound, "blobNotFound", blobID)
			return
		}
		s.log.Warn("protojmap.download.stat_failed", "err", err, "blob", blobID)
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
		s.log.Warn("protojmap.download.get_failed", "err", err, "blob", blobID)
		WriteJMAPError(w, http.StatusInternalServerError,
			"serverFail", "blob get failed")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if name != "" {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename=%q`, name))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		s.log.Warn("protojmap.download.copy_failed", "err", err, "blob", blobID)
	}
}
