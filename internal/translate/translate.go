package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// MaxTextBytes is the hard per-request input cap. Requests whose
	// Text field exceeds this limit are rejected before any network
	// activity to prevent accidental large-payload relay through the
	// translation proxy.
	MaxTextBytes = 50_000

	// DefaultMyMemoryEndpoint is the canonical MyMemory free API base URL.
	DefaultMyMemoryEndpoint = "https://api.mymemory.translated.net/get"

	// DefaultDeepLEndpoint is the canonical DeepL API free-tier base URL.
	DefaultDeepLEndpoint = "https://api-free.deepl.com/v2/translate"
)

// ErrNotConfigured is returned by Client.Translate when the provider is
// not set or unknown. Callers check with errors.Is.
var ErrNotConfigured = errors.New("translate: feature not configured")

// ErrTextTooLong is returned when the input text exceeds MaxTextBytes.
var ErrTextTooLong = errors.New("translate: text exceeds maximum allowed length")

// ErrUpstream is returned when the upstream translation API returns an
// error response or an unrecognisable body. Callers check with errors.Is;
// the wrapped message carries context for operator logging but MUST NOT
// be forwarded to end-user responses (it may contain provider details).
var ErrUpstream = errors.New("translate: upstream error")

// Config holds the operator-supplied configuration for a Client. All
// fields are resolved values (secrets already expanded); no secret
// references appear here.
type Config struct {
	// Provider is the backend name: "mymemory" or "deepl".
	Provider string
	// Endpoint overrides the provider's canonical base URL. When empty
	// the Client uses the provider's documented default.
	Endpoint string
	// APIKey is the resolved (plaintext) API key. Optional for MyMemory
	// (raises the anonymous rate limit when present), required for DeepL.
	// Never logged by this package.
	APIKey string
	// ContactEmail is the operator contact email forwarded to MyMemory
	// via the "de" query parameter to raise the anonymous daily limit.
	// Ignored for DeepL.
	ContactEmail string
	// DefaultSourceLang is the ISO-639-1 source language code used when
	// TranslateRequest.SourceLang is empty. For MyMemory, which always
	// requires a langpair source, an empty DefaultSourceLang causes the
	// client to use "autodetect" as the source component.
	DefaultSourceLang string
}

// TranslateRequest is the input to Client.Translate.
type TranslateRequest struct {
	// Text is the content to translate. Must not exceed MaxTextBytes.
	Text string
	// TargetLang is the ISO-639-1 (or IETF BCP-47 variant, depending on
	// provider) language code for the desired output.
	TargetLang string
	// SourceLang is the ISO-639-1 source language code. An empty value
	// requests autodetect from the provider.
	SourceLang string
}

// TranslateResult is the output of Client.Translate.
type TranslateResult struct {
	// TranslatedText is the translated content returned by the provider.
	TranslatedText string
	// DetectedSourceLang is the source language the provider detected.
	// May be empty when the provider does not report it or when the
	// caller supplied an explicit SourceLang.
	DetectedSourceLang string
}

// Client proxies translation requests to the configured upstream provider.
// It is safe for concurrent use; all state is read-only after construction.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient constructs a Client with the given configuration and HTTP client.
// The supplied *http.Client is used for all outbound requests; callers that
// want per-request timeout control should set a context deadline on each
// Translate call.
func NewClient(cfg Config, hc *http.Client) *Client {
	return &Client{cfg: cfg, http: hc}
}

// Translate sends req to the configured provider and returns the result.
// The context deadline, if set, bounds the outbound HTTP call; callers
// are responsible for injecting an appropriate deadline (STANDARDS §5).
//
// Errors:
//   - ErrTextTooLong when len(req.Text) > MaxTextBytes.
//   - ErrNotConfigured when the provider is unset or unrecognised.
//   - ErrUpstream when the provider returns a non-2xx HTTP status, a
//     non-200 responseStatus (MyMemory), or an undecodable body.
func (c *Client) Translate(ctx context.Context, req TranslateRequest) (TranslateResult, error) {
	if len(req.Text) > MaxTextBytes {
		return TranslateResult{}, ErrTextTooLong
	}
	switch c.cfg.Provider {
	case "mymemory":
		return c.translateMyMemory(ctx, req)
	case "deepl":
		return c.translateDeepL(ctx, req)
	default:
		return TranslateResult{}, fmt.Errorf("%w: provider %q", ErrNotConfigured, c.cfg.Provider)
	}
}

// endpoint returns the configured endpoint or the provider's default.
func (c *Client) endpoint(providerDefault string) string {
	if c.cfg.Endpoint != "" {
		return c.cfg.Endpoint
	}
	return providerDefault
}

// translateMyMemory implements the MyMemory GET API.
//
// Query params:
//   - q=<text>
//   - langpair=<source>|<target>
//   - de=<email> (contact email, raises anonymous daily limit)
//   - key=<api_key> (registered key)
//
// Response shape (simplified):
//
//	{
//	  "responseStatus": 200,
//	  "responseData": {
//	    "translatedText": "...",
//	    "detectedLanguage": "EN"
//	  }
//	}
func (c *Client) translateMyMemory(ctx context.Context, req TranslateRequest) (TranslateResult, error) {
	src := req.SourceLang
	if src == "" {
		src = c.cfg.DefaultSourceLang
	}
	if src == "" {
		src = "autodetect"
	}
	langpair := src + "|" + req.TargetLang

	qp := url.Values{}
	qp.Set("q", req.Text)
	qp.Set("langpair", langpair)
	if c.cfg.ContactEmail != "" {
		qp.Set("de", c.cfg.ContactEmail)
	}
	if c.cfg.APIKey != "" {
		qp.Set("key", c.cfg.APIKey)
	}

	endpoint := c.endpoint(DefaultMyMemoryEndpoint)
	rawURL := endpoint + "?" + qp.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return TranslateResult{}, fmt.Errorf("%w: build request: %v", ErrUpstream, err)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return TranslateResult{}, fmt.Errorf("%w: http: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TranslateResult{}, fmt.Errorf("%w: http status %d", ErrUpstream, resp.StatusCode)
	}

	var body struct {
		ResponseStatus any `json:"responseStatus"`
		ResponseData   struct {
			TranslatedText   string `json:"translatedText"`
			DetectedLanguage string `json:"detectedLanguage"`
		} `json:"responseData"`
	}
	lr := io.LimitReader(resp.Body, 1<<20) // 1 MiB cap on response
	if err := json.NewDecoder(lr).Decode(&body); err != nil {
		return TranslateResult{}, fmt.Errorf("%w: decode response: %v", ErrUpstream, err)
	}

	// responseStatus may be the integer 200 or the string "200".
	switch v := body.ResponseStatus.(type) {
	case float64:
		if int(v) != 200 {
			return TranslateResult{}, fmt.Errorf("%w: responseStatus %v", ErrUpstream, v)
		}
	case string:
		if v != "200" {
			return TranslateResult{}, fmt.Errorf("%w: responseStatus %q", ErrUpstream, v)
		}
	default:
		return TranslateResult{}, fmt.Errorf("%w: unexpected responseStatus type %T", ErrUpstream, v)
	}

	detected := body.ResponseData.DetectedLanguage
	// When the caller asked for autodetect and the provider echoes back
	// the "autodetect" string, treat that as no detected-language signal.
	if strings.EqualFold(detected, "autodetect") {
		detected = ""
	}

	return TranslateResult{
		TranslatedText:     body.ResponseData.TranslatedText,
		DetectedSourceLang: detected,
	}, nil
}

// translateDeepL implements the DeepL POST API.
//
// The request is form-encoded:
//
//	text=<text>&target_lang=<lang>[&source_lang=<lang>]
//
// with Authorization: DeepL-Auth-Key <api_key> header.
//
// Response shape (simplified):
//
//	{
//	  "translations": [
//	    {"detected_source_language": "EN", "text": "..."}
//	  ]
//	}
func (c *Client) translateDeepL(ctx context.Context, req TranslateRequest) (TranslateResult, error) {
	form := url.Values{}
	form.Set("text", req.Text)
	form.Set("target_lang", strings.ToUpper(req.TargetLang))
	if req.SourceLang != "" {
		form.Set("source_lang", strings.ToUpper(req.SourceLang))
	} else if c.cfg.DefaultSourceLang != "" {
		form.Set("source_lang", strings.ToUpper(c.cfg.DefaultSourceLang))
	}
	// When neither SourceLang nor DefaultSourceLang is set, omit source_lang
	// entirely so DeepL performs autodetect.

	endpoint := c.endpoint(DefaultDeepLEndpoint)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return TranslateResult{}, fmt.Errorf("%w: build request: %v", ErrUpstream, err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "DeepL-Auth-Key "+c.cfg.APIKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return TranslateResult{}, fmt.Errorf("%w: http: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TranslateResult{}, fmt.Errorf("%w: http status %d", ErrUpstream, resp.StatusCode)
	}

	var body struct {
		Translations []struct {
			DetectedSourceLanguage string `json:"detected_source_language"`
			Text                   string `json:"text"`
		} `json:"translations"`
	}
	lr := io.LimitReader(resp.Body, 1<<20)
	if err := json.NewDecoder(lr).Decode(&body); err != nil {
		return TranslateResult{}, fmt.Errorf("%w: decode response: %v", ErrUpstream, err)
	}
	if len(body.Translations) == 0 {
		return TranslateResult{}, fmt.Errorf("%w: empty translations array", ErrUpstream)
	}

	return TranslateResult{
		TranslatedText:     body.Translations[0].Text,
		DetectedSourceLang: body.Translations[0].DetectedSourceLanguage,
	}, nil
}
