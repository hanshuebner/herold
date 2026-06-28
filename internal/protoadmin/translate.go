package protoadmin

// translate.go implements POST /api/v1/translate — an operator opt-in
// server-side proxy to a hosted translation API (re #84).
//
// The endpoint is a stateless proxy: it does not persist any text or
// result. The operator enables the feature via [translation].enabled in
// system.toml; when the block is absent or disabled the endpoint returns
// HTTP 501 with the stable machine-readable problem code
// "translation_not_configured" so the Suite SPA can detect support and
// hide the translation affordance when the feature is off.
//
// Auth: any authenticated principal (auth1 gate, no admin elevation, no
// store ownership check). The user supplies the text; the server forwards
// it to the configured provider.
//
// Security: the message text and the resolved api_key are NEVER logged.
// Upstream failure details are absorbed into a generic 502 problem so
// provider internals do not leak to end-user responses.

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/translate"
)

// translateRequest is the JSON body for POST /api/v1/translate.
type translateRequest struct {
	Text       string `json:"text"`
	TargetLang string `json:"target_lang"`
	SourceLang string `json:"source_lang,omitempty"`
}

// translateResponse is the JSON body returned on a 200 OK.
type translateResponse struct {
	TranslatedText     string `json:"translated_text"`
	DetectedSourceLang string `json:"detected_source_lang,omitempty"`
}

// defaultTranslateTimeout is the per-request budget for the upstream
// translation API call. Operators wanting a different ceiling can set a
// context deadline before calling the proxy; this default prevents runaway
// requests when the upstream is slow.
const defaultTranslateTimeout = 10 * time.Second

// handleTranslate implements POST /api/v1/translate (re #84).
//
// The caller provides a JSON body with text and target_lang; the server
// forwards the request to the configured translation provider and returns
// the translated text. The feature must be enabled in system.toml via
// [translation].enabled = true; without this the endpoint returns 501.
func (s *Server) handleTranslate(w http.ResponseWriter, r *http.Request) {
	caller, ok := principalFrom(r.Context())
	if !ok || caller.ID == 0 {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
			"authentication is required", "")
		return
	}

	// Feature gate: return a stable machine-readable 501 when the operator
	// has not enabled the translation proxy. The SPA uses this code to
	// suppress the translation UI affordance entirely (re #84).
	cfg := s.opts.Translation
	if cfg == nil || !cfg.TranslationEnabled() {
		writeProblem(w, r, http.StatusNotImplemented, "translation_not_configured",
			"the translation feature is not enabled on this server", "")
		return
	}

	var req translateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if req.Text == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"text is required", "")
		return
	}
	if req.TargetLang == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"target_lang is required", "")
		return
	}
	if len(req.Text) > translate.MaxTextBytes {
		writeProblem(w, r, http.StatusRequestEntityTooLarge, "text_too_long",
			"text exceeds the maximum allowed length", "")
		return
	}

	// Resolve the api_key secret reference at request time. An empty
	// APIKeyRef is valid for the mymemory provider; ResolveSecretStrict
	// returns an error on an empty string so we gate on non-empty.
	apiKey := ""
	if cfg.APIKeyRef != "" {
		var err error
		apiKey, err = sysconfig.ResolveSecretStrict(cfg.APIKeyRef)
		if err != nil {
			// Secret resolution failure is an operator misconfiguration,
			// not a user error. Log internally; return a 502 to the caller.
			s.logger.ErrorContext(r.Context(), "translate: secret resolution failed",
				"activity", observe.ActivityInternal,
				"err", err.Error())
			writeProblem(w, r, http.StatusBadGateway, "translation_upstream_error",
				"translation service is temporarily unavailable", "")
			return
		}
	}

	hc := s.opts.TranslateHTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultTranslateTimeout}
	}

	client := translate.NewClient(translate.Config{
		Provider:          cfg.Provider,
		Endpoint:          cfg.Endpoint,
		APIKey:            apiKey,
		ContactEmail:      cfg.ContactEmail,
		DefaultSourceLang: cfg.DefaultSourceLang,
	}, hc)

	ctx := r.Context()
	result, err := client.Translate(ctx, translate.TranslateRequest{
		Text:       req.Text,
		TargetLang: req.TargetLang,
		SourceLang: req.SourceLang,
	})
	if err != nil {
		if errors.Is(err, translate.ErrTextTooLong) {
			// Should not reach here (checked above), but handle defensively.
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "text_too_long",
				"text exceeds the maximum allowed length", "")
			return
		}
		// Do not forward provider error details (may contain key material
		// or provider-internal messages). Log internally at info with
		// activity=user so the operator can correlate the 502 with a
		// provider-side rate limit or outage (STANDARDS §7).
		s.logger.InfoContext(r.Context(), "translate: upstream error",
			slog.String("activity", observe.ActivityUser),
			slog.String("provider", cfg.Provider),
			slog.String("target_lang", req.TargetLang))
		writeProblem(w, r, http.StatusBadGateway, "translation_upstream_error",
			"translation service returned an error", "")
		return
	}

	s.logger.InfoContext(r.Context(), "translate: request completed",
		slog.String("activity", observe.ActivityUser),
		slog.String("provider", cfg.Provider),
		slog.String("target_lang", req.TargetLang),
		slog.String("detected_source_lang", result.DetectedSourceLang))

	writeJSON(w, http.StatusOK, translateResponse{
		TranslatedText:     result.TranslatedText,
		DetectedSourceLang: result.DetectedSourceLang,
	})
}
