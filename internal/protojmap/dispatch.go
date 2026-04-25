package protojmap

import (
	"context"
	"encoding/json"
	"errors"
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
		entry := s.dispatchOne(ctx, call, resp.MethodResponses, req.Using)
		resp.MethodResponses = append(resp.MethodResponses, entry)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	if err := enc.Encode(resp); err != nil {
		s.log.Warn("protojmap.api.encode_failed", "err", err)
	}
}

// dispatchOne resolves and executes one method call. Errors are
// rendered as ["error", <body>, <callId>] entries per RFC 8620 §3.6.2;
// successful executions yield ["<method>", <response>, <callId>]. The
// returned Invocation is appended to the response envelope by the
// caller. prior is the slice of already-rendered response invocations
// — back-references resolve against it.
func (s *Server) dispatchOne(ctx context.Context, call Invocation, prior []Invocation, using []CapabilityID) Invocation {
	handler, ok := s.reg.Resolve(call.Name)
	if !ok {
		return errorInvocation(call.CallID, NewMethodError("unknownMethod",
			"no handler registered for "+call.Name))
	}
	// Verify the request "using" array names the capability owning
	// this method (RFC 8620 §3.6.1). Without this check a client
	// could call Email/get without listing the Mail capability, which
	// would defeat the negotiation surface.
	cap, _ := s.reg.CapabilityFor(call.Name)
	if !capabilityListed(using, cap) {
		return errorInvocation(call.CallID, NewMethodError("unknownMethod",
			"method "+call.Name+" requires capability "+string(cap)+" in 'using'"))
	}
	args, refErr := resolveBackReferences(call.Args, prior)
	if refErr != nil {
		return errorInvocation(call.CallID, refErr)
	}
	resp, mErr := handler.Execute(ctx, args)
	if mErr != nil {
		return errorInvocation(call.CallID, mErr)
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		return errorInvocation(call.CallID, NewMethodError("serverFail",
			"response marshal: "+err.Error()))
	}
	return Invocation{Name: call.Name, Args: respBytes, CallID: call.CallID}
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
