package protojmap

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"
)

// handleAPI is POST /jmap. Decodes the request envelope, validates the
// "using" capability list, runs each method call in order with
// back-reference resolution, and writes the response envelope per RFC
// 8620 §3.3-3.7.
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJMAPError(w, http.StatusMethodNotAllowed,
			"methodNotAllowed", "POST required")
		return
	}
	// RFC 8620 §3.4: the Content-Type MUST be "application/json".
	// Parameters such as "; charset=utf-8" are permitted. Reject anything
	// whose media type is not exactly "application/json" before body
	// decode so malformed or incorrectly-typed requests never reach the
	// JSON decoder.
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || mt != "application/json" {
			WriteJMAPError(w, http.StatusBadRequest,
				"notJSON", "Content-Type must be application/json")
			return
		}
	} else {
		// A missing Content-Type is equally invalid for a POST that must
		// carry a JSON body.
		WriteJMAPError(w, http.StatusBadRequest,
			"notJSON", "Content-Type must be application/json")
		return
	}
	body := http.MaxBytesReader(w, r.Body, s.opts.MaxSizeRequest)
	defer body.Close()
	dec := json.NewDecoder(body)
	var req Request
	if err := dec.Decode(&req); err != nil {
		// http.MaxBytesReader returns its own error type when the cap
		// is exceeded; surface that as the JMAP "requestTooLarge".
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			WriteJMAPError(w, http.StatusRequestEntityTooLarge,
				"limitTooLarge", "request body exceeds maxSizeRequest")
			return
		}
		WriteJMAPError(w, http.StatusBadRequest,
			"notJSON", err.Error())
		return
	}
	if len(req.MethodCalls) == 0 {
		WriteJMAPError(w, http.StatusBadRequest,
			"notRequest", "methodCalls must not be empty")
		return
	}
	if len(req.MethodCalls) > s.opts.MaxCallsInRequest {
		WriteJMAPError(w, http.StatusBadRequest,
			"limitTooLarge", "maxCallsInRequest exceeded")
		return
	}
	for _, cap := range req.Using {
		if !s.reg.HasCapability(cap) {
			WriteJMAPError(w, http.StatusBadRequest,
				"unknownCapability", string(cap))
			return
		}
	}

	ctx := r.Context()
	log := loggerFromContext(ctx, s.log)

	resp := Response{
		MethodResponses: make([]Invocation, 0, len(req.MethodCalls)),
		SessionState:    s.sessionState(ctx),
		CreatedIDs:      req.CreatedIDs,
	}
	for _, call := range req.MethodCalls {
		entries := s.dispatchOneMulti(ctx, log, call, resp.MethodResponses, req.Using, req.CreatedIDs)
		resp.MethodResponses = append(resp.MethodResponses, entries...)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	if err := enc.Encode(resp); err != nil {
		s.log.Warn("protojmap.api.encode_failed", "err", err)
	}
}

// dispatchOneMulti executes one method call and returns one or more
// Invocation values. When the handler returns a MultipleInvocations the
// extras are appended after the primary response entry per RFC 8621 §7.5.
//
// After each invocation it emits one record whose message is the method name
// (or warn on method-level error) per REQ-OPS-86d. The log carries:
//   - activity = "user" for data methods; "audit" for Identity/* and
//     Principal/* (security-relevant per REQ-OPS-86).
//   - method, account_id (if present in args), client_call_id, principal_id.
//   - For */set: created, updated, destroyed counts.
//   - For */query: result_count, limit if present.
//   - For */get: id_count requested.
//   - On error: error = "<jmap error type>".
func (s *Server) dispatchOneMulti(ctx context.Context, log *slog.Logger, call Invocation, prior []Invocation, using []CapabilityID, requestCreatedIDs map[Id]Id) []Invocation {
	handler, ok := s.reg.Resolve(call.Name)
	if !ok {
		err := NewMethodError("unknownMethod", "no handler registered for "+call.Name)
		s.logMethodCall(ctx, log, call, nil, err)
		return []Invocation{errorInvocation(call.CallID, err)}
	}
	cap, _ := s.reg.CapabilityFor(call.Name)
	if !capabilityListed(using, cap) {
		err := NewMethodError("unknownMethod",
			"method "+call.Name+" requires capability "+string(cap)+" in 'using'")
		s.logMethodCall(ctx, log, call, nil, err)
		return []Invocation{errorInvocation(call.CallID, err)}
	}
	args, refErr := resolveBackReferences(call.Args, prior)
	if refErr != nil {
		s.logMethodCall(ctx, log, call, nil, refErr)
		return []Invocation{errorInvocation(call.CallID, refErr)}
	}
	creations := gatherCreations(prior, requestCreatedIDs)
	args, refErr = resolveCreationReferences(args, creations)
	if refErr != nil {
		s.logMethodCall(ctx, log, call, nil, refErr)
		return []Invocation{errorInvocation(call.CallID, refErr)}
	}
	resp, mErr := handler.Execute(ctx, args)
	if mErr != nil {
		s.logMethodCall(ctx, log, call, nil, mErr)
		return []Invocation{errorInvocation(call.CallID, mErr)}
	}
	// Check for MultipleInvocations before marshalling.
	if multi, ok := resp.(MultipleInvocations); ok {
		primary := Invocation{Name: call.Name, Args: multi.Primary, CallID: call.CallID}
		s.logMethodCall(ctx, log, call, multi.Primary, nil)
		return append([]Invocation{primary}, multi.Extra...)
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		mErr := NewMethodError("serverFail", "response marshal: "+err.Error())
		s.logMethodCall(ctx, log, call, nil, mErr)
		return []Invocation{errorInvocation(call.CallID, mErr)}
	}
	s.logMethodCall(ctx, log, call, respBytes, nil)
	return []Invocation{{Name: call.Name, Args: respBytes, CallID: call.CallID}}
}

// logMethodCall emits the per-method structured log record per REQ-OPS-86d.
// respArgs is the marshaled response (nil on error paths). mErr is non-nil
// when the method returned a JMAP-level error.
func (s *Server) logMethodCall(ctx context.Context, log *slog.Logger, call Invocation, respArgs json.RawMessage, mErr *MethodError) {
	activity := methodActivity(call.Name)
	level := slog.LevelInfo
	if mErr != nil {
		level = slog.LevelWarn
	}

	attrs := []slog.Attr{
		slog.String("activity", activity),
		slog.String("client_call_id", call.CallID),
	}

	// Pull principal_id from context if available.
	if p, ok := PrincipalFromContext(ctx); ok {
		attrs = append(attrs, slog.Uint64("principal_id", uint64(p.ID)))
	}

	// Extract account_id from the call args when present.
	if len(call.Args) > 0 {
		var base struct {
			AccountID string `json:"accountId"`
		}
		if json.Unmarshal(call.Args, &base) == nil && base.AccountID != "" {
			attrs = append(attrs, slog.String("account_id", base.AccountID))
		}
	}

	// Emit method-family-specific count attrs from the response.
	if len(respArgs) > 0 && mErr == nil {
		attrs = appendMethodCountAttrs(attrs, call.Name, respArgs)
	}

	if mErr != nil {
		attrs = append(attrs, slog.String("error", mErr.Type))
	}

	log.LogAttrs(ctx, level, call.Name, attrs...)
}

// methodActivity returns the activity tag for a JMAP method name per
// REQ-OPS-86. Identity/* and Principal/* methods are "audit" because
// they touch identity/auth-scope data that a security reviewer would
// want retained when other activities are filtered. All other JMAP
// methods are "user".
func methodActivity(method string) string {
	switch {
	case strings.HasPrefix(method, "Identity/"),
		strings.HasPrefix(method, "Principal/"):
		return "audit"
	default:
		return "user"
	}
}

// appendMethodCountAttrs extracts operation counts from the JSON response
// body and appends them as slog.Attr values. The method name determines
// which response shape to decode.
//
//   - */set responses carry created, updated, destroyed maps; we log counts.
//   - */query responses carry ids (the result list) and limit; we log counts.
//   - */get responses carry list; we log the count of returned objects
//     alongside the id_count from the request args (added by caller above).
func appendMethodCountAttrs(attrs []slog.Attr, method string, resp json.RawMessage) []slog.Attr {
	switch {
	case strings.HasSuffix(method, "/set"):
		var s struct {
			Created   map[string]json.RawMessage `json:"created"`
			Updated   map[string]json.RawMessage `json:"updated"`
			Destroyed []string                   `json:"destroyed"`
		}
		if json.Unmarshal(resp, &s) == nil {
			attrs = append(attrs,
				slog.Int("created", len(s.Created)),
				slog.Int("updated", len(s.Updated)),
				slog.Int("destroyed", len(s.Destroyed)),
			)
		}
	case strings.HasSuffix(method, "/query"):
		var q struct {
			IDs   []string `json:"ids"`
			Limit *int     `json:"limit"`
		}
		if json.Unmarshal(resp, &q) == nil {
			attrs = append(attrs, slog.Int("result_count", len(q.IDs)))
			if q.Limit != nil {
				attrs = append(attrs, slog.Int("limit", *q.Limit))
			}
		}
	case strings.HasSuffix(method, "/get"):
		var g struct {
			List []json.RawMessage `json:"list"`
		}
		if json.Unmarshal(resp, &g) == nil {
			attrs = append(attrs, slog.Int("result_count", len(g.List)))
		}
	}
	return attrs
}

// MultipleInvocations may be returned by a MethodHandler.Execute
// implementation when the method must produce more than one entry in
// the methodResponses array (e.g. EmailSubmission/set with
// onSuccessUpdateEmail per RFC 8621 §7.5). The dispatcher expands it
// in-place: the primary Invocation appears first, followed by each
// additional Invocation in order.
//
// The Primary Name is set by the dispatcher from call.Name; callers
// only need to supply Primary.Args and the extras.
type MultipleInvocations struct {
	// Primary is the main response. Name and CallID are set by the dispatcher.
	Primary json.RawMessage
	// Extra holds the additional [(name, args, callId), …] entries appended
	// after the primary response. Their CallID is typically the same as the
	// call that generated them.
	Extra []Invocation
}

// errorInvocation renders the standard JMAP error response shape.
func errorInvocation(callID string, e *MethodError) Invocation {
	body, _ := json.Marshal(e)
	return Invocation{Name: "error", Args: body, CallID: callID}
}

// capabilityListed reports whether cap appears in the request's
// "using" list. Empty cap (an unregistered method that somehow
// resolved) reports false to short-circuit safely.
func capabilityListed(using []CapabilityID, cap CapabilityID) bool {
	if cap == "" {
		return false
	}
	for _, c := range using {
		if c == cap {
			return true
		}
	}
	return false
}
