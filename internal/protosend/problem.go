package protosend

import (
	"encoding/json"
	"net/http"
)

// problemTypeBase is the URI-reference namespace for protosend problem
// types. Stable across releases; consumers that recognise the type
// string can rely on it for classification. Distinct from the protoadmin
// base so HTTP error consumers can route by package boundary.
const problemTypeBase = "https://netzhansa.com/problems/send/"

// problemDoc is the RFC 7807 "application/problem+json" body.
//
// NOTE: This is a near-duplicate of internal/protoadmin/problemDoc. The
// two surfaces share the shape but live in different packages so each
// owns its set of stable type URIs. A future cleanup can converge both
// onto a small internal/httperr package once a third HTTP surface
// arrives (REQ-HOOK Part B); two callers does not earn the abstraction.
type problemDoc struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// writeProblem serialises an RFC 7807 problem document with the given
// type slug and status code.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, typeSlug, title, detail string) {
	doc := newProblem(r, status, typeSlug, title, detail)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

func newProblem(r *http.Request, status int, typeSlug, title, detail string) *problemDoc {
	doc := &problemDoc{
		Type:   problemTypeBase + typeSlug,
		Title:  title,
		Status: status,
		Detail: detail,
	}
	if r != nil {
		doc.Instance = r.URL.Path
	}
	return doc
}

// writeJSON serialises a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// decodeJSONBody reads the request body into dst with the given byte
// cap. Returns false and writes a 400 problem if decoding fails.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, max int64, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	if err := dec.Decode(dst); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid-body",
			"request body could not be parsed", err.Error())
		return false
	}
	return true
}
