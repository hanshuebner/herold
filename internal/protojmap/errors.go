package protojmap

import (
	"encoding/json"
	"net/http"
)

// MethodError is the JMAP per-method error object (RFC 8620 §3.6.2).
// Handlers return *MethodError to signal a method-scoped failure; the
// dispatch core then emits a single ["error", <error-body>, <callId>]
// entry in the response envelope, leaving sibling method calls
// untouched. Top-level transport errors use WriteJMAPError instead.
type MethodError struct {
	// Type is the JMAP error type token, e.g. "invalidArguments",
	// "unknownMethod", "forbidden". RFC 8620 §3.6.2 reserves the set;
	// custom types are permitted for handler-specific failures.
	Type string `json:"type"`
	// Description is a free-text human-readable explanation. Optional.
	Description string `json:"description,omitempty"`
	// Properties names the request fields that triggered the error
	// (used by "invalidArguments"). Optional.
	Properties []string `json:"properties,omitempty"`
}

// MarshalJSON renders the error per RFC 8620 §3.6.2. We declare an
// explicit MarshalJSON so future fields land naturally under one
// shape; the default struct tags would already produce equivalent
// output, but pinning the encoding stops accidental drift.
func (e *MethodError) MarshalJSON() ([]byte, error) {
	type alias MethodError
	return json.Marshal((*alias)(e))
}

// NewMethodError is a convenience constructor.
func NewMethodError(typ, description string) *MethodError {
	return &MethodError{Type: typ, Description: description}
}

// problemBase is the URI prefix for JMAP transport-level error type
// slugs, shared with protoadmin's RFC 7807 response envelope so log
// tailers see one consistent vocabulary across protocols.
const problemBase = "https://herold.dev/problems/jmap-"

// problemDoc is the RFC 7807 "application/problem+json" body emitted
// at the HTTP transport layer. Method-level errors use MethodError
// instead and travel inside the response envelope.
type problemDoc struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// WriteJMAPError writes a transport-level error response. Use this
// for RFC 8620 §3.6.1 "Request-level errors" (invalid JSON, unknown
// capability, request too large, ...) and HTTP transport failures
// (auth, rate limit). Per-method errors travel through MethodError on
// the dispatcher path instead.
func WriteJMAPError(w http.ResponseWriter, status int, errType, detail string) {
	doc := problemDoc{
		Type:   problemBase + errType,
		Title:  errType,
		Status: status,
		Detail: detail,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}
