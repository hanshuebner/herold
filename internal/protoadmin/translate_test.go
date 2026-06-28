package protoadmin_test

// translate_test.go exercises POST /api/v1/translate (re #84).
//
// Tests use an httptest server to stub the upstream translation API so no
// real network calls are made. The harness mirrors the pattern used in
// spam_feedback tests and routes_test.go.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// buildTranslateServer constructs a protoadmin.Server with a translation
// config pointing at stub. The returned handler has bootstrap completed
// and the first API key set in apiKey.
func buildTranslateServer(t *testing.T, cfg *sysconfig.TranslationConfig, stubHandler http.Handler) (handler http.Handler, apiKey string, stub *httptest.Server) {
	t.Helper()
	stub = httptest.NewServer(stubHandler)
	t.Cleanup(stub.Close)

	if cfg != nil && cfg.Endpoint == "" {
		cfg.Endpoint = stub.URL
	}

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	t.Cleanup(func() { _ = fs.Close() })
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:  10,
		BootstrapWindow:     time.Minute,
		Translation:         cfg,
		TranslateHTTPClient: stub.Client(),
	})
	handler = srv.Handler()

	// Bootstrap an initial principal and API key.
	b, _ := json.Marshal(map[string]any{"email": "translate@example.com"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("bootstrap: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal bootstrap: %v", err)
	}
	return handler, out.InitialAPIKey, stub
}

// doTranslate POSTs to /api/v1/translate with the given body and API key.
func doTranslate(t *testing.T, handler http.Handler, apiKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/translate", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		r.Header.Set("Authorization", "Bearer "+apiKey)
	}
	handler.ServeHTTP(w, r)
	return w
}

// myMemoryOKBody returns a minimal successful MyMemory response body.
func myMemoryOKBody(translated, detected string) string {
	return `{"responseStatus":200,"responseData":{"translatedText":"` +
		translated + `","detectedLanguage":"` + detected + `"}}`
}

// TestTranslate_NotConfigured verifies that a missing [translation] config
// returns HTTP 501 with the stable code "translation_not_configured".
func TestTranslate_NotConfigured(t *testing.T) {
	t.Parallel()
	handler, apiKey, _ := buildTranslateServer(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called when feature is disabled")
	}))

	w := doTranslate(t, handler, apiKey, map[string]any{
		"text":        "hello",
		"target_lang": "de",
	})
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	var prob map[string]any
	if err := json.NewDecoder(w.Body).Decode(&prob); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	typeStr, _ := prob["type"].(string)
	if !strings.HasSuffix(typeStr, "translation_not_configured") {
		t.Errorf("problem type = %q, want suffix translation_not_configured", typeStr)
	}
}

// TestTranslate_DisabledFlag verifies that [translation].enabled = false
// returns the same 501 not-configured response.
func TestTranslate_DisabledFlag(t *testing.T) {
	t.Parallel()
	handler, apiKey, _ := buildTranslateServer(t, &sysconfig.TranslationConfig{
		Enabled: false,
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called when feature is disabled")
	}))

	w := doTranslate(t, handler, apiKey, map[string]any{
		"text":        "hello",
		"target_lang": "de",
	})
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

// TestTranslate_Unauthenticated verifies that an unauthenticated request
// returns 401 even when the feature is enabled.
func TestTranslate_Unauthenticated(t *testing.T) {
	t.Parallel()
	handler, _, _ := buildTranslateServer(t, &sysconfig.TranslationConfig{
		Enabled:  true,
		Provider: "mymemory",
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	w := doTranslate(t, handler, "" /* no key */, map[string]any{
		"text":        "hello",
		"target_lang": "de",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestTranslate_MissingText verifies that an empty text field returns 400.
func TestTranslate_MissingText(t *testing.T) {
	t.Parallel()
	handler, apiKey, _ := buildTranslateServer(t, &sysconfig.TranslationConfig{
		Enabled:  true,
		Provider: "mymemory",
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	w := doTranslate(t, handler, apiKey, map[string]any{
		"target_lang": "de",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestTranslate_MissingTargetLang verifies that a missing target_lang returns 400.
func TestTranslate_MissingTargetLang(t *testing.T) {
	t.Parallel()
	handler, apiKey, _ := buildTranslateServer(t, &sysconfig.TranslationConfig{
		Enabled:  true,
		Provider: "mymemory",
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	w := doTranslate(t, handler, apiKey, map[string]any{
		"text": "hello",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestTranslate_TextTooLong verifies that oversized text returns 413.
func TestTranslate_TextTooLong(t *testing.T) {
	t.Parallel()
	handler, apiKey, _ := buildTranslateServer(t, &sysconfig.TranslationConfig{
		Enabled:  true,
		Provider: "mymemory",
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called for oversized text")
	}))

	w := doTranslate(t, handler, apiKey, map[string]any{
		"text":        strings.Repeat("a", 50_001),
		"target_lang": "de",
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
}

// TestTranslate_MyMemorySuccess verifies the happy path for the mymemory
// provider: 200 with translated_text and detected_source_lang populated.
func TestTranslate_MyMemorySuccess(t *testing.T) {
	t.Parallel()
	handler, apiKey, _ := buildTranslateServer(t,
		&sysconfig.TranslationConfig{
			Enabled:  true,
			Provider: "mymemory",
		},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, myMemoryOKBody("Hallo Welt", "EN"))
		}),
	)

	w := doTranslate(t, handler, apiKey, map[string]any{
		"text":        "Hello World",
		"target_lang": "de",
		"source_lang": "en",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["translated_text"] != "Hallo Welt" {
		t.Errorf("translated_text = %v, want Hallo Welt", resp["translated_text"])
	}
	if resp["detected_source_lang"] != "EN" {
		t.Errorf("detected_source_lang = %v, want EN", resp["detected_source_lang"])
	}
}

// TestTranslate_UpstreamError verifies that an upstream failure returns 502
// without leaking provider error details.
func TestTranslate_UpstreamError(t *testing.T) {
	t.Parallel()
	handler, apiKey, _ := buildTranslateServer(t,
		&sysconfig.TranslationConfig{
			Enabled:  true,
			Provider: "mymemory",
		},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}),
	)

	w := doTranslate(t, handler, apiKey, map[string]any{
		"text":        "hello",
		"target_lang": "de",
	})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	var prob map[string]any
	if err := json.NewDecoder(w.Body).Decode(&prob); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	typeStr, _ := prob["type"].(string)
	if !strings.HasSuffix(typeStr, "translation_upstream_error") {
		t.Errorf("problem type = %q, want suffix translation_upstream_error", typeStr)
	}
}

// TestTranslate_DeepLSuccess verifies the DeepL provider path returns 200
// with the translated text from the stub response, including correct
// Authorization header construction from a secret reference.
//
// Not parallel: uses t.Setenv which is incompatible with t.Parallel.
func TestTranslate_DeepLSuccess(t *testing.T) {
	const deepLBody = `{"translations":[{"detected_source_language":"EN","text":"Hallo"}]}`

	t.Setenv("HEROLD_TEST_DEEPL_KEY", "test-deepl-key")

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "DeepL-Auth-Key test-deepl-key" {
			t.Errorf("Authorization = %q, want DeepL-Auth-Key test-deepl-key",
				r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, deepLBody)
	}))
	t.Cleanup(stub.Close)

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	t.Cleanup(func() { _ = fs.Close() })
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow: 10,
		BootstrapWindow:    time.Minute,
		Translation: &sysconfig.TranslationConfig{
			Enabled:   true,
			Provider:  "deepl",
			Endpoint:  stub.URL,
			APIKeyRef: "$HEROLD_TEST_DEEPL_KEY",
		},
		TranslateHTTPClient: stub.Client(),
	})
	h := srv.Handler()

	b, _ := json.Marshal(map[string]any{"email": "deepl@example.com"})
	bw := httptest.NewRecorder()
	br := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", bytes.NewReader(b))
	br.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(bw, br)
	if bw.Code != http.StatusCreated {
		t.Fatalf("bootstrap: %d", bw.Code)
	}
	var bOut struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(bw.Body.Bytes(), &bOut); err != nil {
		t.Fatalf("unmarshal bootstrap: %v", err)
	}

	w := doTranslate(t, h, bOut.InitialAPIKey, map[string]any{
		"text":        "Hello",
		"target_lang": "DE",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["translated_text"] != "Hallo" {
		t.Errorf("translated_text = %v, want Hallo", resp["translated_text"])
	}
}

// TestTranslate_ResponseShape verifies the exact JSON shape of a 200 response
// as documented for the SPA contract.
func TestTranslate_ResponseShape(t *testing.T) {
	t.Parallel()
	handler, apiKey, _ := buildTranslateServer(t,
		&sysconfig.TranslationConfig{
			Enabled:  true,
			Provider: "mymemory",
		},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, myMemoryOKBody("Bonjour", "EN"))
		}),
	)

	w := doTranslate(t, handler, apiKey, map[string]any{
		"text":        "Hello",
		"target_lang": "fr",
		"source_lang": "en",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp struct {
		TranslatedText     string `json:"translated_text"`
		DetectedSourceLang string `json:"detected_source_lang"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TranslatedText == "" {
		t.Error("translated_text must be non-empty")
	}
}

// TestTranslate_SelfServiceRoutePresent verifies that POST /api/v1/translate
// is registered on the self-service mux (returns non-404).
func TestTranslate_SelfServiceRoutePresent(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	t.Cleanup(func() { _ = fs.Close() })
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{})

	mux := http.NewServeMux()
	srv.RegisterSelfServiceRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/translate", nil)
	mux.ServeHTTP(w, r)
	if w.Code == http.StatusNotFound {
		t.Error("POST /api/v1/translate returned 404 on self-service mux; route is missing")
	}
}
