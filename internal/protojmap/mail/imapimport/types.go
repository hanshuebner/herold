package imapimport

import (
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2).
type jmapID = string

// jmapUTCDate formats a time.Time as the JMAP UTCDate wire form
// (RFC 8620 §1.4: RFC 3339, UTC, second precision, "Z" suffix).
func jmapUTCDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// jmapIMAPImportAccount is the wire-form IMAPImport object
// (REQ-IMAP-IMP-02, -15, -61). The credential is write-only and NEVER
// included in responses; hasCredential reports whether one is stored.
type jmapIMAPImportAccount struct {
	// ID is the opaque account identifier.
	ID jmapID `json:"id"`
	// AccountName is the user/operator-visible label.
	AccountName string `json:"accountName"`
	// Host is the upstream IMAP server hostname.
	Host string `json:"host"`
	// Port is the upstream IMAP port.
	Port int `json:"port"`
	// TLSMode is the connection TLS strategy ("starttls" or "implicit").
	TLSMode string `json:"tlsMode"`
	// Username is the IMAP authentication username.
	Username string `json:"username"`
	// AuthMethod is the credential type ("password", "app_password",
	// "xoauth2").
	AuthMethod string `json:"authMethod"`
	// BackfillHorizon is the stored backfill floor as an absolute date
	// ("YYYY-MM-DD") or "all" when no floor is set
	// (REQ-IMAP-IMP-15..16).
	BackfillHorizon string `json:"backfillHorizon"`
	// State is the worker lifecycle state ("enabled", "disabled",
	// "errored").
	State string `json:"state"`
	// LastSuccessAt is the most recent successful fetch instant (UTCDate)
	// or null when the account has never connected.
	LastSuccessAt *string `json:"lastSuccessAt"`
	// LastError is the most recent error string ("" when none).
	LastError string `json:"lastError"`
	// DeletePropagates controls upstream EXPUNGE write-back
	// (REQ-IMAP-IMP-44).
	DeletePropagates bool `json:"deletePropagates"`
	// HasCredential reports whether a sealed credential is stored
	// (REQ-IMAP-IMP-70). The actual credential is never returned.
	HasCredential bool `json:"hasCredential"`
}

// recordToJMAP converts a store.IMAPImportAccount to the wire form.
// The sealed credential_ct is inspected only for presence (HasCredential);
// its plaintext is never exposed.
func recordToJMAP(a store.IMAPImportAccount) jmapIMAPImportAccount {
	wire := jmapIMAPImportAccount{
		ID:               a.ID,
		AccountName:      a.AccountName,
		Host:             a.Host,
		Port:             a.Port,
		TLSMode:          string(a.TLSMode),
		Username:         a.Username,
		AuthMethod:       string(a.AuthMethod),
		State:            string(a.State),
		LastError:        a.LastError,
		DeletePropagates: a.DeletePropagates,
		HasCredential:    len(a.CredentialCT) > 0,
	}
	// BackfillHorizon: nil floor means "all"; non-nil is the absolute date
	// (REQ-IMAP-IMP-16).
	if a.BackfillFloorDate == nil {
		wire.BackfillHorizon = "all"
	} else {
		wire.BackfillHorizon = a.BackfillFloorDate.UTC().Format("2006-01-02")
	}
	if a.LastSuccessAt != nil {
		s := jmapUTCDate(*a.LastSuccessAt)
		wire.LastSuccessAt = &s
	}
	return wire
}
