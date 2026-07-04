package protoadmin

import (
	"bytes"
	"encoding/json"
	"io"
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

// writeProblemWithExtras renders an RFC 7807 problem document with
// additional top-level fields merged in. Used by handlers that need to
// surface structured error metadata (e.g. retry_after_seconds) beyond
// the spec's required type/title/status/detail/instance set. The
// extras map keys MUST NOT collide with the standard fields; collisions
// are silently dropped.
func writeProblemWithExtras(w http.ResponseWriter, r *http.Request, status int, typeSlug, title, detail string, extras map[string]any) {
	body := map[string]any{
		"type":   problemTypeBase + typeSlug,
		"title":  title,
		"status": status,
	}
	if detail != "" {
		body["detail"] = detail
	}
	if r != nil {
		body["instance"] = r.URL.Path
	}
	for k, v := range extras {
		switch k {
		case "type", "title", "status", "detail", "instance":
			continue
		}
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
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

// decodeOptionalJSONBody reads the request body into dst. If the body is
// absent or empty (possibly just whitespace) the function returns true
// without modifying dst, so the caller may use dst's zero value as the
// default. Returns false and writes a 400 only when the body is non-empty
// but malformed or contains unknown fields.
func decodeOptionalJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_body",
			"could not read request body", err.Error())
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true // no body; leave dst at its zero value
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_body",
			"request body could not be parsed", err.Error())
		return false
	}
	return true
}
