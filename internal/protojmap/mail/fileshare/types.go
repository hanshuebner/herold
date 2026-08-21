package fileshare

import (
	"fmt"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2).
type jmapID = string

// jmapUTCDate formats a time.Time as the JMAP UTCDate wire form
// (RFC 8620 §1.4: RFC 3339, UTC, second precision, "Z" offset).
func jmapUTCDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// stateString stringifies a JMAP state counter.
func stateString(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

// jmapFileShare is the wire-form FileShare object (REQ-SHARE-40..43).
// password and passwordHash are never included on the wire; hasPassword
// reports presence of a password (REQ-SHARE-43).
type jmapFileShare struct {
	// ID is the capability token / primary key. URL-safe CSPRNG string.
	ID jmapID `json:"id"`
	// BlobID is the JMAP blob identifier (the BLAKE3 hash in hex).
	BlobID string `json:"blobId"`
	// Name is the filename presented to the recipient.
	Name string `json:"name"`
	// Type is the MIME content type served on download.
	Type string `json:"type"`
	// Size is the blob byte length.
	Size int64 `json:"size"`
	// URL is the public recipient-facing download URL
	// ({publicBaseURL}/share/{id}).
	URL string `json:"url"`
	// State is the lifecycle state: "pending", "active", or "revoked".
	State string `json:"state"`
	// CreatedAt is the row insert instant (UTCDate).
	CreatedAt string `json:"createdAt"`
	// ExpiresAt is the absolute expiry instant (UTCDate).
	ExpiresAt string `json:"expiresAt"`
	// ExpiresIn is the share's retention in seconds: the lifetime the
	// share carries (or will carry once confirmed), independent of how
	// much of it has already elapsed. For a share created with an
	// explicit retention this is that (clamped) value; for a share that
	// omitted one it is the deployment's default_ttl. This is NOT
	// derived from ExpiresAt - now, so a still-pending share (whose
	// ExpiresAt reflects pending_ttl, not the eventual active lifetime)
	// reports the lifetime it will actually receive at confirmation.
	// REQ-SHARE-02, REQ-SHARE-41.
	ExpiresIn int64 `json:"expiresIn"`
	// MaxDownloads is the optional download cap. Null means unlimited.
	MaxDownloads *int64 `json:"maxDownloads"`
	// DownloadCount is the number of times the blob has been downloaded
	// via the public route (REQ-SHARE-02).
	DownloadCount int64 `json:"downloadCount"`
	// HasPassword reports whether the share is password-protected
	// (REQ-SHARE-43). The actual password is write-only.
	HasPassword bool `json:"hasPassword"`
	// LastDownloadedAt is the instant of the most recent download, or
	// null when no download has occurred.
	LastDownloadedAt *string `json:"lastDownloadedAt"`
	// SourceMessageId / SourceSubject / SourceRecipients are the
	// owner-only message back-reference (REQ-SHARE-04), captured at
	// confirmation. Null/empty while pending. Never exposed on the
	// recipient-facing surfaces.
	SourceMessageID  *string  `json:"sourceMessageId"`
	SourceSubject    *string  `json:"sourceSubject"`
	SourceRecipients []string `json:"sourceRecipients,omitempty"`
}

// capabilityDescriptor is the wire form of the CapabilityFileShares
// session descriptor (REQ-SHARE-40, and the session-capability
// requirement added alongside REQ-SHARE-20). It advertises the
// deployment's configured lifecycle ceiling so the composer's expiry
// picker offers only choices the deployment permits, and defaults to
// the operator's default_ttl rather than a client-side constant.
//
// quota_used_bytes is deliberately absent: it is per-principal runtime
// state, and this descriptor is a per-server (not per-account) value
// under session.capabilities. offload_threshold_bytes is also absent:
// no sysconfig knob backs it, so this package has no server value to
// report for it (the suite's client-side constant remains authoritative).
type capabilityDescriptor struct {
	// DefaultTTLSeconds is the active share lifetime offered as the
	// default preset (FileSharesConfig.DefaultTTL).
	DefaultTTLSeconds int64 `json:"default_ttl_seconds"`
	// MaxTTLSeconds is the ceiling the owner cannot exceed
	// (FileSharesConfig.MaxTTL). Presets above this are not offered.
	MaxTTLSeconds int64 `json:"max_ttl_seconds"`
	// QuotaMaxBytes is the deployment's configured per-principal share
	// storage cap (FileSharesConfig.ShareQuotaPerPrincipal). A per-
	// principal admin override (REQ-SHARE-52) is not reflected here;
	// this is the deployment default.
	QuotaMaxBytes int64 `json:"quota_max_bytes"`
}

// newCapabilityDescriptor builds the session capability descriptor from
// the deployment's FileSharesConfig.
func newCapabilityDescriptor(cfg store.FileSharesConfig) capabilityDescriptor {
	return capabilityDescriptor{
		DefaultTTLSeconds: int64(cfg.DefaultTTL / time.Second),
		MaxTTLSeconds:     int64(cfg.MaxTTL / time.Second),
		QuotaMaxBytes:     cfg.ShareQuotaPerPrincipal,
	}
}

// recordToJMAP converts a store.FileShare to the wire form.
// publicBaseURL is the operator-supplied base (e.g. "https://mail.example.com").
// defaultTTL is the deployment's default_ttl (FileSharesConfig.DefaultTTL),
// used to report ExpiresIn when the share carries no explicit retention
// (mirrors the store's own Confirm fallback).
func recordToJMAP(fs store.FileShare, publicBaseURL string, defaultTTL time.Duration) jmapFileShare {
	retention := fs.Retention
	if retention <= 0 {
		retention = defaultTTL
	}
	wire := jmapFileShare{
		ID:            fs.ID,
		BlobID:        fs.BlobHash,
		Name:          fs.Filename,
		Type:          fs.ContentType,
		Size:          fs.BlobSize,
		URL:           fmt.Sprintf("%s/share/%s", publicBaseURL, fs.ID),
		State:         string(fs.State),
		CreatedAt:     jmapUTCDate(fs.CreatedAt),
		ExpiresAt:     jmapUTCDate(fs.ExpiresAt),
		ExpiresIn:     int64(retention / time.Second),
		MaxDownloads:  fs.MaxDownloads,
		DownloadCount: fs.DownloadCount,
		HasPassword:   fs.PasswordHash != "",
	}
	if fs.LastDownloadedAt != nil {
		s := jmapUTCDate(*fs.LastDownloadedAt)
		wire.LastDownloadedAt = &s
	}
	if fs.SourceMessageID != "" {
		v := fs.SourceMessageID
		wire.SourceMessageID = &v
	}
	if fs.SourceSubject != "" {
		v := fs.SourceSubject
		wire.SourceSubject = &v
	}
	wire.SourceRecipients = fs.SourceRecipients
	return wire
}
