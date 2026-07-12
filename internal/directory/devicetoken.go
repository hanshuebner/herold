package directory

// devicetoken.go implements the bearer-token grant native clients (the
// Android app, issue #199) use to obtain a long-lived, revocable
// per-device access token without ever holding a browser session
// cookie: the client exchanges its password (and TOTP code, when
// enrolled) directly for a Bearer token, mirroring the credential
// check `POST /api/v1/auth/login` already performs but returning a
// token instead of a cookie.
//
// Token format and storage deliberately reuse the existing "hk_"
// Bearer-API-key mechanism (internal/protoadmin.HashAPIKey /
// internal/protojmap.hashAPIKey) bit for bit:
//
//   - The plaintext token is a 256-bit random value with the "hk_"
//     prefix, so it authenticates against protojmap's and protoadmin's
//     existing Bearer-verification code paths (requireAuth /
//     authenticateBearer) with zero changes there -- "which listeners
//     accept bearer tokens" is already answered by the code that
//     accepts operator-issued API keys today.
//   - It is hashed at rest with SHA-256, not Argon2id. Argon2id's
//     deliberate slowness defends a *low-entropy*, human-chosen secret
//     (a password) against offline brute force; applied to an
//     already-unguessable 256-bit random token it adds nothing but
//     latency to every Bearer-authenticated JMAP request, which is
//     exactly the hot path STANDARDS.md says to keep a cache in front
//     of. Passwords and TOTP secrets keep Argon2id / the TOTP HMAC
//     construction; opaque bearer secrets use a fast, constant-time-
//     compared hash, matching the scheme already shipped for API keys.
//   - It is revocable via the existing self-service and admin API-key
//     endpoints (DELETE /api/v1/api-keys/{id}, REQ-AUTH-SCOPE-04) --
//     no separate revocation path is introduced.
//   - It does not expire on its own (matching the existing API-key
//     model); revocation is the sole lifecycle control. See the
//     analysis comment on issue #199 for why a shorter-lived
//     access/refresh-token pair (REQ-AND-AUTH-02) is an open question
//     rather than implemented here.
//
// Scope: a device token is issued with the full end-user scope set
// (auth.AllEndUserScopes) -- the same set a Suite session cookie
// carries -- because the mobile client uses it for mail, chat,
// calendar and contacts JMAP calls alike (docs/design/android/notes/
// server-contract.md). Admin scope can never be obtained through this
// grant.
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/store"
)

// DeviceTokenPrefix is the Bearer-token prefix minted by IssueDeviceToken.
// It is identical to protoadmin.APIKeyPrefix / protojmap.APIKeyPrefix
// ("hk_") so a device token authenticates through the existing API-key
// Bearer path unmodified.
const DeviceTokenPrefix = "hk_"

// deviceTokenNamePrefix marks the APIKey.Name of rows minted by
// IssueDeviceToken so self-service and admin listings can tell a
// device-issued token apart from a manually created API key without a
// schema change. DeviceTokenLabel strips it back off for display.
const deviceTokenNamePrefix = "device: "

// maxDeviceLabelLen bounds the operator-supplied device label so a
// pathological client can't stuff an unbounded string into the
// api_keys.name column.
const maxDeviceLabelLen = 200

// ErrTOTPRequired is returned by IssueDeviceToken when the principal has
// TOTP enrolled and no code was supplied. Mirrors the step_up_required
// signal POST /api/v1/auth/login would give a browser client, so the
// caller can prompt for a code without re-asking for the password.
var ErrTOTPRequired = errors.New("directory: totp code required")

// IssueDeviceToken authenticates (email, password) and, when the
// principal has TOTP enrolled, verifies totpCode, then mints a
// long-lived Bearer token scoped to auth.AllEndUserScopes and persists
// it as a store.APIKey row labelled from deviceLabel (free text such as
// "Pixel 8" or "iPhone 15"; empty defaults to "device").
//
// Returns:
//   - ErrUnauthorized: bad email or password (checked before TOTP is ever
//     considered), or the principal is disabled.
//   - ErrRateLimited: the per-(email,source) failure budget from
//     Authenticate or VerifyTOTP is exhausted.
//   - ErrTOTPRequired: the password matched, the principal has TOTP
//     enrolled, and totpCode is either empty or does not validate.
//     Mirrors REQ-AUTH-JSON-LOGIN, which maps both "absent" and
//     "incorrect" to the same step_up_required signal: once the password
//     has been proven correct, a wrong code does not need the same
//     anti-enumeration ambiguity as a wrong password, so the caller can
//     re-prompt for a code (not a full re-login) exactly as REQ-AS-15
//     describes for the browser login flow.
//
// The plaintext token is returned exactly once; only its SHA-256 hash
// is persisted. Callers MUST NOT log the plaintext (observe/secret.go's
// slog field-name stripping does not know about caller-local variables).
func (d *Directory) IssueDeviceToken(ctx context.Context, email, password, totpCode, deviceLabel string) (plaintext string, key store.APIKey, err error) {
	if err := ctx.Err(); err != nil {
		return "", store.APIKey{}, err
	}
	pid, err := d.authenticateWithOptionalTOTP(ctx, email, password, totpCode)
	if err != nil {
		return "", store.APIKey{}, err
	}

	plaintextTok, hash, err := GenerateDeviceToken(d.rand)
	if err != nil {
		return "", store.APIKey{}, fmt.Errorf("directory: generate device token: %w", err)
	}

	scopeJSON, err := json.Marshal(auth.AllEndUserScopes)
	if err != nil {
		return "", store.APIKey{}, fmt.Errorf("directory: encode device token scope: %w", err)
	}

	label := strings.TrimSpace(deviceLabel)
	if label == "" {
		label = "device"
	}
	if len(label) > maxDeviceLabelLen {
		label = label[:maxDeviceLabelLen]
	}

	inserted, err := d.meta.InsertAPIKey(ctx, store.APIKey{
		PrincipalID: pid,
		Hash:        hash,
		Name:        deviceTokenNamePrefix + label,
		ScopeJSON:   string(scopeJSON),
	})
	if err != nil {
		return "", store.APIKey{}, fmt.Errorf("directory: insert device token: %w", err)
	}

	d.audit(ctx, pid, "principal.device_token.issue",
		slog.String("label", label),
		slog.Uint64("api_key_id", uint64(inserted.ID)),
	)

	return plaintextTok, inserted, nil
}

// GenerateDeviceToken returns a fresh plaintext Bearer token and its
// SHA-256 hex hash. rnd is the entropy source (crypto/rand.Reader in
// production; a deterministic reader in tests, matching the Directory's
// own rand field). Exported so tests and the HTTP issuance handler can
// generate/verify tokens without depending on a live Directory.
func GenerateDeviceToken(rnd io.Reader) (plaintext, hash string, err error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	var b [32]byte
	if _, err := rnd.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("directory: read random: %w", err)
	}
	plaintext = DeviceTokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return plaintext, HashDeviceToken(plaintext), nil
}

// HashDeviceToken returns the lowercase hex SHA-256 of plaintext. Bit-
// for-bit identical to protoadmin.HashAPIKey / protojmap.hashAPIKey so a
// device token minted here verifies through either package's existing
// Bearer-auth path.
func HashDeviceToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// DeviceTokenLabel reports whether an APIKey.Name was minted by
// IssueDeviceToken and, if so, returns the operator-supplied label with
// the internal marker stripped off. Used by self-service / admin
// listings that want to render device-issued tokens distinctly from
// manually created API keys.
func DeviceTokenLabel(name string) (label string, ok bool) {
	if !strings.HasPrefix(name, deviceTokenNamePrefix) {
		return "", false
	}
	return strings.TrimPrefix(name, deviceTokenNamePrefix), true
}

// authenticateWithOptionalTOTP verifies (email, password) and, when the
// resolved principal has TOTP enrolled, a required totpCode. It is the
// shared credential-check core behind both IssueDeviceToken and the
// OAuth2 authorize-page login POST (oauth2.go, issue #199): both are
// "prove who you are with password (+TOTP)" entry points that then
// diverge on what they mint (a long-lived device token vs. a short-lived
// authorization code).
//
// Returns the same error classes IssueDeviceToken documents:
// ErrUnauthorized (bad credentials or disabled principal), ErrRateLimited
// (the per-(email,source) budget from Authenticate / VerifyTOTP is
// exhausted), or ErrTOTPRequired (password correct, TOTP enrolled, code
// absent or wrong).
func (d *Directory) authenticateWithOptionalTOTP(ctx context.Context, email, password, totpCode string) (PrincipalID, error) {
	pid, err := d.Authenticate(ctx, email, password)
	if err != nil {
		// ErrUnauthorized / ErrRateLimited already recorded + rate-limited
		// by Authenticate; propagate as-is.
		return 0, err
	}
	p, err := d.meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		return 0, fmt.Errorf("directory: load principal: %w", err)
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		return 0, ErrUnauthorized
	}
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		if totpCode == "" {
			return 0, ErrTOTPRequired
		}
		if verr := d.VerifyTOTP(ctx, pid, totpCode); verr != nil {
			if errors.Is(verr, ErrRateLimited) {
				return 0, ErrRateLimited
			}
			return 0, ErrTOTPRequired
		}
	}
	return pid, nil
}
