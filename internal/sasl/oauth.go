package sasl

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// NewOAUTHBEARER constructs a server-side OAUTHBEARER mechanism
// (RFC 7628). The client sends a single message:
//
//	gs2-header ^A auth=Bearer <token> ^A ^A
//
// The server verifies the token via tv and emits either a success
// (empty) or a JSON error line as a challenge. Per RFC 7628 §3.2.3 the
// client follows a failure with a single 0x01 byte to which the server
// replies with a terminating protocol error; we implement that two-step
// flow.
func NewOAUTHBEARER(tv TokenVerifier) Mechanism { return &oauthMech{tv: tv, flavor: flavorOAUTHBEARER} }

// NewXOAUTH2 constructs a server-side XOAUTH2 mechanism. The client
// message layout is:
//
//	user=<email> ^A auth=Bearer <token> ^A ^A
//
// Otherwise the semantics match OAUTHBEARER.
func NewXOAUTH2(tv TokenVerifier) Mechanism { return &oauthMech{tv: tv, flavor: flavorXOAUTH2} }

type oauthFlavor int

const (
	flavorOAUTHBEARER oauthFlavor = iota
	flavorXOAUTH2
)

type oauthMech struct {
	tv     TokenVerifier
	flavor oauthFlavor

	state int // 0=init, 1=awaiting client FAIL ack, 2=done ok
	pid   PrincipalID
}

func (m *oauthMech) Name() string {
	if m.flavor == flavorXOAUTH2 {
		return "XOAUTH2"
	}
	return "OAUTHBEARER"
}

func (m *oauthMech) Principal() (PrincipalID, error) {
	if m.state != 2 {
		return 0, ErrAuthFailed
	}
	return m.pid, nil
}

func (m *oauthMech) Start(ctx context.Context, ir []byte) ([]byte, bool, error) {
	if m.state != 0 {
		return nil, false, ErrProtocolError
	}
	if !tlsPresent(ctx) {
		return nil, false, ErrTLSRequired
	}
	if len(ir) == 0 {
		// Empty IR is not valid for these mechanisms; request a
		// continuation to give the client a chance to supply one.
		return []byte{}, false, nil
	}
	return m.consume(ctx, ir)
}

func (m *oauthMech) Next(ctx context.Context, resp []byte) ([]byte, bool, error) {
	switch m.state {
	case 0:
		return m.consume(ctx, resp)
	case 1:
		// Per RFC 7628 the client acknowledges a failure challenge
		// with a single 0x01 byte; any other content is a protocol
		// error. Either way we terminate with ErrAuthFailed.
		return nil, true, ErrAuthFailed
	default:
		return nil, false, ErrProtocolError
	}
}

func (m *oauthMech) consume(ctx context.Context, msg []byte) ([]byte, bool, error) {
	parsed, err := parseOAUTHMessage(msg, m.flavor)
	if err != nil {
		return nil, false, err
	}
	// providerHint sourcing per flavour:
	//   OAUTHBEARER (RFC 7628 §3.1) advertises host=<hostname> alongside
	//   auth=Bearer <token>; we use that as the provider hint.
	//   XOAUTH2 has no host= field in the standard layout, so callers
	//   that want strict provider routing should expose XOAUTH2 only
	//   when a single provider is configured. We pass through whatever
	//   the message carried (host=) if present, otherwise empty.
	pid, verr := m.tv.VerifyAccessToken(ctx, parsed.host, parsed.token)
	if verr != nil {
		// Emit a JSON challenge per RFC 7628 §3.2.2 for OAUTHBEARER;
		// XOAUTH2 servers typically emit a base64 JSON too but the
		// exact shape is Google-specific. Both flavours tolerate an
		// empty challenge followed by a failure terminator.
		m.state = 1
		return []byte(`{"status":"invalid_token"}`), false, nil
	}
	m.pid = pid
	m.state = 2
	return nil, true, nil
}

// oauthMessage holds the parsed fields of an OAUTHBEARER / XOAUTH2
// client message.
type oauthMessage struct {
	token string
	host  string // RFC 7628 host= (provider hint); empty when absent
}

// parseOAUTHMessage parses the client's message per the flavour and
// returns the bearer token plus the host advertisement (used as a
// provider hint by the verifier).
func parseOAUTHMessage(msg []byte, flavor oauthFlavor) (oauthMessage, error) {
	if len(msg) > 16*1024 {
		return oauthMessage{}, fmt.Errorf("OAUTH: message too large: %w", ErrInvalidMessage)
	}
	// Fields are ^A-separated. Trailing empty fields are expected.
	fields := bytes.Split(msg, []byte{0x01})
	var (
		authField string
		host      string
	)
	for _, f := range fields {
		switch {
		case bytes.HasPrefix(f, []byte("auth=")):
			authField = string(f[len("auth="):])
		case bytes.HasPrefix(f, []byte("host=")):
			host = string(f[len("host="):])
		}
	}
	if authField == "" {
		return oauthMessage{}, fmt.Errorf("OAUTH: missing auth= field: %w", ErrInvalidMessage)
	}
	// auth=Bearer <token> (case-insensitive scheme per RFC 6750 §2.1)
	parts := strings.SplitN(authField, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return oauthMessage{}, fmt.Errorf("OAUTH: bad auth scheme: %w", ErrInvalidMessage)
	}
	_ = flavor
	return oauthMessage{
		token: strings.TrimSpace(parts[1]),
		host:  host,
	}, nil
}
