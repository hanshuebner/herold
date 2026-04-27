package protoadmin

import (
	"encoding/json"
	"net/http"
)

// problemTypeBase is the URI-reference namespace for protoadmin problem
// types. Stable across releases; a consumer that recognises a type
// string can rely on it to classify errors.
const problemTypeBase = "https://netzhansa.com/problems/"

// problemDoc is the RFC 7807 "application/problem+json" body.
type problemDoc struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// writeProblem serialises an RFC 7807 problem document with the given
// type slug ("not_found", "conflict", etc.) and status code.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, typeSlug, title, detail string) {
	doc := problemDoc{
		Type:   problemTypeBase + typeSlug,
		Title:  title,
		Status: status,
		Detail: detail,
	}
	if r != nil {
		doc.Instance = r.URL.Path
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

// writeJSON serialises a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// decodeJSONBody reads the request body into dst. Returns false and
// writes a 400 problem if decoding fails; the caller simply returns.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_body",
			"request body could not be parsed", err.Error())
		return false
	}
	return true
}
