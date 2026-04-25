package protocall

import (
	"encoding/json"
	"net/http"
)

// problemTypeBase is the URI-reference namespace for protocall
// problem types. Stable across releases; consumers that match on the
// trailing slug ("rate_limited", "unauthorized", "turn_disabled",
// ...) can rely on it.
const problemTypeBase = "https://herold.dev/problems/"

// problemDoc is the RFC 7807 "application/problem+json" body. The
// shape is identical to internal/protoadmin's; we duplicate the
// helper to keep the package self-contained.
type problemDoc struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

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
