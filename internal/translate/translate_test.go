package translate_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/translate"
)

// newTestServer spins up an httptest.Server whose handler is set by the
// caller. The returned *http.Client is configured to route all requests
// through the test server via a custom transport. The server is
// registered for cleanup automatically.
func newTestServer(t *testing.T, h http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client := &http.Client{
		Transport: &http.Transport{},
	}
	// The test server's URL is used directly in Client.Config.Endpoint;
	// no custom transport override is needed since the endpoint already
	// points at the test server's http:// address.
	_ = client
	return srv, srv.Client()
}

// myMemoryOKResponse returns a valid MyMemory success response body.
func myMemoryOKResponse(translatedText, detectedLang string) string {
	return `{
		"responseStatus": 200,
		"responseData": {
			"translatedText": "` + translatedText + `",
			"detectedLanguage": "` + detectedLang + `"
		}
	}`
}

// myMemoryErrorResponse returns a MyMemory error response body.
func myMemoryErrorResponse(status int) string {
	b, _ := json.Marshal(map[string]any{
		"responseStatus": status,
		"responseData":   map[string]any{"translatedText": ""},
	})
	return string(b)
}

// deepLOKResponse returns a valid DeepL success response body.
func deepLOKResponse(text, detectedLang string) string {
	return `{
		"translations": [
			{"detected_source_language": "` + detectedLang + `", "text": "` + text + `"}
		]
	}`
}

// TestMyMemory_Success verifies that a well-formed MyMemory response is
// correctly parsed and the result fields are populated.
func TestMyMemory_Success(t *testing.T) {
	t.Parallel()
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		q := r.URL.Query()
		if q.Get("q") == "" {
			t.Error("missing q param")
		}
		if q.Get("langpair") == "" {
			t.Error("missing langpair param")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, myMemoryOKResponse("Hallo Welt", "EN"))
	}))

	c := translate.NewClient(translate.Config{
		Provider: "mymemory",
		Endpoint: srv.URL,
	}, hc)

	res, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "Hello World",
		TargetLang: "de",
		SourceLang: "en",
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if res.TranslatedText != "Hallo Welt" {
		t.Errorf("TranslatedText = %q, want %q", res.TranslatedText, "Hallo Welt")
	}
	if res.DetectedSourceLang != "EN" {
		t.Errorf("DetectedSourceLang = %q, want %q", res.DetectedSourceLang, "EN")
	}
}

// TestMyMemory_RequestShape verifies that the outgoing request carries the
// correct query parameters for all configured options.
func TestMyMemory_RequestShape(t *testing.T) {
	t.Parallel()
	var gotQuery url.Values
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		io.WriteString(w, myMemoryOKResponse("translated", "FR"))
	}))

	c := translate.NewClient(translate.Config{
		Provider:     "mymemory",
		Endpoint:     srv.URL,
		APIKey:       "test-key",
		ContactEmail: "admin@example.com",
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "bonjour",
		TargetLang: "de",
		SourceLang: "fr",
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if gotQuery.Get("q") != "bonjour" {
		t.Errorf("q = %q, want %q", gotQuery.Get("q"), "bonjour")
	}
	if gotQuery.Get("langpair") != "fr|de" {
		t.Errorf("langpair = %q, want %q", gotQuery.Get("langpair"), "fr|de")
	}
	if gotQuery.Get("key") != "test-key" {
		t.Errorf("key = %q, want %q", gotQuery.Get("key"), "test-key")
	}
	if gotQuery.Get("de") != "admin@example.com" {
		t.Errorf("de = %q, want %q", gotQuery.Get("de"), "admin@example.com")
	}
}

// TestMyMemory_AutodetectSource verifies that an empty SourceLang causes
// the langpair source to be "autodetect" (not the string "").
func TestMyMemory_AutodetectSource(t *testing.T) {
	t.Parallel()
	var gotLangpair string
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLangpair = r.URL.Query().Get("langpair")
		io.WriteString(w, myMemoryOKResponse("ok", ""))
	}))
	c := translate.NewClient(translate.Config{
		Provider: "mymemory",
		Endpoint: srv.URL,
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if !strings.HasPrefix(gotLangpair, "autodetect|") {
		t.Errorf("langpair = %q, want prefix %q", gotLangpair, "autodetect|")
	}
}

// TestMyMemory_DefaultSourceLang verifies that Config.DefaultSourceLang
// is used when TranslateRequest.SourceLang is empty.
func TestMyMemory_DefaultSourceLang(t *testing.T) {
	t.Parallel()
	var gotLangpair string
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLangpair = r.URL.Query().Get("langpair")
		io.WriteString(w, myMemoryOKResponse("ok", "EN"))
	}))
	c := translate.NewClient(translate.Config{
		Provider:          "mymemory",
		Endpoint:          srv.URL,
		DefaultSourceLang: "en",
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if !strings.HasPrefix(gotLangpair, "en|") {
		t.Errorf("langpair = %q, want prefix %q", gotLangpair, "en|")
	}
}

// TestMyMemory_NonOKResponseStatus verifies that a non-200 responseStatus
// in the JSON body surfaces as ErrUpstream.
func TestMyMemory_NonOKResponseStatus(t *testing.T) {
	t.Parallel()
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, myMemoryErrorResponse(429))
	}))
	c := translate.NewClient(translate.Config{
		Provider: "mymemory",
		Endpoint: srv.URL,
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if !errors.Is(err, translate.ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream", err)
	}
}

// TestMyMemory_HTTPError verifies that an HTTP-level error (non-2xx status)
// surfaces as ErrUpstream.
func TestMyMemory_HTTPError(t *testing.T) {
	t.Parallel()
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	c := translate.NewClient(translate.Config{
		Provider: "mymemory",
		Endpoint: srv.URL,
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if !errors.Is(err, translate.ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream", err)
	}
}

// TestMyMemory_MalformedJSON verifies that unparseable response bodies
// surface as ErrUpstream.
func TestMyMemory_MalformedJSON(t *testing.T) {
	t.Parallel()
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not-json{{{")
	}))
	c := translate.NewClient(translate.Config{
		Provider: "mymemory",
		Endpoint: srv.URL,
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if !errors.Is(err, translate.ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream", err)
	}
}

// TestDeepL_Success verifies that a well-formed DeepL response is
// correctly parsed.
func TestDeepL_Success(t *testing.T) {
	t.Parallel()
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected Content-Type: %s", r.Header.Get("Content-Type"))
		}
		io.WriteString(w, deepLOKResponse("Hallo Welt", "EN"))
	}))

	c := translate.NewClient(translate.Config{
		Provider: "deepl",
		Endpoint: srv.URL,
		APIKey:   "deepl-test-key",
	}, hc)
	res, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "Hello World",
		TargetLang: "DE",
		SourceLang: "EN",
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if res.TranslatedText != "Hallo Welt" {
		t.Errorf("TranslatedText = %q, want %q", res.TranslatedText, "Hallo Welt")
	}
	if res.DetectedSourceLang != "EN" {
		t.Errorf("DetectedSourceLang = %q, want %q", res.DetectedSourceLang, "EN")
	}
}

// TestDeepL_RequestShape verifies the outgoing DeepL request shape:
// POST with form body containing text, target_lang, and the
// Authorization header carrying the API key.
func TestDeepL_RequestShape(t *testing.T) {
	t.Parallel()
	var (
		gotAuth string
		gotForm url.Values
	)
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		r.ParseForm()
		gotForm = r.Form
		io.WriteString(w, deepLOKResponse("ok", "EN"))
	}))
	c := translate.NewClient(translate.Config{
		Provider: "deepl",
		Endpoint: srv.URL,
		APIKey:   "my-deepl-key",
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
		SourceLang: "en",
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if gotAuth != "DeepL-Auth-Key my-deepl-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "DeepL-Auth-Key my-deepl-key")
	}
	if gotForm.Get("text") != "hello" {
		t.Errorf("text = %q, want %q", gotForm.Get("text"), "hello")
	}
	if gotForm.Get("target_lang") != "DE" {
		t.Errorf("target_lang = %q, want %q", gotForm.Get("target_lang"), "DE")
	}
	if gotForm.Get("source_lang") != "EN" {
		t.Errorf("source_lang = %q, want %q", gotForm.Get("source_lang"), "EN")
	}
}

// TestDeepL_AutodetectSource verifies that an empty SourceLang causes
// the source_lang form field to be omitted from the DeepL request.
func TestDeepL_AutodetectSource(t *testing.T) {
	t.Parallel()
	var gotSourceLang string
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotSourceLang = r.FormValue("source_lang")
		io.WriteString(w, deepLOKResponse("ok", "EN"))
	}))
	c := translate.NewClient(translate.Config{
		Provider: "deepl",
		Endpoint: srv.URL,
		APIKey:   "key",
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
		// SourceLang intentionally empty
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if gotSourceLang != "" {
		t.Errorf("source_lang = %q, want empty (omitted)", gotSourceLang)
	}
}

// TestDeepL_HTTPError verifies that a non-2xx DeepL response surfaces as
// ErrUpstream.
func TestDeepL_HTTPError(t *testing.T) {
	t.Parallel()
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	c := translate.NewClient(translate.Config{
		Provider: "deepl",
		Endpoint: srv.URL,
		APIKey:   "bad-key",
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if !errors.Is(err, translate.ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream", err)
	}
}

// TestDeepL_EmptyTranslations verifies that an empty translations array
// surfaces as ErrUpstream.
func TestDeepL_EmptyTranslations(t *testing.T) {
	t.Parallel()
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"translations":[]}`)
	}))
	c := translate.NewClient(translate.Config{
		Provider: "deepl",
		Endpoint: srv.URL,
		APIKey:   "key",
	}, hc)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if !errors.Is(err, translate.ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream", err)
	}
}

// TestTextTooLong verifies that input exceeding MaxTextBytes is rejected
// before any network activity.
func TestTextTooLong(t *testing.T) {
	t.Parallel()
	var called bool
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	c := translate.NewClient(translate.Config{
		Provider: "mymemory",
		Endpoint: srv.URL,
	}, hc)

	longText := strings.Repeat("a", translate.MaxTextBytes+1)
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       longText,
		TargetLang: "de",
	})
	if !errors.Is(err, translate.ErrTextTooLong) {
		t.Errorf("err = %v, want ErrTextTooLong", err)
	}
	if called {
		t.Error("network request was made despite oversized input")
	}
}

// TestContextDeadline verifies that a cancelled context causes the
// translate call to return promptly with a context error.
func TestContextDeadline(t *testing.T) {
	t.Parallel()
	// Server that sleeps long enough that the client's context will
	// expire first.
	srv, hc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	c := translate.NewClient(translate.Config{
		Provider: "mymemory",
		Endpoint: srv.URL,
	}, hc)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Translate(ctx, translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	// The error wraps ErrUpstream because the network call failed.
	if !errors.Is(err, translate.ErrUpstream) {
		t.Errorf("err = %v, want wrapping ErrUpstream", err)
	}
}

// TestUnknownProvider verifies that a misconfigured provider name
// surfaces as ErrNotConfigured.
func TestUnknownProvider(t *testing.T) {
	t.Parallel()
	c := translate.NewClient(translate.Config{
		Provider: "acme-translate",
	}, &http.Client{})
	_, err := c.Translate(context.Background(), translate.TranslateRequest{
		Text:       "hello",
		TargetLang: "de",
	})
	if !errors.Is(err, translate.ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}
