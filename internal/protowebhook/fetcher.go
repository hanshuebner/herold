package protowebhook

import (
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// FetchPath is the URL path prefix the FetchHandler is mounted at. It
// is exported so admin operators wiring the handler into their HTTP
// listener can use a stable constant.
const FetchPath = "/webhook-fetch/"

// FetchServer serves the signed body URLs emitted in webhook payloads
// (REQ-HOOK-30/31). It validates the HMAC token, checks expiry, then
// streams the underlying blob bytes back to the receiver.
//
// The server is HTTP-only; callers terminate TLS upstream (the admin /
// send listeners already do).
type FetchServer struct {
	store      store.Store
	logger     *slog.Logger
	clock      clock.Clock
	signingKey []byte
}

// FetchOptions configures a FetchServer.
type FetchOptions struct {
	// Store is the metadata + blob surface; required.
	Store store.Store
	// Logger; nil falls back to slog.Default().
	Logger *slog.Logger
	// Clock; nil falls back to clock.NewReal().
	Clock clock.Clock
	// SigningKey MUST match the Dispatcher Options.SigningKey so
	// tokens minted by the dispatcher verify here.
	SigningKey []byte
}

// NewFetchServer constructs a FetchServer.
func NewFetchServer(opts FetchOptions) *FetchServer {
	s := &FetchServer{
		store:      opts.Store,
		logger:     opts.Logger,
		clock:      opts.Clock,
		signingKey: append([]byte(nil), opts.SigningKey...),
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.clock == nil {
		s.clock = clock.NewReal()
	}
	return s
}

// Handler returns the http.Handler that serves GET requests under
// FetchPath. Mount it at the operator's chosen base — typically the
// admin or send listener — so the URL prefix matches Options.FetchURLBaseURL.
func (s *FetchServer) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

// FetchHandler is a convenience alias matching the spec's naming.
func (s *FetchServer) FetchHandler() http.Handler { return s.Handler() }

func (s *FetchServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deliveryID := strings.TrimPrefix(r.URL.Path, FetchPath)
	if deliveryID == "" || strings.ContainsRune(deliveryID, '/') {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	blobHash := q.Get("blob")
	expStr := q.Get("exp")
	token := q.Get("token")
	if blobHash == "" || expStr == "" || token == "" {
		http.Error(w, "missing parameter", http.StatusBadRequest)
		return
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		http.Error(w, "bad expiry", http.StatusBadRequest)
		return
	}
	if s.clock.Now().Unix() > exp {
		http.Error(w, "expired", http.StatusForbidden)
		return
	}
	expected := fetchURLToken(deliveryID, blobHash, exp, s.signingKey)
	if !constantTimeHexEqual(expected, token) {
		http.Error(w, "bad signature", http.StatusForbidden)
		return
	}

	// Token verified. Stream the blob.
	rc, err := s.store.Blobs().Get(r.Context(), blobHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.logger.Warn("protowebhook: fetch blob",
			"delivery_id", deliveryID,
			"blob", blobHash,
			"err", err.Error())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, rc); err != nil {
		// Connection-side error after the header was written; nothing
		// to do but log.
		s.logger.Warn("protowebhook: fetch stream",
			"delivery_id", deliveryID,
			"err", err.Error())
	}
}

// constantTimeHexEqual decodes two hex-encoded values and compares them
// in constant time. Mismatched lengths return false without leaking
// length via the comparison.
func constantTimeHexEqual(a, b string) bool {
	ad, err := hex.DecodeString(a)
	if err != nil {
		return false
	}
	bd, err := hex.DecodeString(b)
	if err != nil {
		return false
	}
	if len(ad) != len(bd) {
		return false
	}
	return hmac.Equal(ad, bd)
}

// VerifyToken is exposed so tests and operator diag can verify a token
// without standing up an HTTP listener. Returns nil on a good token,
// a sentinel error on expiry / signature mismatch.
//
// (Not part of the wire contract; kept package-internal in spirit but
// exported so the test package can call it.)
func VerifyToken(signingKey []byte, deliveryID, blobHash string, expUnix int64, token string, now time.Time) error {
	if now.Unix() > expUnix {
		return errExpired
	}
	expected := fetchURLToken(deliveryID, blobHash, expUnix, signingKey)
	if !constantTimeHexEqual(expected, token) {
		return errBadSignature
	}
	return nil
}

// Sentinel errors for VerifyToken.
var (
	errExpired      = errors.New("protowebhook: token expired")
	errBadSignature = errors.New("protowebhook: bad signature")
)
