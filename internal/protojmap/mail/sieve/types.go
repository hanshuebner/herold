package sieve

import (
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2). Aliased here
// so per-method code reads naturally.
type jmapID = string

// idForPrincipal renders a principal id in the wire form Sieve uses for
// its singleton script row. The JMAP "id" of the script is the
// principal id stringified — there is one script per principal in v1
// (per REQ-PROTO-51's ManageSieve model).
func idForPrincipal(pid store.PrincipalID) jmapID {
	return strconv.FormatUint(uint64(pid), 10)
}

// principalFromID inverts idForPrincipal, returning ok=false on any
// non-numeric input. Sieve/get and Sieve/set use this to validate that
// a client-supplied id refers to the requesting principal's row.
func principalFromID(id jmapID) (store.PrincipalID, bool) {
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.PrincipalID(v), true
}

// jmapSieveScript is the wire-form Sieve script object (RFC 9007
// §2.1). Phase 1 ships a singleton-per-principal model so isActive is
// always true on a present row; the only scripts visible are those
// currently in use.
type jmapSieveScript struct {
	ID        jmapID    `json:"id"`
	Name      string    `json:"name"`
	BlobID    string    `json:"blobId"`
	IsActive  bool      `json:"isActive"`
	CreatedAt time.Time `json:"createdAt"`
}

// sieveValidationError is the per-error entry returned in
// "sieveValidationError" responses (RFC 9007 §2.4). One entry per
// parser/validator failure; line/column are 1-based.
type sieveValidationError struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}
