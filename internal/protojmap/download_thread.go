package protojmap

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// CapabilityThreadExport advertises GET /jmap/download-thread
// (REQ-TEXP-10). The descriptor carries the downloadUrl template.
const CapabilityThreadExport CapabilityID = "https://netzhansa.com/jmap/thread-export"

// threadExportDownloadURL is the downloadUrl template advertised under
// CapabilityThreadExport, in the core downloadUrl variable syntax.
const threadExportDownloadURL = "/jmap/download-thread/{accountId}/{threadId}/{name}"

// threadExportCapability is the session descriptor body of
// CapabilityThreadExport.
type threadExportCapability struct {
	DownloadURL string `json:"downloadUrl"`
}

// handleDownloadThread serves a thread as a zip of .eml files
// (REQ-TEXP-10..13). Account resolution follows the JMAP method
// surface, so an ACL-shared account is addressable and filtered to the
// caller's visible mailboxes.
func (s *Server) handleDownloadThread(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		WriteJMAPError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	ctx := r.Context()
	accountID := r.PathValue("accountId")
	threadID := r.PathValue("threadId")
	ownerPID, merr := ResolveAccount(ctx, s.store.Meta(), p.ID, accountID)
	if merr != nil {
		if merr.Type == "serverFail" {
			s.log.Warn("download_thread.resolve_account_failed", "activity", "user", "err", merr.Description)
			WriteJMAPError(w, http.StatusInternalServerError, "serverFail", "account resolution failed")
			return
		}
		WriteJMAPError(w, http.StatusForbidden, "accountNotFound",
			"account is not accessible to this principal")
		return
	}
	exp, err := s.threadExporter.Resolve(ctx, p.ID, ownerPID, threadID)
	if err != nil {
		if errors.Is(err, ErrThreadNotFound) {
			WriteJMAPError(w, http.StatusNotFound, "threadNotFound", threadID)
			return
		}
		s.log.Warn("download_thread.resolve_failed", "activity", "user", "err", err, "thread", threadID)
		WriteJMAPError(w, http.StatusInternalServerError, "serverFail", "thread resolve failed")
		return
	}
	size := exp.TotalSize()
	// REQ-TEXP-13: the export draws on the per-principal download
	// budget (REQ-STORE-20..25) by its total message bytes.
	bucket := s.dlBucket(p.ID)
	if bucket != nil && !p.Flags.Has(store.PrincipalFlagIgnoreDownloadLimits) {
		ok, retryAfter := bucket.tryConsume(size)
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			WriteJMAPError(w, http.StatusTooManyRequests, "rateLimited",
				fmt.Sprintf("download bandwidth budget exhausted; retry after %s", retryAfter))
			return
		}
	}
	name := dispositionFilename(r.PathValue("name"), "application/zip")
	log := loggerFromContext(ctx, s.log)
	log.Info("download_thread",
		"activity", "user",
		"principal_id", uint64(p.ID),
		"account_id", accountID,
		"thread_id", threadID,
		"messages", len(exp.Messages),
		"size_bytes", size,
	)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", attachmentDisposition(name))
	w.WriteHeader(http.StatusOK)
	if err := s.threadExporter.WriteZip(ctx, w, exp, 0); err != nil {
		// Headers are on the wire; the client sees a truncated archive.
		log.Warn("download_thread.stream_failed", "activity", "user", "err", err, "thread", threadID)
	}
}

// attachmentDisposition renders an attachment Content-Disposition with
// an ASCII filename fallback and the RFC 5987 filename* parameter
// carrying the exact UTF-8 name.
func attachmentDisposition(name string) string {
	var ascii strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			ascii.WriteByte('_')
			continue
		}
		ascii.WriteRune(r)
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii.String(), rfc5987Encode(name))
}

// rfc5987Encode percent-encodes s per RFC 5987 §3.2.1 (attr-char is
// left as is, every other byte is %XX).
func rfc5987Encode(s string) string {
	const attrChar = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(attrChar, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
