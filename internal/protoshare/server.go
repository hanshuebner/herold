package protoshare

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// ShareStore is the narrow store surface protoshare needs. The caller
// supplies a concrete store.Store and the server calls Meta() and Blobs()
// as required; this interface exists to make tests easy to write without
// the full store.
type ShareStore interface {
	// GetFileShareByID returns the row regardless of state.
	GetFileShareByID(ctx context.Context, id string) (store.FileShare, error)
	// RecordFileShareDownload atomically increments download_count and
	// returns the post-increment row.
	RecordFileShareDownload(ctx context.Context, id string) (store.FileShare, error)
	// BlobGet opens the blob for seekable, range-capable read (REQ-STORE-14).
	BlobGet(ctx context.Context, hash string) (store.BlobReader, error)
	// BlobStat returns (size, refs, err) for a hash; used to fill
	// Content-Length on HEAD without opening the blob.
	BlobStat(ctx context.Context, hash string) (size int64, refs int, err error)
}

// storeAdapter wraps a store.Store and implements ShareStore.
type storeAdapter struct{ s store.Store }

func (a storeAdapter) GetFileShareByID(ctx context.Context, id string) (store.FileShare, error) {
	return a.s.Meta().GetFileShareByID(ctx, id)
}
func (a storeAdapter) RecordFileShareDownload(ctx context.Context, id string) (store.FileShare, error) {
	return a.s.Meta().RecordFileShareDownload(ctx, id)
}
func (a storeAdapter) BlobGet(ctx context.Context, hash string) (store.BlobReader, error) {
	return a.s.Blobs().Get(ctx, hash)
}
func (a storeAdapter) BlobStat(ctx context.Context, hash string) (int64, int, error) {
	return a.s.Blobs().Stat(ctx, hash)
}

// StoreAdapter returns a ShareStore backed by s. Callers that hold a
// store.Store pass it through this adapter when constructing a Server.
func StoreAdapter(s store.Store) ShareStore { return storeAdapter{s: s} }

// RateLimitConfig carries the per-(IP, share) rate-limit parameters.
type RateLimitConfig struct {
	// RequestsPerWindow is the maximum number of requests allowed per
	// (source-IP, share-id) tuple within Window. Defaults to 50.
	RequestsPerWindow int
	// Window is the sliding-window duration. Defaults to 10 minutes.
	Window time.Duration
}

// Options configures a Server.
type Options struct {
	// Store is the narrow store surface. Required.
	Store ShareStore

	// SigningKey is the HMAC-SHA256 key used to sign and verify the
	// unlock cookie. Must be at least 32 bytes. Required.
	SigningKey []byte

	// RateLimit configures per-(IP,share) request rate limiting. Zero
	// value uses the defaults from REQ-SHARE-32 (50 req / 10 min).
	RateLimit RateLimitConfig

	// Clock; nil falls back to clock.NewReal().
	Clock clock.Clock

	// Logger; nil falls back to slog.Default().
	Logger *slog.Logger
}

// Server handles the three public share routes.
type Server struct {
	store      ShareStore
	signingKey []byte
	rl         *rateLimiter
	clock      clock.Clock
	logger     *slog.Logger
	landingTpl *template.Template
}

// New constructs a Server. Panics if required fields are missing.
func New(opts Options) *Server {
	if opts.Store == nil {
		panic("protoshare.New: Store is required")
	}
	if len(opts.SigningKey) < 32 {
		panic("protoshare.New: SigningKey must be >= 32 bytes")
	}

	rl := opts.RateLimit
	if rl.RequestsPerWindow <= 0 {
		rl.RequestsPerWindow = 50
	}
	if rl.Window <= 0 {
		rl.Window = 10 * time.Minute
	}

	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	lg := opts.Logger
	if lg == nil {
		lg = slog.Default()
	}

	tpl := template.Must(template.New("landing").Parse(landingHTML))

	return &Server{
		store:      opts.Store,
		signingKey: append([]byte(nil), opts.SigningKey...),
		rl:         newRateLimiter(clk, rl.RequestsPerWindow, rl.Window),
		clock:      clk,
		logger:     lg,
		landingTpl: tpl,
	}
}

// NewFromStore is a convenience constructor for callers that hold a
// store.Store rather than a pre-wrapped ShareStore.
func NewFromStore(s store.Store, opts Options) *Server {
	opts.Store = StoreAdapter(s)
	return New(opts)
}

// RegisterRoutes mounts the three public share routes on mux. The mux is
// a plain *http.ServeMux; no chi dependency is required. The routes are:
//
//	GET  /share/{id}
//	GET  /share/{id}/download
//	POST /share/{id}/unlock
func (srv *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /share/{id}", http.HandlerFunc(srv.handleLanding))
	mux.Handle("GET /share/{id}/download", http.HandlerFunc(srv.handleDownload))
	mux.Handle("HEAD /share/{id}/download", http.HandlerFunc(srv.handleDownload))
	mux.Handle("POST /share/{id}/unlock", http.HandlerFunc(srv.handleUnlock))
}

// ---- helpers ----------------------------------------------------------------

// logID returns a safe-to-log truncated share ID (first 8 chars of a
// base64url token, or its hex-prefix). REQ-SHARE-11d.
func logID(id string) string {
	if len(id) > 8 {
		return id[:8] + "..."
	}
	return id
}

// sourceIP extracts the peer IP from r.RemoteAddr; falls back to the full
// RemoteAddr on parse error. X-Forwarded-For is intentionally not consulted
// here — the public listener should be behind a trusted TLS terminator; the
// operator can extract a better IP at the mux level if needed.
func sourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// gone410 writes the uniform 410 Gone response required by REQ-SHARE-11e.
// All not-serveable cases (expired, revoked, exhausted, pending,
// never-existed, wrong/missing password) use this — no distinguishing body.
func gone410(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusGone)
	_, _ = io.WriteString(w, "410 Gone\n")
}

// isServeable returns true iff the share passes all the stateless checks:
// state==active, not expired, and not download-exhausted. Password
// checking is handled separately by the handlers (cookie-based).
func isServeable(fs store.FileShare, now time.Time) bool {
	if fs.State != store.FileShareStateActive {
		return false
	}
	if now.After(fs.ExpiresAt) {
		return false
	}
	if fs.MaxDownloads != nil && fs.DownloadCount >= *fs.MaxDownloads {
		return false
	}
	return true
}

// unlockCookieName returns the per-share cookie name. The name is
// deterministic and scoped to the share ID so different shares do not
// interfere.
func unlockCookieName(shareID string) string {
	// Use a short HMAC prefix of the share ID as a stable cookie name so
	// the cookie name itself doesn't leak the full ID. Cookie names must
	// be valid HTTP token characters; base64url is fine.
	h := hmac.New(sha256.New, []byte("unlock-cookie-name"))
	h.Write([]byte(shareID))
	return "share_unlock_" + hex.EncodeToString(h.Sum(nil))[:12]
}

// cookieTTL is the lifetime of the unlock cookie.
const cookieTTL = 15 * time.Minute

// mintUnlockCookie returns a short-lived HMAC-signed cookie value for the
// given share ID. Format: "<expires-unix>.<hmac-hex>". REQ-SHARE-30.
func (srv *Server) mintUnlockCookie(shareID string, now time.Time) string {
	exp := now.Add(cookieTTL).Unix()
	msg := fmt.Sprintf("%s:%d", shareID, exp)
	mac := hmac.New(sha256.New, srv.signingKey)
	mac.Write([]byte(msg))
	return fmt.Sprintf("%d.%s", exp, hex.EncodeToString(mac.Sum(nil)))
}

// verifyUnlockCookie checks whether the cookie value is a valid, unexpired
// unlock token for the given share ID. Constant-time MAC comparison.
func (srv *Server) verifyUnlockCookie(shareID, cookieVal string, now time.Time) bool {
	dot := strings.LastIndex(cookieVal, ".")
	if dot < 0 {
		return false
	}
	expStr := cookieVal[:dot]
	macHex := cookieVal[dot+1:]
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return false
	}
	if now.Unix() > exp {
		return false
	}
	msg := fmt.Sprintf("%s:%d", shareID, exp)
	expected := hmac.New(sha256.New, srv.signingKey)
	expected.Write([]byte(msg))
	got, err := hex.DecodeString(macHex)
	if err != nil {
		return false
	}
	return hmac.Equal(expected.Sum(nil), got)
}

// hasUnlockCookie reports whether the request carries a valid unlock cookie
// for the share.
func (srv *Server) hasUnlockCookie(r *http.Request, shareID string) bool {
	c, err := r.Cookie(unlockCookieName(shareID))
	if err != nil {
		return false
	}
	return srv.verifyUnlockCookie(shareID, c.Value, srv.clock.Now())
}

// isPasswordProtected reports whether the share requires a password.
func isPasswordProtected(fs store.FileShare) bool {
	return fs.PasswordHash != ""
}

// safeContentType returns the MIME type to serve, overriding HTML and SVG
// to application/octet-stream so a share URL cannot host script on the
// server's origin (REQ-SHARE-63).
func safeContentType(stored string) string {
	base, _, _ := mime.ParseMediaType(stored)
	switch strings.ToLower(base) {
	case "text/html", "application/xhtml+xml", "image/svg+xml":
		return "application/octet-stream"
	}
	return stored
}

// rfc5987Encode encodes a filename according to RFC 5987 (UTF-8, percent-
// encoded). Only US-ASCII unreserved characters and the set allowed in a
// token are left unescaped; everything else is percent-encoded.
func rfc5987Encode(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != utf8.RuneError && strings.ContainsRune(unreserved, r) && r < 0x80 {
			b.WriteRune(r)
		} else {
			// Percent-encode each byte of the UTF-8 representation.
			for _, byt := range []byte(s[i : i+size]) {
				fmt.Fprintf(&b, "%%%02X", byt)
			}
		}
		i += size
	}
	return b.String()
}

// humanSize formats bytes as a human-readable string (B / KiB / MiB / GiB).
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1024*1024*1024))
	}
}

// ---- rate-limit helper ------------------------------------------------------

// checkRateLimit applies the per-(IP, share) rate limit and writes a 429
// response if exceeded. Returns true when the request is allowed.
func (srv *Server) checkRateLimit(w http.ResponseWriter, r *http.Request, shareID string) bool {
	key := sourceIP(r) + "|" + shareID
	ok, retryAfter := srv.rl.allow(key)
	if ok {
		return true
	}
	secs := int64(math.Ceil(retryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = io.WriteString(w, "429 Too Many Requests\n")
	srv.logger.Debug("protoshare: rate limited",
		slog.String("activity", observe.ActivityAccess),
		slog.String("share_id_prefix", logID(shareID)),
		slog.String("source_ip", sourceIP(r)),
	)
	return false
}

// ---- handlers ---------------------------------------------------------------

// handleLanding serves the minimal HTML landing page (REQ-SHARE-30).
func (srv *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	shareID := r.PathValue("id")
	if !srv.checkRateLimit(w, r, shareID) {
		return
	}

	fs, err := srv.store.GetFileShareByID(r.Context(), shareID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			gone410(w)
			return
		}
		srv.logger.Warn("protoshare: GetFileShareByID",
			slog.String("activity", observe.ActivitySystem),
			slog.String("share_id_prefix", logID(shareID)),
			slog.String("err", err.Error()),
		)
		gone410(w)
		return
	}

	now := srv.clock.Now()
	if !isServeable(fs, now) {
		gone410(w)
		return
	}

	locked := isPasswordProtected(fs) && !srv.hasUnlockCookie(r, shareID)

	var remaining *int64
	if fs.MaxDownloads != nil {
		rem := *fs.MaxDownloads - fs.DownloadCount
		if rem < 0 {
			rem = 0
		}
		remaining = &rem
	}

	data := landingData{
		Filename:  fs.Filename,
		Size:      humanSize(fs.BlobSize),
		ExpiresAt: fs.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"),
		Remaining: remaining,
		Locked:    locked,
		ShareID:   shareID,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := srv.landingTpl.Execute(w, data); err != nil {
		srv.logger.Warn("protoshare: template execute",
			slog.String("activity", observe.ActivitySystem),
			slog.String("share_id_prefix", logID(shareID)),
			slog.String("err", err.Error()),
		)
	}
}

// handleUnlock verifies the form-posted password, mints an unlock cookie,
// and redirects back to the landing page (REQ-SHARE-30).
func (srv *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	shareID := r.PathValue("id")
	if !srv.checkRateLimit(w, r, shareID) {
		return
	}

	fs, err := srv.store.GetFileShareByID(r.Context(), shareID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			gone410(w)
			return
		}
		srv.logger.Warn("protoshare: unlock GetFileShareByID",
			slog.String("activity", observe.ActivitySystem),
			slog.String("share_id_prefix", logID(shareID)),
			slog.String("err", err.Error()),
		)
		gone410(w)
		return
	}

	now := srv.clock.Now()
	if !isServeable(fs, now) {
		gone410(w)
		return
	}

	if err := r.ParseForm(); err != nil {
		gone410(w)
		return
	}
	password := r.FormValue("password")

	// REQ-SHARE-11c: wrong or missing password yields the same response as
	// a missing share. We still do the verify to keep the Argon2id timing
	// visible (constant-time outcome).
	if !store.VerifySharePassword(fs.PasswordHash, password) {
		gone410(w)
		return
	}

	// Password correct: mint a short-lived, HMAC-signed cookie.
	cookieVal := srv.mintUnlockCookie(shareID, now)
	http.SetCookie(w, &http.Cookie{
		Name:     unlockCookieName(shareID),
		Value:    cookieVal,
		Path:     "/share/" + shareID,
		MaxAge:   int(cookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/share/"+shareID, http.StatusSeeOther)
}

// handleDownload streams the blob bytes (REQ-SHARE-30, REQ-SHARE-31).
// HEAD is supported and never increments the download counter.
// Range requests are honoured (REQ-SHARE-31).
func (srv *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	shareID := r.PathValue("id")
	if !srv.checkRateLimit(w, r, shareID) {
		return
	}

	fs, err := srv.store.GetFileShareByID(r.Context(), shareID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			gone410(w)
			return
		}
		srv.logger.Warn("protoshare: download GetFileShareByID",
			slog.String("activity", observe.ActivitySystem),
			slog.String("share_id_prefix", logID(shareID)),
			slog.String("err", err.Error()),
		)
		gone410(w)
		return
	}

	now := srv.clock.Now()
	if !isServeable(fs, now) {
		gone410(w)
		return
	}

	// Password gate.
	if isPasswordProtected(fs) && !srv.hasUnlockCookie(r, shareID) {
		gone410(w)
		return
	}

	// HEAD: send headers only, never increment.
	if r.Method == http.MethodHead {
		srv.writeDownloadHeaders(w, fs)
		return
	}

	// GET: increment the download counter before streaming. We commit the
	// increment first so max_downloads cannot be raced (REQ-SHARE-30).
	// Only the first range request of a transfer should increment. We
	// detect "this is a continuation" by the presence of a Range header
	// that does NOT start at byte 0 — a first-range request may carry
	// Range: bytes=0-N, which is treated as a fresh download.
	isRangeContinuation := false
	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		isRangeContinuation = !isFirstRange(rangeHdr)
	}

	if !isRangeContinuation {
		post, err := srv.store.RecordFileShareDownload(r.Context(), shareID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				gone410(w)
				return
			}
			srv.logger.Warn("protoshare: RecordFileShareDownload",
				slog.String("activity", observe.ActivitySystem),
				slog.String("share_id_prefix", logID(shareID)),
				slog.String("err", err.Error()),
			)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Re-check max_downloads with the post-increment count so the
		// loser in a concurrent race gets 410 (REQ-SHARE-30, arch §Failure
		// and edge behaviour).
		if post.MaxDownloads != nil && post.DownloadCount > *post.MaxDownloads {
			gone410(w)
			return
		}
	}

	// Stream bytes.
	rc, err := srv.store.BlobGet(r.Context(), fs.BlobHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Blob vanished under a live share: 500, not 410 (architecture
			// §Failure and edge behaviour).
			srv.logger.Error("protoshare: blob missing for active share",
				slog.String("activity", observe.ActivitySystem),
				slog.String("share_id_prefix", logID(shareID)),
				slog.String("blob_hash", fs.BlobHash),
			)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		srv.logger.Error("protoshare: BlobGet",
			slog.String("activity", observe.ActivitySystem),
			slog.String("share_id_prefix", logID(shareID)),
			slog.String("err", err.Error()),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	srv.writeDownloadHeaders(w, fs)

	// Honour range requests using http.ServeContent. The BlobReader is
	// seekable (REQ-STORE-14) so ServeContent can satisfy any Range
	// without buffering the blob into memory.
	if r.Header.Get("Range") != "" {
		// ServeContent sets Content-Range and 206 for us.
		http.ServeContent(w, r, "", time.Time{}, rc)
		return
	}
	if _, err := io.Copy(w, rc); err != nil {
		srv.logger.Debug("protoshare: stream copy",
			slog.String("activity", observe.ActivityAccess),
			slog.String("share_id_prefix", logID(shareID)),
			slog.String("err", err.Error()),
		)
	}
}

// writeDownloadHeaders sets all required download response headers.
// REQ-SHARE-11d, REQ-SHARE-30, REQ-SHARE-31, REQ-SHARE-63.
func (srv *Server) writeDownloadHeaders(w http.ResponseWriter, fs store.FileShare) {
	ct := safeContentType(fs.ContentType)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(fs.BlobSize, 10))
	// RFC 5987 encoded filename.
	encoded := rfc5987Encode(fs.Filename)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+encoded)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Accept-Ranges", "bytes")
}

// isFirstRange reports whether a Range header value represents a request
// starting at byte 0 (i.e., the first chunk of a download, not a
// continuation). Returns false for any range not starting at 0.
func isFirstRange(rangeHdr string) bool {
	// Range: bytes=<start>-<end> or bytes=<start>-
	// We only count it as the first range if start == 0.
	rangeHdr = strings.TrimSpace(rangeHdr)
	if !strings.HasPrefix(rangeHdr, "bytes=") {
		return false
	}
	spec := strings.TrimPrefix(rangeHdr, "bytes=")
	// Multiple ranges: "bytes=0-499,500-999" — treat as first if first
	// range starts at 0.
	parts := strings.SplitN(spec, ",", 2)
	first := strings.TrimSpace(parts[0])
	dash := strings.Index(first, "-")
	if dash < 0 {
		return false
	}
	startStr := first[:dash]
	if startStr == "0" || startStr == "" {
		return true
	}
	n, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return false
	}
	return n == 0
}

// ---- landing page template --------------------------------------------------

type landingData struct {
	Filename  string
	Size      string
	ExpiresAt string
	Remaining *int64 // nil means unlimited
	Locked    bool
	ShareID   string
}

// landingHTML is the minimal, dependency-free landing page template.
// No principal identity, no other share, no server internals are exposed
// (REQ-SHARE-30). Plain HTML; no CSS framework.
const landingHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>File Share</title>
<style>
body{font-family:sans-serif;max-width:480px;margin:60px auto;padding:0 16px}
h1{font-size:1.2rem;word-break:break-all}
dl{margin:1em 0}dt{font-weight:bold}dd{margin:0 0 .5em 0}
form{margin:1em 0}
input[type=password]{display:block;width:100%;padding:.4em;font-size:1rem;box-sizing:border-box}
button,a.btn{display:inline-block;padding:.5em 1.2em;font-size:1rem;cursor:pointer;text-decoration:none;background:#0066cc;color:#fff;border:none;border-radius:4px}
button:hover,a.btn:hover{background:#0052a3}
.notice{color:#666;font-size:.9rem;margin-top:1.5em}
</style>
</head>
<body>
<h1>{{.Filename}}</h1>
<dl>
<dt>Size</dt><dd>{{.Size}}</dd>
<dt>Expires</dt><dd>{{.ExpiresAt}}</dd>
{{if .Remaining}}<dt>Downloads remaining</dt><dd>{{.Remaining}}</dd>{{end}}
</dl>
{{if .Locked}}
<form method="POST" action="/share/{{.ShareID}}/unlock">
<label for="password">Password required</label><br>
<input type="password" id="password" name="password" required autofocus>
<br><br>
<button type="submit">Unlock</button>
</form>
{{else}}
<a class="btn" href="/share/{{.ShareID}}/download">Download</a>
{{end}}
<p class="notice">This link may expire or be revoked at any time.</p>
</body>
</html>
`
