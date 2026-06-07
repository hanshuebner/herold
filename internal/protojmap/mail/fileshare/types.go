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

// recordToJMAP converts a store.FileShare to the wire form.
// publicBaseURL is the operator-supplied base (e.g. "https://mail.example.com").
func recordToJMAP(fs store.FileShare, publicBaseURL string) jmapFileShare {
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
