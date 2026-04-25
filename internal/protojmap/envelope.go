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
