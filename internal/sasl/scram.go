package sasl

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// NewSCRAMSHA256 constructs a SCRAM-SHA-256 server-side mechanism
// (RFC 5802). When channelBinding is true the mechanism is the -PLUS
// variant (RFC 5929 tls-server-end-point); callers must also attach
// the binding value via WithTLSServerEndpoint.
func NewSCRAMSHA256(auth Authenticator, lookup PasswordLookup, channelBinding bool) Mechanism {
	return &scramMech{
		auth:           auth,
		lookup:         lookup,
		channelBinding: channelBinding,
	}
}

type scramMech struct {
	auth           Authenticator
	lookup         PasswordLookup
	channelBinding bool

	state int // 0=init, 1=sent first, 2=done
	pid   PrincipalID

	// remembered across Start → Next
	clientFirstBare string
	gs2Header       string
	clientNonce     string
	serverNonce     string
	cred            SCRAMCredentials
	serverFirst     string
	authcid         string
	cbindInput      []byte // gs2-header + (if PLUS) tls-server-end-point bytes
}

func (m *scramMech) Name() string {
	if m.channelBinding {
		return "SCRAM-SHA-256-PLUS"
	}
	return "SCRAM-SHA-256"
}

func (m *scramMech) Principal() (PrincipalID, error) {
	if m.state != 2 {
		return 0, ErrAuthFailed
	}
	return m.pid, nil
}

// Start consumes the client-first-message.
func (m *scramMech) Start(ctx context.Context, ir []byte) ([]byte, bool, error) {
	if m.state != 0 {
		return nil, false, ErrProtocolError
	}
	if m.lookup == nil {
		return nil, false, ErrMechanismUnsupported
	}
	if len(ir) == 0 {
		// Client-first must arrive as the IR in SCRAM.
		return nil, false, fmt.Errorf("SCRAM: missing client-first: %w", ErrInvalidMessage)
	}
	cf := string(ir)
	gs2, bare, err := splitGS2(cf)
	if err != nil {
		return nil, false, err
	}
	m.gs2Header = gs2
	m.clientFirstBare = bare

	// Parse bare: n=authcid,r=clientnonce[,ext]*
	attrs, err := parseSCRAMAttrs(bare)
	if err != nil {
		return nil, false, err
	}
	authcid, ok := attrs["n"]
	if !ok {
		return nil, false, fmt.Errorf("SCRAM: missing n=: %w", ErrInvalidMessage)
	}
	cnonce, ok := attrs["r"]
	if !ok || cnonce == "" {
		return nil, false, fmt.Errorf("SCRAM: missing r=: %w", ErrInvalidMessage)
	}
	m.authcid = decodeSaslname(authcid)
	m.clientNonce = cnonce

	// Channel binding policy validation.
	switch {
	case strings.HasPrefix(gs2, "p="):
		if !m.channelBinding {
			return nil, false, fmt.Errorf("SCRAM: client used p=/PLUS on a non-PLUS mechanism: %w", ErrChannelBindingMismatch)
		}
	case strings.HasPrefix(gs2, "y,"):
		if m.channelBinding {
			// y, means client thinks server does NOT support CB but
			// selected the -PLUS mechanism; that is a mismatch.
			return nil, false, fmt.Errorf("SCRAM: y-flag on PLUS mechanism: %w", ErrChannelBindingMismatch)
		}
	case strings.HasPrefix(gs2, "n,"):
		if m.channelBinding {
			return nil, false, fmt.Errorf("SCRAM: n-flag on PLUS mechanism: %w", ErrChannelBindingMismatch)
		}
	default:
		return nil, false, fmt.Errorf("SCRAM: bad gs2-header: %w", ErrInvalidMessage)
	}

	// Look up credentials; produce a random salt+iter on failure to
	// keep timing indistinguishable from success for unknown users.
	cred, pid, lookupErr := m.lookup.LookupSCRAMCredentials(ctx, m.authcid)
	if lookupErr != nil {
		// Deterministic-fake credentials so we still emit a challenge.
		cred = fakeCredentials(m.authcid)
		m.pid = 0
	} else {
		m.pid = pid
	}
	m.cred = cred

	// Generate server nonce and append to the client nonce per RFC 5802.
	sNonce, err := randomNonce(18)
	if err != nil {
		return nil, false, fmt.Errorf("SCRAM: random nonce: %w", err)
	}
	m.serverNonce = sNonce
	combined := m.clientNonce + m.serverNonce

	// server-first-message = r=combined,s=base64(salt),i=iter
	sf := fmt.Sprintf("r=%s,s=%s,i=%d",
		combined,
		base64.StdEncoding.EncodeToString(cred.Salt),
		cred.Iterations,
	)
	m.serverFirst = sf
	m.state = 1
	// If the initial lookup failed, remember by zeroing pid; we still
	// advance through the protocol to not leak timing. The auth check
	// in Next compares against the fake credentials, which will fail.
	if lookupErr != nil && pid == 0 {
		m.pid = 0
	}
	return []byte(sf), false, nil
}

// Next consumes the client-final-message and produces
// server-final-message.
func (m *scramMech) Next(ctx context.Context, resp []byte) ([]byte, bool, error) {
	if m.state != 1 {
		return nil, false, ErrProtocolError
	}
	clientFinal := string(resp)
	attrs, err := parseSCRAMAttrs(clientFinal)
	if err != nil {
		return nil, false, err
	}
	cbindB64, ok := attrs["c"]
	if !ok {
		return nil, false, fmt.Errorf("SCRAM: missing c=: %w", ErrInvalidMessage)
	}
	nonce, ok := attrs["r"]
	if !ok || nonce != m.clientNonce+m.serverNonce {
		return nil, false, fmt.Errorf("SCRAM: nonce mismatch: %w", ErrInvalidMessage)
	}
	proofB64, ok := attrs["p"]
	if !ok {
		return nil, false, fmt.Errorf("SCRAM: missing p=: %w", ErrInvalidMessage)
	}
	// Recompute the expected channel-binding input.
	var cb []byte
	if m.channelBinding {
		ep := channelBinding(ctx)
		if len(ep) == 0 {
			return nil, false, fmt.Errorf("SCRAM-PLUS: no server-endpoint binding: %w", ErrChannelBindingMismatch)
		}
		cb = ep
	}
	expCBInput := append([]byte(m.gs2Header), cb...)
	expCBB64 := base64.StdEncoding.EncodeToString(expCBInput)
	if subtle.ConstantTimeCompare([]byte(expCBB64), []byte(cbindB64)) != 1 {
		return nil, false, fmt.Errorf("SCRAM: channel binding mismatch: %w", ErrChannelBindingMismatch)
	}
	m.cbindInput = expCBInput

	// Reconstruct client-final-message-without-proof.
	cfWithoutProof := stripProof(clientFinal)
	authMessage := m.clientFirstBare + "," + m.serverFirst + "," + cfWithoutProof

	// Verify proof. ClientSignature = HMAC(StoredKey, AuthMessage).
	// Reconstruct ClientKey = ClientProof XOR ClientSignature and
	// compare H(ClientKey) against stored StoredKey.
	clientSignature := hmacSHA256(m.cred.StoredKey, []byte(authMessage))
	clientProof, err := base64.StdEncoding.DecodeString(proofB64)
	if err != nil {
		return nil, false, fmt.Errorf("SCRAM: bad proof b64: %w", ErrInvalidMessage)
	}
	if len(clientProof) != len(clientSignature) {
		return nil, false, fmt.Errorf("SCRAM: bad proof length: %w", ErrAuthFailed)
	}
	reconstructedClientKey := make([]byte, len(clientSignature))
	for i := range reconstructedClientKey {
		reconstructedClientKey[i] = clientProof[i] ^ clientSignature[i]
	}
	reconstructedStored := sha256.Sum256(reconstructedClientKey)
	if subtle.ConstantTimeCompare(reconstructedStored[:], m.cred.StoredKey) != 1 || m.pid == 0 {
		return nil, false, ErrAuthFailed
	}
	serverSignature := hmacSHA256(m.cred.ServerKey, []byte(authMessage))
	sf := "v=" + base64.StdEncoding.EncodeToString(serverSignature)
	m.state = 2
	return []byte(sf), true, nil
}

// DeriveSCRAMCredentials derives SCRAM credentials from a plaintext
// password using PBKDF2-HMAC-SHA256. Suitable for tests and for the
// on-password-set path in the directory (future wave).
func DeriveSCRAMCredentials(password string, salt []byte, iterations int) SCRAMCredentials {
	salted := pbkdf2.Key([]byte(password), salt, iterations, sha256.Size, sha256.New)
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	storedKeyArr := sha256.Sum256(clientKey)
	stored := storedKeyArr[:]
	server := hmacSHA256(salted, []byte("Server Key"))
	return SCRAMCredentials{
		Salt:       append([]byte(nil), salt...),
		Iterations: iterations,
		StoredKey:  stored,
		ServerKey:  server,
	}
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// fakeCredentials returns deterministic bogus credentials for an
// unknown authcid so the server-first message we emit is indistinguishable
// (in timing and structure) from the hit path. The resulting StoredKey
// will never match a legitimate client proof.
func fakeCredentials(authcid string) SCRAMCredentials {
	salt := sha256.Sum256([]byte("sasl-scram-fake:" + authcid))
	return DeriveSCRAMCredentials("impossible", salt[:16], 4096)
}

func randomNonce(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// RFC 5802: nonce is printable ASCII except ','. Base64 (without
	// '=' trailer) fits; strip '/' and '+' by falling back to
	// URL-safe alphabet without padding.
	s := base64.RawURLEncoding.EncodeToString(b)
	return s, nil
}

// splitGS2 separates the GS2 header from client-first-message-bare.
// RFC 5802 §5.1: client-first-message = gs2-header "," [ reserved-mext
// "," ] username "," nonce [...]. The gs2-header is gs2-cbind-flag ","
// [ authzid ] ",". Return both forms.
func splitGS2(cf string) (gs2, bare string, err error) {
	// gs2-cbind-flag is "p=<name>", "y", or "n". It is followed by
	// optional authzid after a comma.
	parts := strings.SplitN(cf, ",", 3)
	if len(parts) < 3 {
		return "", "", fmt.Errorf("SCRAM: short client-first: %w", ErrInvalidMessage)
	}
	gs2 = parts[0] + "," + parts[1] + ","
	bare = parts[2]
	return gs2, bare, nil
}

// parseSCRAMAttrs parses "a=val,b=val,..." into a map. Values are not
// un-escaped (we lookup =2C / =3D manually for names only when needed).
func parseSCRAMAttrs(s string) (map[string]string, error) {
	out := make(map[string]string)
	for _, kv := range strings.Split(s, ",") {
		if len(kv) < 2 || kv[1] != '=' {
			// Ignore reserved-mext and similar decorations.
			continue
		}
		out[kv[:1]] = kv[2:]
	}
	return out, nil
}

// stripProof returns the client-final-message-without-proof: drop the
// trailing ",p=..." attribute.
func stripProof(s string) string {
	idx := strings.LastIndex(s, ",p=")
	if idx < 0 {
		return s
	}
	return s[:idx]
}

// decodeSaslname applies RFC 5802 §5.1 saslname normalization: =2C -> ','
// and =3D -> '='. Any other '=<...>' is an error.
func decodeSaslname(s string) string {
	if !strings.ContainsRune(s, '=') {
		return s
	}
	var out bytes.Buffer
	for i := 0; i < len(s); i++ {
		if s[i] == '=' && i+2 < len(s) {
			switch s[i+1 : i+3] {
			case "2C":
				out.WriteByte(',')
				i += 2
				continue
			case "3D":
				out.WriteByte('=')
				i += 2
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
