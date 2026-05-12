package taggedaddress

import (
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

// jmapTaggedAddressFilter is the wire-form TaggedAddressFilter object
// (REQ-TAG-70). It mirrors store.TaggedAddressFilter with stable
// JSON-friendly types.
type jmapTaggedAddressFilter struct {
	// ID is the opaque, store-allocated primary key. Clients echo this
	// back on update / destroy.
	ID jmapID `json:"id"`
	// BaseIdentityID is the JMAP Identity id whose canonical address
	// (minus the +suffix) catches mail tagged by this row.
	BaseIdentityID jmapID `json:"baseIdentityId"`
	// Suffix is the lower-cased sub-address suffix (REQ-TAG-02). The
	// store normalises on insert; the wire never carries mixed-case
	// suffixes.
	Suffix string `json:"suffix"`
	// Action is one of "label", "label_archive", "label_archive_read"
	// (REQ-TAG-30).
	Action string `json:"action"`
	// LabelName is the mailbox/label name to file into.
	LabelName string `json:"labelName"`
	// CreatedAt is the row's insert instant.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is the most recent mutation instant.
	UpdatedAt string `json:"updatedAt"`
}

func recordToJMAP(f store.TaggedAddressFilter) jmapTaggedAddressFilter {
	return jmapTaggedAddressFilter{
		ID:             f.ID,
		BaseIdentityID: f.BaseIdentityID,
		Suffix:         f.Suffix,
		Action:         f.Action,
		LabelName:      f.LabelName,
		CreatedAt:      jmapUTCDate(f.CreatedAt),
		UpdatedAt:      jmapUTCDate(f.UpdatedAt),
	}
}

// stateString stringifies the principal's Identity state counter to the
// JMAP wire form. Tagged-address mutations bump JMAPStateKindIdentity so
// the SPA's Identity/changes consumers re-fetch (REQ-TAG-72). The /get
// and /changes responses surface that same counter so a single state
// token covers both surfaces.
func stateString(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

// validateSuffix reports an error string when s is not a valid stored
// suffix. Empty and ASCII-control bytes are rejected; the length cap is
// generous (REQ-TAG-01 leaves the upper bound to implementation
// discretion, but unbounded suffixes are a wire-protocol abuse vector).
func validateSuffix(s string) (string, error) {
	if s == "" {
		return "", errInvalid("suffix must not be empty")
	}
	if len(s) > 64 {
		return "", errInvalid("suffix exceeds 64-byte limit")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Disallow control bytes, '@', and '+' (the suffix is stored
		// without the delimiter; embedding a `+` in the stored form is
		// allowed by REQ-TAG-01, but `@` and control bytes are not).
		if c < 0x20 || c == 0x7f || c == '@' {
			return "", errInvalid("suffix contains forbidden byte")
		}
	}
	return s, nil
}

// validateAction reports an error if a is not one of the allowed
// TaggedAddressAction* constants (REQ-TAG-30).
func validateAction(a string) error {
	switch a {
	case store.TaggedAddressActionLabel,
		store.TaggedAddressActionLabelArchive,
		store.TaggedAddressActionLabelArchiveRead:
		return nil
	}
	return errInvalid("action must be label, label_archive, or label_archive_read")
}

// validateLabelName reports an error when n is empty or excessively
// long. The 255-byte ceiling matches IMAP mailbox-name limits in
// practice; the store itself only requires non-empty.
func validateLabelName(n string) error {
	if n == "" {
		return errInvalid("labelName must not be empty")
	}
	if len(n) > 255 {
		return errInvalid("labelName exceeds 255-byte limit")
	}
	return nil
}

// errInvalid is a tiny typed error so the handler can format
// invalidProperties uniformly.
type errInvalid string

func (e errInvalid) Error() string { return string(e) }
