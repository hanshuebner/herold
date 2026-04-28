package protojmap

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
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
	resp := Response{
		MethodResponses: make([]Invocation, 0, len(req.MethodCalls)),
		SessionState:    s.sessionState(ctx),
		CreatedIDs:      req.CreatedIDs,
	}
	for _, call := range req.MethodCalls {
		entries := s.dispatchOneMulti(ctx, call, resp.MethodResponses, req.Using, req.CreatedIDs)
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
func (s *Server) dispatchOneMulti(ctx context.Context, call Invocation, prior []Invocation, using []CapabilityID, requestCreatedIDs map[Id]Id) []Invocation {
	handler, ok := s.reg.Resolve(call.Name)
	if !ok {
		return []Invocation{errorInvocation(call.CallID, NewMethodError("unknownMethod",
			"no handler registered for "+call.Name))}
	}
	cap, _ := s.reg.CapabilityFor(call.Name)
	if !capabilityListed(using, cap) {
		return []Invocation{errorInvocation(call.CallID, NewMethodError("unknownMethod",
			"method "+call.Name+" requires capability "+string(cap)+" in 'using'"))}
	}
	args, refErr := resolveBackReferences(call.Args, prior)
	if refErr != nil {
		return []Invocation{errorInvocation(call.CallID, refErr)}
	}
	creations := gatherCreations(prior, requestCreatedIDs)
	args, refErr = resolveCreationReferences(args, creations)
	if refErr != nil {
		return []Invocation{errorInvocation(call.CallID, refErr)}
	}
	resp, mErr := handler.Execute(ctx, args)
	if mErr != nil {
		return []Invocation{errorInvocation(call.CallID, mErr)}
	}
	// Check for MultipleInvocations before marshalling.
	if multi, ok := resp.(MultipleInvocations); ok {
		primary := Invocation{Name: call.Name, Args: multi.Primary, CallID: call.CallID}
		return append([]Invocation{primary}, multi.Extra...)
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		return []Invocation{errorInvocation(call.CallID, NewMethodError("serverFail",
			"response marshal: "+err.Error()))}
	}
	return []Invocation{{Name: call.Name, Args: respBytes, CallID: call.CallID}}
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
