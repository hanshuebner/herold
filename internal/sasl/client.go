package sasl

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// ClientMechanism is the per-session client-side SASL state machine
// used by the SMTP submission path when authenticating to a smart-host
// upstream. Callers:
//
//  1. Build a mechanism via the appropriate NewClient* constructor.
//  2. Call Start once. If ok=true the exchange completes after the
//     server's 235 (no further challenges); the returned bytes are the
//     base64-encoded initial-response to send alongside the AUTH verb
//     (RFC 4954 §4 initial-response form).
//  3. If ok=false, send a base64-encoded empty response (or omit the
//     IR) and feed each base64-decoded server challenge into Next
//     until ok=true.
//
// Implementations are NOT safe for concurrent use by multiple
// goroutines; one mechanism instance corresponds to one AUTH exchange.
type ClientMechanism interface {
	// Name returns the wire-protocol name (e.g. "PLAIN").
	Name() string
	// Start returns the optional initial-response bytes for AUTH
	// <name> [ir]. ok=true means no further server challenges are
	// expected (PLAIN, XOAUTH2). ok=false means a continuation flow
	// follows (LOGIN, SCRAM).
	Start() (ir []byte, ok bool, err error)
	// Next consumes a base64-decoded server challenge and returns the
	// next client response. ok=true when the exchange has completed
	// successfully (the caller still expects a 235 final reply on the
	// wire, but no further client message is required).
	Next(challenge []byte) (resp []byte, ok bool, err error)
}

// ErrClientProtocol is returned when the client mechanism is driven
// out of sequence (Next before Start, Next after completion, etc.) or
// when the server's challenge is structurally invalid.
var ErrClientProtocol = errors.New("sasl: client mechanism protocol error")

// NewClientPLAIN constructs a SASL PLAIN client (RFC 4616). The
// authzid is typically empty; if non-empty it is sent verbatim as the
// authzid field. Plain-text: callers MUST drive this only over a
// TLS-protected connection.
func NewClientPLAIN(authzid, username, password string) ClientMechanism {
	return &clientPlain{
		authzid:  authzid,
		username: username,
		password: password,
	}
}

type clientPlain struct {
	authzid, username, password string
	started, done               bool
}

func (c *clientPlain) Name() string { return "PLAIN" }

func (c *clientPlain) Start() ([]byte, bool, error) {
	if c.started {
		return nil, false, ErrClientProtocol
	}
	c.started = true
	c.done = true
	// RFC 4616: authzid \0 authcid \0 passwd
	var b bytes.Buffer
	b.WriteString(c.authzid)
	b.WriteByte(0)
	b.WriteString(c.username)
	b.WriteByte(0)
	b.WriteString(c.password)
	return b.Bytes(), true, nil
}

func (c *clientPlain) Next(_ []byte) ([]byte, bool, error) {
	if !c.started || c.done {
		return nil, false, ErrClientProtocol
	}
	return nil, false, ErrClientProtocol
}

// NewClientLOGIN constructs a SASL LOGIN client. LOGIN is not in any
// RFC but is widely deployed: the server sends "Username:" then
// "Password:" base64-encoded; the client returns each field. Plain-text:
// TLS required at the call site.
func NewClientLOGIN(username, password string) ClientMechanism {
	return &clientLogin{username: username, password: password}
}

type clientLogin struct {
	username, password string
	state              int // 0=init, 1=sent username, 2=done
}

func (c *clientLogin) Name() string { return "LOGIN" }

func (c *clientLogin) Start() ([]byte, bool, error) {
	if c.state != 0 {
		return nil, false, ErrClientProtocol
	}
	// LOGIN does not use the AUTH initial-response: the client waits
	// for the server's "Username:" prompt. Returning a nil IR with
	// ok=false signals the caller to send "AUTH LOGIN" with no IR.
	return nil, false, nil
}

func (c *clientLogin) Next(challenge []byte) ([]byte, bool, error) {
	switch c.state {
	case 0:
		// First challenge: typically "Username:". We do not sniff the
		// content because some servers vary the prompt; the protocol
		// is positional.
		c.state = 1
		return []byte(c.username), false, nil
	case 1:
		c.state = 2
		return []byte(c.password), true, nil
	default:
		return nil, false, ErrClientProtocol
	}
}

// NewClientXOAUTH2 constructs a SASL XOAUTH2 client (Google's
// non-standard OAuth-bearer flavour). The wire message is:
//
//	user=<email> ^A auth=Bearer <token> ^A ^A
//
// One-shot: ok=true after Start.
func NewClientXOAUTH2(username, accessToken string) ClientMechanism {
	return &clientXOAuth2{username: username, token: accessToken}
}

type clientXOAuth2 struct {
	username, token string
	started, done   bool
}

func (c *clientXOAuth2) Name() string { return "XOAUTH2" }

func (c *clientXOAuth2) Start() ([]byte, bool, error) {
	if c.started {
		return nil, false, ErrClientProtocol
	}
	c.started = true
	c.done = true
	var b bytes.Buffer
	b.WriteString("user=")
	b.WriteString(c.username)
	b.WriteByte(0x01)
	b.WriteString("auth=Bearer ")
	b.WriteString(c.token)
	b.WriteByte(0x01)
	b.WriteByte(0x01)
	return b.Bytes(), true, nil
}

func (c *clientXOAuth2) Next(challenge []byte) ([]byte, bool, error) {
	if !c.started {
		return nil, false, ErrClientProtocol
	}
	// On token rejection the server emits a base64 JSON failure
	// challenge, to which the client responds with a single empty
	// line; the upper layer will then see the 535 reply. We do not
	// inspect the JSON content here; the calling code reads the SMTP
	// reply that follows and surfaces the auth failure.
	if c.done {
		return []byte{}, false, nil
	}
	return nil, false, ErrClientProtocol
}

// NewClientSCRAMSHA256 constructs a SASL SCRAM-SHA-256 client (RFC
// 5802). When channelBinding is nil the mechanism is the bare variant
// (gs2-cbind-flag = "n"); when non-nil it is the -PLUS variant with
// tls-server-end-point binding. Tests typically pass nil.
func NewClientSCRAMSHA256(username, password string, channelBinding []byte) ClientMechanism {
	return &clientScram{
		username:       username,
		password:       password,
		channelBinding: channelBinding,
	}
}

type clientScram struct {
	username, password string
	channelBinding     []byte

	state int // 0=init, 1=sent first, 2=sent final, 3=done
	// Persisted across Start -> Next exchanges.
	clientNonce string
	gs2Header   string
	clientFirst string // bare form
	authMessage string
	saltedPass  []byte
}

func (c *clientScram) Name() string {
	if c.channelBinding != nil {
		return "SCRAM-SHA-256-PLUS"
	}
	return "SCRAM-SHA-256"
}

func (c *clientScram) Start() ([]byte, bool, error) {
	if c.state != 0 {
		return nil, false, ErrClientProtocol
	}
	nonce, err := randomNonce(18)
	if err != nil {
		return nil, false, fmt.Errorf("scram: nonce: %w", err)
	}
	c.clientNonce = nonce
	if c.channelBinding != nil {
		c.gs2Header = "p=tls-server-end-point,,"
	} else {
		c.gs2Header = "n,,"
	}
	c.clientFirst = "n=" + saslnameEncode(c.username) + ",r=" + nonce
	c.state = 1
	return []byte(c.gs2Header + c.clientFirst), false, nil
}

func (c *clientScram) Next(challenge []byte) ([]byte, bool, error) {
	switch c.state {
	case 1:
		// server-first: r=<combined>,s=<base64 salt>,i=<iter>
		attrs, err := parseSCRAMAttrs(string(challenge))
		if err != nil {
			return nil, false, fmt.Errorf("scram: server-first: %w", err)
		}
		combinedNonce, ok := attrs["r"]
		if !ok || !strings.HasPrefix(combinedNonce, c.clientNonce) || len(combinedNonce) <= len(c.clientNonce) {
			return nil, false, fmt.Errorf("scram: bad server nonce: %w", ErrClientProtocol)
		}
		saltB64, ok := attrs["s"]
		if !ok {
			return nil, false, fmt.Errorf("scram: missing salt: %w", ErrClientProtocol)
		}
		salt, err := base64.StdEncoding.DecodeString(saltB64)
		if err != nil {
			return nil, false, fmt.Errorf("scram: salt b64: %w", ErrClientProtocol)
		}
		iterStr, ok := attrs["i"]
		if !ok {
			return nil, false, fmt.Errorf("scram: missing iter: %w", ErrClientProtocol)
		}
		var iter int
		if _, err := fmt.Sscanf(iterStr, "%d", &iter); err != nil || iter < 1 {
			return nil, false, fmt.Errorf("scram: bad iter %q: %w", iterStr, ErrClientProtocol)
		}

		// Compute the channel-binding input: gs2-header + (PLUS only)
		// raw tls-server-end-point bytes.
		cbInput := append([]byte(c.gs2Header), c.channelBinding...)
		cbB64 := base64.StdEncoding.EncodeToString(cbInput)

		c.saltedPass = pbkdf2.Key([]byte(c.password), salt, iter, sha256.Size, sha256.New)
		clientKey := hmacSHA256(c.saltedPass, []byte("Client Key"))
		storedKeyArr := sha256.Sum256(clientKey)

		clientFinalNoProof := "c=" + cbB64 + ",r=" + combinedNonce
		c.authMessage = c.clientFirst + "," + string(challenge) + "," + clientFinalNoProof
		clientSig := hmacSHA256(storedKeyArr[:], []byte(c.authMessage))
		proof := make([]byte, len(clientKey))
		for i := range proof {
			proof[i] = clientKey[i] ^ clientSig[i]
		}
		clientFinal := clientFinalNoProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
		c.state = 2
		return []byte(clientFinal), false, nil
	case 2:
		// server-final: v=<base64 server signature> OR e=<error>
		attrs, err := parseSCRAMAttrs(string(challenge))
		if err != nil {
			return nil, false, fmt.Errorf("scram: server-final: %w", err)
		}
		if e, ok := attrs["e"]; ok {
			return nil, false, fmt.Errorf("scram: server error %q: %w", e, ErrClientProtocol)
		}
		vB64, ok := attrs["v"]
		if !ok {
			return nil, false, fmt.Errorf("scram: missing v: %w", ErrClientProtocol)
		}
		v, err := base64.StdEncoding.DecodeString(vB64)
		if err != nil {
			return nil, false, fmt.Errorf("scram: v b64: %w", ErrClientProtocol)
		}
		serverKey := hmacSHA256(c.saltedPass, []byte("Server Key"))
		expected := hmacSHA256(serverKey, []byte(c.authMessage))
		if !hmac.Equal(v, expected) {
			return nil, false, fmt.Errorf("scram: server signature mismatch: %w", ErrClientProtocol)
		}
		c.state = 3
		return nil, true, nil
	default:
		return nil, false, ErrClientProtocol
	}
}

// saslnameEncode applies RFC 5802 §5.1 escaping to a username: ',' ->
// "=2C", '=' -> "=3D". Other bytes pass through.
func saslnameEncode(s string) string {
	if !strings.ContainsAny(s, ",=") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ',':
			b.WriteString("=2C")
		case '=':
			b.WriteString("=3D")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// _ keeps crypto/rand in the import set; randomNonce is the live
// consumer (declared in scram.go) and tests do not currently inject a
// deterministic source.
var _ = rand.Reader
