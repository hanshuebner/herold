package maildkim

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"log/slog"

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
