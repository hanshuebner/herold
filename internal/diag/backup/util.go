package backup

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// micros converts a time.Time to Unix microseconds; the zero time
// maps to 0 (matching the SQL backends' usMicros helper).
func micros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// fromMicros converts a Unix-micros value back to a UTC time.Time;
// the zero value maps to the zero time.
func fromMicros(us int64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

// encodeJSON returns the JSON encoding of v as a string. Used for
// the few columns that store JSON blobs (acme_orders.hostnames_json,
// webhooks.retry_policy_json, audit_log.metadata_json). Marshalling
// a tiny struct never errors in practice; on a freak failure we
// return "" rather than panicking.
func encodeJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeJSON parses raw into out. Caller-supplied out must be a
// pointer; errors are returned to the caller.
func decodeJSON(raw string, out any) error {
	return json.Unmarshal([]byte(raw), out)
}

// decodeMetadataJSON decodes the audit_log metadata_json blob into a
// string-keyed map. Returns nil for empty / malformed input so the
// caller can fall through to "no metadata".
func decodeMetadataJSON(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

// splitCSV splits a comma-separated list into a string slice,
// trimming surrounding whitespace and dropping empty fields. The
// SQLite / Postgres backends store keyword and scope lists this way
// to keep the schema flat; the application layer joins / splits as
// needed.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sortStrings is a tiny convenience: sorted copy of in.
func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
