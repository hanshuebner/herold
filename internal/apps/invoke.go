package apps

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// Invocation request and response bounds (REQ-APP-43, REQ-APP-44).
const (
	// MaxResponseBytes bounds the app's response body.
	MaxResponseBytes = 64 * 1024
	// MaxTextRunes bounds the notice and error texts passed to the user.
	MaxTextRunes = 500
	// SignatureTolerance is the clock skew apps accept on Herold-Signature.
	SignatureTolerance = 300 * time.Second
)

// Wire header names (REQ-APP-43).
const (
	HeaderSignature    = "Herold-Signature"
	HeaderInvocationID = "Herold-Invocation-Id"
	HeaderAppAPI       = "Herold-App-Api"
)

// Response types an app may answer with (REQ-APP-44).
const (
	ResponseOpenURL = "open_url"
	ResponseNotice  = "notice"
)

// Party is a user or account identity in the invocation context.
type Party struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
}

// ThreadInfo describes the invoked thread in the invocation context.
type ThreadInfo struct {
	ID           string `json:"id"`
	Subject      string `json:"subject"`
	MessageCount int    `json:"message_count"`
}

// MessageInfo describes one message part of the invocation.
type MessageInfo struct {
	Index      int    `json:"index"`
	ReceivedAt string `json:"received_at"`
	Size       int64  `json:"size"`
}

// InvocationContext is the JSON "context" part of the request body
// (REQ-APP-41).
type InvocationContext struct {
	HeroldApp    int           `json:"herold_app"`
	InvocationID string        `json:"invocation_id"`
	IssuedAt     string        `json:"issued_at"`
	AppKey       string        `json:"app_key"`
	ExtensionID  string        `json:"extension_id"`
	User         Party         `json:"user"`
	Account      Party         `json:"account"`
	Thread       ThreadInfo    `json:"thread"`
	Messages     []MessageInfo `json:"messages"`
}

// MessageSource streams the invocation's messages in export order,
// calling fn once per message with its 1-based index and raw bytes.
type MessageSource func(ctx context.Context, fn func(index int, r io.Reader) error) error

// Invocation is one signed POST to an app endpoint.
type Invocation struct {
	// Origin is the app's normalised origin.
	Origin string
	// Endpoint is the extension's absolute path under Origin.
	Endpoint string
	// Key is the HMAC key: the secret's base64url text as presented to
	// the administrator (REQ-APP-43).
	Key []byte
	// Context is the context part. HeroldApp and IssuedAt are filled in
	// by Invoke when empty.
	Context InvocationContext
	// Messages streams the message parts; nil sends the context part
	// only.
	Messages MessageSource
	// Now is the signing instant.
	Now time.Time
}

// Result is a validated app response (REQ-APP-44).
type Result struct {
	// Type is ResponseOpenURL or ResponseNotice.
	Type string
	// URL is set for ResponseOpenURL.
	URL string
	// Text is set for ResponseNotice.
	Text string
	// Status is the app's HTTP status.
	Status int
	// RequestBytes is the size of the request body sent.
	RequestBytes int64
}

// ErrorKind classifies a failed invocation (REQ-APP-44, REQ-APP-51).
type ErrorKind string

// Invocation failure kinds.
const (
	// ErrorRejected: the app answered non-2xx with an error text, or 4xx.
	ErrorRejected ErrorKind = "appRejected"
	// ErrorUnavailable: timeout, transport error, invalid response, or
	// 5xx without an error text.
	ErrorUnavailable ErrorKind = "appUnavailable"
)

// InvokeError reports a failed invocation.
type InvokeError struct {
	Kind ErrorKind
	// Status is the app's HTTP status, 0 when no response arrived.
	Status int
	// Text is the app's error text (ErrorRejected only), plain text.
	Text string
	// Reason is an operator-facing description for logs.
	Reason string
	// RequestBytes is the size of the request body, when it was built.
	RequestBytes int64
	// Timeout reports that the request ran out of time.
	Timeout bool
}

func (e *InvokeError) Error() string {
	return fmt.Sprintf("apps: invocation %s: %s", e.Kind, e.Reason)
}

// NewInvocationID returns a random 128-bit hex identifier.
func NewInvocationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("apps: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// newSigner returns the HMAC state for timestamp t with the
// "v1:<t>:" prefix already written; the body bytes follow.
func newSigner(key []byte, t int64) hash.Hash {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "v1:%d:", t)
	return mac
}

// SignatureHeader renders the Herold-Signature value for body signed
// with key at unix time t (REQ-APP-43).
func SignatureHeader(key []byte, t int64, body []byte) string {
	mac := newSigner(key, t)
	mac.Write(body)
	return formatSignature(t, mac.Sum(nil))
}

func formatSignature(t int64, sum []byte) string {
	return "t=" + strconv.FormatInt(t, 10) + ",v1=" + hex.EncodeToString(sum)
}

// countingWriter counts bytes written through it.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// writeBody renders the multipart body (REQ-APP-41) to w with a fixed
// boundary, so two renderings of one invocation are byte-identical.
func writeBody(ctx context.Context, w io.Writer, boundary string, contextJSON []byte, src MessageSource, count int) error {
	mw := multipart.NewWriter(w)
	if err := mw.SetBoundary(boundary); err != nil {
		return err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="context"`)
	h.Set("Content-Type", "application/json")
	pw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := pw.Write(contextJSON); err != nil {
		return err
	}
	if src != nil {
		width := len(strconv.Itoa(count))
		if width < 2 {
			width = 2
		}
		err := src(ctx, func(index int, r io.Reader) error {
			mh := textproto.MIMEHeader{}
			mh.Set("Content-Disposition",
				fmt.Sprintf(`form-data; name="message[]"; filename="%0*d.eml"`, width, index))
			mh.Set("Content-Type", "message/rfc822")
			part, err := mw.CreatePart(mh)
			if err != nil {
				return err
			}
			_, err = io.Copy(part, r)
			return err
		})
		if err != nil {
			return err
		}
	}
	return mw.Close()
}

// ErrBodyBuild wraps an error raised while rendering the request body
// (for example the export's byte bound); Invoke returns it unwrapped
// from InvokeError so callers can map it to their own error.
var ErrBodyBuild = errors.New("apps: build request body")

// Invoke signs and POSTs inv to inv.Origin+inv.Endpoint and validates
// the response (REQ-APP-41..44). The body is rendered twice, once to
// compute the signature and length and once onto the wire, so message
// bytes stream from the source and are never held in memory together.
//
// Errors raised by inv.Messages while computing the signature are
// returned wrapped in ErrBodyBuild before any byte is sent; every
// other failure is an *InvokeError.
func (c *Client) Invoke(ctx context.Context, inv Invocation) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if inv.Context.HeroldApp == 0 {
		inv.Context.HeroldApp = ManifestAPIVersion
	}
	if inv.Context.IssuedAt == "" {
		inv.Context.IssuedAt = inv.Now.UTC().Format(time.RFC3339)
	}
	if inv.Context.Messages == nil {
		inv.Context.Messages = []MessageInfo{}
	}
	contextJSON, err := json.Marshal(inv.Context)
	if err != nil {
		return Result{}, fmt.Errorf("%w: context: %v", ErrBodyBuild, err)
	}
	boundary := "herold-" + NewInvocationID()
	count := len(inv.Context.Messages)

	t := inv.Now.Unix()
	mac := newSigner(inv.Key, t)
	var cw countingWriter
	if err := writeBody(ctx, io.MultiWriter(mac, &cw), boundary, contextJSON, inv.Messages, count); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrBodyBuild, err)
	}
	size := cw.n
	sig := formatSignature(t, mac.Sum(nil))

	hc, err := c.httpClient(inv.Origin)
	if err != nil {
		return Result{}, &InvokeError{Kind: ErrorUnavailable, Reason: err.Error(), RequestBytes: size}
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(writeBody(ctx, pw, boundary, contextJSON, inv.Messages, count))
	}()
	defer pr.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inv.Origin+inv.Endpoint, pr)
	if err != nil {
		return Result{}, &InvokeError{Kind: ErrorUnavailable, Reason: err.Error(), RequestBytes: size}
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set(HeaderSignature, sig)
	req.Header.Set(HeaderInvocationID, inv.Context.InvocationID)
	req.Header.Set(HeaderAppAPI, strconv.Itoa(ManifestAPIVersion))

	resp, err := hc.Do(req)
	if err != nil {
		return Result{}, &InvokeError{Kind: ErrorUnavailable, Reason: err.Error(),
			RequestBytes: size, Timeout: errorIsTimeout(err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return Result{}, &InvokeError{Kind: ErrorUnavailable, Status: resp.StatusCode,
			Reason: "read response: " + err.Error(), RequestBytes: size, Timeout: errorIsTimeout(err)}
	}
	res, ierr := c.validateResponse(inv.Origin, resp, body)
	if ierr != nil {
		ierr.Status = resp.StatusCode
		ierr.RequestBytes = size
		return Result{}, ierr
	}
	res.Status = resp.StatusCode
	res.RequestBytes = size
	return res, nil
}

type wireResponse struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Text  string `json:"text"`
	Error string `json:"error"`
}

// validateResponse applies REQ-APP-44 to one app response.
func (c *Client) validateResponse(origin string, resp *http.Response, body []byte) (Result, *InvokeError) {
	unavailable := func(format string, args ...any) *InvokeError {
		return &InvokeError{Kind: ErrorUnavailable, Reason: fmt.Sprintf(format, args...)}
	}
	if len(body) > MaxResponseBytes {
		return Result{}, unavailable("response exceeds %d bytes", MaxResponseBytes)
	}
	status := resp.StatusCode
	if status < 200 || status > 299 {
		var wr wireResponse
		text := ""
		if isJSONContentType(resp.Header.Get("Content-Type")) && json.Unmarshal(body, &wr) == nil {
			text = PlainText(wr.Error)
		}
		if status >= 400 && status <= 499 || text != "" {
			return Result{}, &InvokeError{Kind: ErrorRejected, Text: text,
				Reason: fmt.Sprintf("app answered %d", status)}
		}
		return Result{}, unavailable("app answered %d", status)
	}
	if !isJSONContentType(resp.Header.Get("Content-Type")) {
		return Result{}, unavailable("response Content-Type %q is not application/json", resp.Header.Get("Content-Type"))
	}
	var wr wireResponse
	if err := json.Unmarshal(body, &wr); err != nil {
		return Result{}, unavailable("response is not JSON: %v", err)
	}
	switch wr.Type {
	case ResponseOpenURL:
		u, err := c.checkOpenURL(origin, wr.URL)
		if err != nil {
			return Result{}, unavailable("open_url: %v", err)
		}
		return Result{Type: ResponseOpenURL, URL: u}, nil
	case ResponseNotice:
		text := PlainText(wr.Text)
		if text == "" {
			return Result{}, unavailable("notice without text")
		}
		return Result{Type: ResponseNotice, Text: text}, nil
	default:
		return Result{}, unavailable("unknown response type %q", wr.Type)
	}
}

// checkOpenURL verifies raw is an absolute URL under origin: https, or
// http only for an origin in AllowedInsecureOrigins (REQ-APP-44).
func (c *Client) checkOpenURL(origin, raw string) (string, error) {
	if raw == "" || len(raw) > 4096 {
		return "", errors.New("missing or oversized url")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return "", fmt.Errorf("%q is not an absolute URL", raw)
	}
	if u.User != nil {
		return "", errors.New("url must not carry userinfo")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && c.insecure[origin]) {
		return "", fmt.Errorf("url scheme %q not allowed", u.Scheme)
	}
	got, err := sysconfig.NormalizeOrigin(u.Scheme + "://" + u.Host)
	if err != nil || got != origin {
		return "", fmt.Errorf("url %q is not under the app origin", raw)
	}
	return u.String(), nil
}

// PlainText reduces s to at most MaxTextRunes runes of plain text:
// invalid UTF-8 and control characters other than newline become
// spaces, and surrounding whitespace is trimmed.
func PlainText(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToValidUTF8(s, " ") {
		if n >= MaxTextRunes {
			break
		}
		if r != '\n' && (unicode.IsControl(r) || r == utf8.RuneError) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// errorIsTimeout reports whether err is a deadline or timeout error.
func errorIsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
