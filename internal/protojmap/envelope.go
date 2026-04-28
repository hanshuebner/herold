package protojmap

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Id is the JMAP id type (RFC 8620 §1.2). Opaque string assigned by
// the server; the client MAY use the special form "#<creationId>" in
// some method arguments to back-reference a creation in the same
// request (RFC 8620 §5.3) — that is distinct from the result-back
// reference handled in this file.
type Id = string

// Invocation is one method call or response entry — the canonical RFC
// 8620 §3.4 triple of [name, arguments, callId]. JSON encoding uses
// raw arrays; we model it as a struct here so the dispatcher reads
// fields by name. Marshal/Unmarshal preserve the on-the-wire array
// shape.
type Invocation struct {
	// Name is the JMAP method name on requests ("Email/get") or
	// "error" / "<method>" on responses.
	Name string
	// Args is the raw JSON arguments object. The dispatcher resolves
	// any "#"-prefixed back-references (RFC 8620 §3.7) before passing
	// it to the handler.
	Args json.RawMessage
	// CallID is the client-assigned correlation token. The dispatcher
	// echoes it on every response entry produced from this call.
	CallID string
}

// MarshalJSON renders the invocation as [name, args, callId].
func (i Invocation) MarshalJSON() ([]byte, error) {
	if len(i.Args) == 0 {
		return json.Marshal([3]any{i.Name, json.RawMessage(`{}`), i.CallID})
	}
	return json.Marshal([3]any{i.Name, i.Args, i.CallID})
}

// UnmarshalJSON parses the invocation from [name, args, callId].
func (i *Invocation) UnmarshalJSON(b []byte) error {
	var raw [3]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("invocation: %w", err)
	}
	if err := json.Unmarshal(raw[0], &i.Name); err != nil {
		return fmt.Errorf("invocation name: %w", err)
	}
	i.Args = append(i.Args[:0], raw[1]...)
	if err := json.Unmarshal(raw[2], &i.CallID); err != nil {
		return fmt.Errorf("invocation callId: %w", err)
	}
	return nil
}

// Request is the JMAP request envelope (RFC 8620 §3.3).
type Request struct {
	Using       []CapabilityID `json:"using"`
	MethodCalls []Invocation   `json:"methodCalls"`
	CreatedIDs  map[Id]Id      `json:"createdIds,omitempty"`
}

// Response is the JMAP response envelope (RFC 8620 §3.3).
type Response struct {
	MethodResponses []Invocation `json:"methodResponses"`
	SessionState    string       `json:"sessionState"`
	CreatedIDs      map[Id]Id    `json:"createdIds,omitempty"`
}

// resolveBackReferences walks args in-place and replaces every JMAP
// result-reference (RFC 8620 §3.7) with the value extracted from
// previously executed method responses. A result-reference is a JSON
// object of shape:
//
//	{"resultOf": "<callId>", "name": "<method>", "path": "/some/jpointer"}
//
// AND the field that holds it is a key prefixed with "#" in the
// containing object — e.g. {"#parentId": {"resultOf": ..., ...}}. The
// resolver renames the key from "#parentId" to "parentId" and
// substitutes the resolved value.
//
// On any failure the function returns a MethodError with type
// "invalidResultReference" so the caller can short-circuit the method.
func resolveBackReferences(args json.RawMessage, prior []Invocation) (json.RawMessage, *MethodError) {
	if len(args) == 0 {
		return args, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		// args may be a primitive or an array; handlers receive it as
		// the original bytes in that case.
		return args, nil
	}
	mutated := false
	for k, v := range obj {
		if len(k) == 0 || k[0] != '#' {
			continue
		}
		var ref struct {
			ResultOf string `json:"resultOf"`
			Name     string `json:"name"`
			Path     string `json:"path"`
		}
		if err := json.Unmarshal(v, &ref); err != nil {
			return nil, NewMethodError("invalidResultReference",
				fmt.Sprintf("malformed result reference for %q: %v", k, err))
		}
		if ref.ResultOf == "" || ref.Name == "" {
			return nil, NewMethodError("invalidResultReference",
				fmt.Sprintf("result reference for %q missing resultOf or name", k))
		}
		// Locate the prior call by callId.
		var found *Invocation
		for i := range prior {
			if prior[i].CallID != ref.ResultOf {
				continue
			}
			if prior[i].Name != ref.Name {
				return nil, NewMethodError("invalidResultReference",
					fmt.Sprintf("result reference for %q points at callId %q whose name is %q, expected %q",
						k, ref.ResultOf, prior[i].Name, ref.Name))
			}
			found = &prior[i]
			break
		}
		if found == nil {
			return nil, NewMethodError("invalidResultReference",
				fmt.Sprintf("no prior method response with callId %q", ref.ResultOf))
		}
		val, err := evalJSONPointer(found.Args, ref.Path)
		if err != nil {
			return nil, NewMethodError("invalidResultReference",
				fmt.Sprintf("result reference path %q: %v", ref.Path, err))
		}
		delete(obj, k)
		obj[k[1:]] = val
		mutated = true
	}
	if !mutated {
		return args, nil
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, NewMethodError("invalidResultReference",
			fmt.Sprintf("re-marshal: %v", err))
	}
	return out, nil
}

// gatherCreations builds the in-request creation-id map per RFC 8620
// §5.3: every prior /set response's "created" object contributes one
// entry (creationId -> server-assigned id), and any request-envelope
// createdIds map is merged in. The resulting map is what
// resolveCreationReferences consults.
func gatherCreations(prior []Invocation, requestCreatedIDs map[Id]Id) map[string]Id {
	out := make(map[string]Id, len(requestCreatedIDs)+len(prior))
	for k, v := range requestCreatedIDs {
		out[k] = v
	}
	for _, p := range prior {
		if p.Name == "error" || len(p.Args) == 0 {
			continue
		}
		var resp struct {
			Created map[string]struct {
				ID Id `json:"id"`
			} `json:"created"`
		}
		if err := json.Unmarshal(p.Args, &resp); err != nil {
			continue
		}
		for cid, rec := range resp.Created {
			if rec.ID != "" {
				out[cid] = rec.ID
			}
		}
	}
	return out
}

// isCreationIDChar reports whether c is permitted in a JMAP creation id
// (RFC 8620 §1.2: same character set as Id — A-Z, a-z, 0-9, "-", "_").
func isCreationIDChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_'
}

// looksLikeCreationRef reports whether s is "#<creationId>" with a
// non-empty suffix matching the JMAP id character set. The suffix
// length is bounded at 255 per RFC 8620 §1.2.
func looksLikeCreationRef(s string) bool {
	if len(s) < 2 || len(s) > 256 || s[0] != '#' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isCreationIDChar(s[i]) {
			return false
		}
	}
	return true
}

// resolveCreationReferences walks args and substitutes any string value
// of shape "#<creationId>" with the real id assigned by a prior /set in
// the same request, per RFC 8620 §5.3. Strings whose suffix does not
// match the JMAP id character set are left alone (they are not
// creation references). Strings that look like creation references but
// whose creationId is not in creations are left untouched too — the
// handler will see the original string and may reject it as invalid.
// This is intentionally permissive at the dispatcher to avoid mistaking
// user-supplied content (e.g. a "#hashtag" subject line) for a
// reference.
func resolveCreationReferences(args json.RawMessage, creations map[string]Id) (json.RawMessage, *MethodError) {
	if len(args) == 0 || len(creations) == 0 {
		return args, nil
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return args, nil
	}
	v, mutated := substituteCreationRefs(v, creations)
	if !mutated {
		return args, nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, NewMethodError("serverFail",
			fmt.Sprintf("creation-ref re-marshal: %v", err))
	}
	return out, nil
}

// substituteCreationRefs recursively walks a JSON-decoded value and
// replaces qualifying creation references. Returns the (possibly new)
// value and whether any substitution occurred.
func substituteCreationRefs(v any, creations map[string]Id) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		mutated := false
		for k, child := range t {
			if nv, m := substituteCreationRefs(child, creations); m {
				t[k] = nv
				mutated = true
			}
		}
		return t, mutated
	case []any:
		mutated := false
		for i, child := range t {
			if nv, m := substituteCreationRefs(child, creations); m {
				t[i] = nv
				mutated = true
			}
		}
		return t, mutated
	case string:
		if !looksLikeCreationRef(t) {
			return v, false
		}
		if real, ok := creations[t[1:]]; ok {
			return real, true
		}
		return v, false
	default:
		return v, false
	}
}

// evalJSONPointer evaluates an RFC 6901 JSON Pointer against the JSON
// document doc. RFC 8620 §3.7 limits the pointer subset to: empty
// string (whole doc), /<key>, /<index>, and the "*" wildcard which
// JMAP defines as "for each element of the parent array, evaluate the
// remainder of the path; collect into an array". Wildcards are
// supported because they are required for the back-reference test
// pattern from RFC 8620 §3.7.1 ("#ids": ... /list/*/id).
func evalJSONPointer(doc json.RawMessage, pointer string) (json.RawMessage, error) {
	if pointer == "" {
		return doc, nil
	}
	if pointer[0] != '/' {
		return nil, errors.New("pointer must start with '/' or be empty")
	}
	tokens := splitPointer(pointer)
	return evalPointerTokens(doc, tokens)
}

func splitPointer(pointer string) []string {
	// Strip leading '/'. Per RFC 6901 §3, "~1" decodes to "/" and
	// "~0" decodes to "~"; JMAP never uses these in practice but we
	// honour them for spec compliance.
	parts := []string{}
	cur := ""
	for i := 1; i < len(pointer); i++ {
		c := pointer[i]
		switch c {
		case '/':
			parts = append(parts, cur)
			cur = ""
		case '~':
			if i+1 < len(pointer) {
				switch pointer[i+1] {
				case '0':
					cur += "~"
					i++
					continue
				case '1':
					cur += "/"
					i++
					continue
				}
			}
			cur += string(c)
		default:
			cur += string(c)
		}
	}
	parts = append(parts, cur)
	return parts
}

func evalPointerTokens(doc json.RawMessage, tokens []string) (json.RawMessage, error) {
	if len(tokens) == 0 {
		return doc, nil
	}
	tok := tokens[0]
	rest := tokens[1:]
	if tok == "*" {
		var arr []json.RawMessage
		if err := json.Unmarshal(doc, &arr); err != nil {
			return nil, fmt.Errorf("wildcard expects array: %w", err)
		}
		out := make([]json.RawMessage, 0, len(arr))
		for _, el := range arr {
			v, err := evalPointerTokens(el, rest)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return json.Marshal(out)
	}
	// Try array index first; if doc is an object, fall through to
	// string-key lookup.
	var arr []json.RawMessage
	if err := json.Unmarshal(doc, &arr); err == nil {
		idx, ok := parseIndex(tok)
		if !ok || idx < 0 || idx >= len(arr) {
			return nil, fmt.Errorf("array index %q out of range", tok)
		}
		return evalPointerTokens(arr[idx], rest)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(doc, &obj); err == nil {
		v, ok := obj[tok]
		if !ok {
			return nil, fmt.Errorf("key %q not found", tok)
		}
		return evalPointerTokens(v, rest)
	}
	return nil, fmt.Errorf("token %q against non-object/non-array", tok)
}

func parseIndex(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
