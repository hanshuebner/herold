package store

import "time"

// AppID identifies one registered app (REQ-APP-01).
type AppID uint64

// AppState is the lifecycle state of a registered app (REQ-APP-11).
type AppState string

// App lifecycle states (REQ-APP-11).
const (
	// AppStatePendingApproval is the state of a freshly registered app:
	// its manifest has been fetched but no administrator has approved it,
	// so it offers no actions.
	AppStatePendingApproval AppState = "pending_approval"
	// AppStateActive is an approved app whose actions are offered to the
	// principals it is enabled for (REQ-APP-13).
	AppStateActive AppState = "active"
	// AppStateDisabled is an approved app an administrator switched off;
	// it offers no actions until it is enabled again.
	AppStateDisabled AppState = "disabled"
)

// Valid reports whether s is one of the three defined states.
func (s AppState) Valid() bool {
	switch s {
	case AppStatePendingApproval, AppStateActive, AppStateDisabled:
		return true
	}
	return false
}

// App is one row of the apps table (REQ-APP-01, REQ-APP-02): an external
// web application the operator registered, together with its approved
// manifest, a manifest awaiting approval, and its sealed signing secret.
type App struct {
	// ID is the server-assigned identifier.
	ID AppID
	// AppKey is the manifest's stable reverse-DNS identifier; unique
	// across all apps.
	AppKey string
	// Origin is scheme + host + port; every URL the app is reached at
	// lies under it.
	Origin string
	// ManifestJSON is the approved manifest in canonical JSON, or nil
	// while the app has never been approved.
	ManifestJSON []byte
	// ManifestDigest is the hex SHA-256 of ManifestJSON; empty when
	// ManifestJSON is nil.
	ManifestDigest string
	// PendingManifestJSON is a fetched manifest awaiting approval, or
	// nil when none is pending (REQ-APP-12).
	PendingManifestJSON []byte
	// PendingManifestDigest is the hex SHA-256 of PendingManifestJSON;
	// empty when nothing is pending.
	PendingManifestDigest string
	// SecretSealed is the signing secret sealed with secrets.Seal
	// (REQ-APP-30).
	SecretSealed []byte
	// State is the lifecycle state (REQ-APP-11).
	State AppState
	// AllPrincipals offers the app's actions to every principal when
	// true; otherwise only to the principals listed in app_principals
	// (REQ-APP-13).
	AllPrincipals bool
	// CreatedAt / UpdatedAt are the row timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
	// LastInvokedAt is the instant of the last successful invocation;
	// the zero time when the app was never invoked (REQ-APP-45).
	LastInvokedAt time.Time
}
