package chat

// Directory/search — compose-window address autocomplete.
//
// The method is gated on CapabilityDirectoryAutocomplete (advertised when
// [server.directory_autocomplete].mode != "off") and returns an inline list
// of {id, email, displayName} records matching a text prefix. It
// intentionally does NOT use the query+get two-round-trip pattern:
// autocomplete needs one round-trip per keystroke.
//
// Privacy contract: identical to Principal/get and Principal/query —
// only id, email, and displayName are ever returned.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// directorySearchMaxLimit is the server-side hard cap on Directory/search
// results.
const directorySearchMaxLimit = 25

// directorySearchDefaultLimit is the default when the caller omits limit.
const directorySearchDefaultLimit = 10

// -- Wire types -------------------------------------------------------

type directorySearchRequest struct {
	AccountID  jmapID `json:"accountId"`
	TextPrefix string `json:"textPrefix"`
	Limit      *int   `json:"limit"`
}

// directorySearchItem is one entry in the Directory/search response.
type directorySearchItem struct {
	ID          jmapID `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

type directorySearchResponse struct {
	AccountID string                `json:"accountId"`
	Items     []directorySearchItem `json:"items"`
}

// -- Directory/search -------------------------------------------------

// directorySearchHandler implements the Directory/search JMAP method.
// It is registered under CapabilityDirectoryAutocomplete (not the chat
// capability), so clients include that cap in "using" whether or not they
// also have chat in "using".
type directorySearchHandler struct {
	store  store.Store
	modeFn func() sysconfig.DirectoryAutocompleteMode
}

func (h *directorySearchHandler) Method() string { return "Directory/search" }

func (h *directorySearchHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}

	var req directorySearchRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}

	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	// Defence-in-depth: if mode is "off" the capability is not advertised,
	// so this handler should not be reachable under normal operation. Treat
	// a forced invocation the same way the dispatcher would treat a missing
	// capability.
	mode := h.modeFn()
	if mode == sysconfig.DirectoryAutocompleteModeOff {
		return nil, protojmap.NewMethodError("unknownMethod",
			"Directory/search requires the directory-autocomplete capability")
	}

	// Validate textPrefix.
	prefix := strings.TrimSpace(req.TextPrefix)
	if prefix == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "textPrefix must be non-empty")
	}

	// Validate and clamp limit.
	limit := directorySearchDefaultLimit
	if req.Limit != nil {
		if *req.Limit <= 0 {
			return nil, protojmap.NewMethodError("invalidArguments", "limit must be a positive integer")
		}
		limit = *req.Limit
		if limit > directorySearchMaxLimit {
			limit = directorySearchMaxLimit
		}
	}

	resp := directorySearchResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		Items:     []directorySearchItem{},
	}

	switch mode {
	case sysconfig.DirectoryAutocompleteModeAll:
		// Empty domain string falls through to the unrestricted search path.
		principals, err := h.store.Meta().SearchPrincipalsByTextInDomain(ctx, prefix, "", limit)
		if err != nil {
			return nil, serverFail(err)
		}
		for _, p := range principals {
			rp := renderPrincipal(p, nil)
			resp.Items = append(resp.Items, directorySearchItem{
				ID:          rp.ID,
				Email:       rp.Email,
				DisplayName: rp.DisplayName,
			})
		}

	case sysconfig.DirectoryAutocompleteModeDomain:
		// Derive domain from the caller's canonical email.
		callerPrincipal, err := h.store.Meta().GetPrincipalByID(ctx, pid)
		if err != nil {
			return nil, serverFail(err)
		}
		atIdx := strings.LastIndexByte(callerPrincipal.CanonicalEmail, '@')
		if atIdx < 0 {
			return nil, protojmap.NewMethodError("serverFail",
				"caller canonical email has no domain component")
		}
		domain := callerPrincipal.CanonicalEmail[atIdx+1:]

		principals, err := h.store.Meta().SearchPrincipalsByTextInDomain(ctx, prefix, domain, limit)
		if err != nil {
			return nil, serverFail(err)
		}
		for _, p := range principals {
			rp := renderPrincipal(p, nil)
			resp.Items = append(resp.Items, directorySearchItem{
				ID:          rp.ID,
				Email:       rp.Email,
				DisplayName: rp.DisplayName,
			})
		}
	}

	return resp, nil
}

// RegisterDirectorySearch installs the Directory/search handler under
// CapabilityDirectoryAutocomplete. It is called separately from the main
// chat Register so that the handler is reachable whenever the
// directory-autocomplete capability is in the session, regardless of
// whether the chat capability is also present.
//
// modeFn returns the current DirectoryAutocompleteMode at call time so
// that an operator config reload (SIGHUP) takes effect on the next
// request without a server restart.
func RegisterDirectorySearch(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	modeFn func() sysconfig.DirectoryAutocompleteMode,
) {
	reg.Register(protojmap.CapabilityDirectoryAutocomplete,
		&directorySearchHandler{store: st, modeFn: modeFn})
}
