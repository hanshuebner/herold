package maildkim

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultSignedHeaders is the canonical RFC 6376 §5.4.1 header set Herold
// covers under DKIM by default. The set is conservative: every header that
// affects message identity is included, so re-signing or downstream
// modification surfaces as a verification failure. Headers absent from the
// message are skipped naturally by go-msgauth's signer (picker.Pick returns
// "" for missing keys), so the "(when present)" semantics requested by the
// implementor brief fall out for free.
var DefaultSignedHeaders = []string{
	"From",
	"To",
	"Cc",
	"Subject",
	"Date",
	"Message-ID",
	"MIME-Version",
	"Content-Type",
	"Content-Transfer-Encoding",
	"In-Reply-To",
	"References",
}

// ErrNoActiveKey indicates Sign was asked to sign for a domain that has no
// active DKIM key. Callers (the queue worker, the protosend submission
// path) translate this into a permanent failure so operators see the
// missing-key error and rotate one in.
var ErrNoActiveKey = errors.New("maildkim: no active DKIM key for domain")

// Signer signs RFC 5322 messages with the active DKIM key for a domain.
// Multiple goroutines may call Sign concurrently; the underlying
// keymgmt.Manager and the per-call go-msgauth signer are independent.
type Signer struct {
	keys    *keymgmt.Manager
	logger  *slog.Logger
	clock   clock.Clock
	headers []string
}

// SignerOption customises a Signer.
type SignerOption func(*Signer)

// WithSignedHeaders overrides DefaultSignedHeaders. The list MUST contain
// "From" — RFC 6376 §5.4 makes that mandatory. Pass nil to restore the
// default set.
func WithSignedHeaders(headers []string) SignerOption {
	return func(s *Signer) {
		if headers == nil {
			s.headers = DefaultSignedHeaders
			return
		}
		dup := make([]string, len(headers))
		copy(dup, headers)
		s.headers = dup
	}
}

// NewSigner returns a Signer that consults km for keys. logger and clk
// must not be nil.
func NewSigner(km *keymgmt.Manager, logger *slog.Logger, clk clock.Clock, opts ...SignerOption) *Signer {
	if km == nil {
		panic("maildkim: nil key manager")
	}
	if logger == nil {
		panic("maildkim: nil logger")
	}
	if clk == nil {
		panic("maildkim: nil clock")
	}
	s := &Signer{
		keys:    km,
		logger:  logger,
		clock:   clk,
		headers: DefaultSignedHeaders,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Sign returns a fresh byte slice containing message with a DKIM-Signature
// header prepended. Canonicalization is relaxed/relaxed; the body hash is
// SHA-256; signed headers default to DefaultSignedHeaders. Sign performs
// no I/O beyond the keymgmt lookup.
//
// Sign is the reference implementation used by the equivalence test suite.
// Production code calls SignStream instead to avoid buffering the full body
// in RAM (REQ-STORE-17). Do not delete Sign — it is the test oracle.
//
// A non-nil error is returned for missing keys (ErrNoActiveKey), key
// material that cannot be parsed, or signature failures from the
// underlying signer. Callers must treat this as a permanent classify
// signal: retrying without operator intervention will not help.
func (s *Signer) Sign(ctx context.Context, domain string, message []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(message) == 0 {
		return nil, errors.New("maildkim: empty message")
	}
	key, err := s.keys.ActiveKey(ctx, domain)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrNoActiveKey, domain)
		}
		return nil, fmt.Errorf("maildkim: lookup key: %w", err)
	}
	signer, err := keymgmt.LoadPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("maildkim: load key: %w", err)
	}

	opts := &dkim.SignOptions{
		Domain:                 key.Domain,
		Selector:               key.Selector,
		Signer:                 signer,
		Hash:                   crypto.SHA256,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys:             s.headers,
	}

	var out bytes.Buffer
	out.Grow(len(message) + 512)
	if err := dkim.Sign(&out, bytes.NewReader(message), opts); err != nil {
		return nil, fmt.Errorf("maildkim: sign: %w", err)
	}
	s.logger.DebugContext(ctx, "maildkim: signed message",
		slog.String("activity", "system"),
		slog.String("subsystem", "maildkim"),
		slog.String("domain", key.Domain),
		slog.String("selector", key.Selector),
		slog.String("algorithm", key.Algorithm.String()))
	return out.Bytes(), nil
}

// SignStream signs the message read from src and returns an io.Reader that
// emits the DKIM-Signature header(s) followed by the original message body
// without ever accumulating the full body in RAM (REQ-STORE-17).
//
// src must be an io.ReadSeeker (store.BlobReader satisfies this). SignStream
// reads src twice:
//
//   - Pass 1: the full message is streamed through go-msgauth's dkim.NewSigner
//     to compute the relaxed body hash and the header signature. The signer
//     is driven by an io.Copy; nothing beyond the signer's internal state is
//     held.
//   - After Close: src is seeked back to offset 0.
//   - Output: io.MultiReader(signatureHeader, src) so the caller streams the
//     signed message with a single subsequent io.Copy.
//
// The signature produced by SignStream is byte-identical to Sign for the same
// key and message on deterministic algorithms (RSA PKCS#1 v1.5, Ed25519).
// The equivalence test suite in signer_stream_test.go enforces this.
//
// Errors: same classification as Sign — ErrNoActiveKey for missing keys,
// permanent otherwise. Context cancellation routes to transient.
func (s *Signer) SignStream(ctx context.Context, domain string, src io.ReadSeeker) (io.Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	key, err := s.keys.ActiveKey(ctx, domain)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrNoActiveKey, domain)
		}
		return nil, fmt.Errorf("maildkim: lookup key: %w", err)
	}
	cryptoSigner, err := keymgmt.LoadPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("maildkim: load key: %w", err)
	}

	opts := &dkim.SignOptions{
		Domain:                 key.Domain,
		Selector:               key.Selector,
		Signer:                 cryptoSigner,
		Hash:                   crypto.SHA256,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys:             s.headers,
	}

	// Pass 1: stream src through go-msgauth's NewSigner to compute the body
	// hash and header signature. NewSigner runs a goroutine internally; we
	// drive it synchronously via io.Copy. No body bytes are retained.
	dkimSigner, err := dkim.NewSigner(opts)
	if err != nil {
		return nil, fmt.Errorf("maildkim: create signer: %w", err)
	}
	if _, err := io.Copy(dkimSigner, src); err != nil {
		_ = dkimSigner.Close()
		return nil, fmt.Errorf("maildkim: stream message for signing: %w", err)
	}
	if err := dkimSigner.Close(); err != nil {
		return nil, fmt.Errorf("maildkim: finalize signature: %w", err)
	}
	sigHeader := dkimSigner.Signature()

	// Seek the source back to the start so the caller's io.Copy reads the
	// full original message as the body portion of the signed output.
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("maildkim: seek body for output: %w", err)
	}

	s.logger.DebugContext(ctx, "maildkim: signed message (stream)",
		slog.String("activity", "system"),
		slog.String("subsystem", "maildkim"),
		slog.String("domain", key.Domain),
		slog.String("selector", key.Selector),
		slog.String("algorithm", key.Algorithm.String()))

	return io.MultiReader(strings.NewReader(sigHeader), src), nil
}
