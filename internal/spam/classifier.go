package spam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
)

// Verdict is the classifier's normalized verdict. We intentionally keep the
// enum small — detailed fields live in Classification.RawResponse.
type Verdict int

// Verdict values. Ham / Spam are the two classifier outcomes; Unclassified
// covers plugin timeouts, crashes, and unparseable responses. The delivery
// path treats Unclassified as "not spam" by default (REQ-FILT-40).
const (
	Unclassified Verdict = iota
	Ham
	Spam
)

// String returns the canonical lower-case token used in logs and the
// JSON payload sent to plugins.
func (v Verdict) String() string {
	switch v {
	case Ham:
		return "ham"
	case Spam:
		return "spam"
	default:
		return "unclassified"
	}
}

// parseVerdict normalizes a verdict string returned by the plugin.
func parseVerdict(s string) Verdict {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ham", "clean", "not_spam", "not-spam":
		return Ham
	case "spam", "junk":
		return Spam
	default:
		return Unclassified
	}
}

// Classification is the Classify result.
type Classification struct {
	// Verdict is the classifier outcome distilled to the small enum.
	Verdict Verdict
	// Score is the [0,1] confidence attached to Verdict. A negative
	// value indicates the plugin returned no score (unclassified).
	Score float64
	// RawResponse carries the plugin's full JSON response so the
	// delivery path can log reason strings, model name, etc.
	RawResponse map[string]any
}

// PluginInvoker is the minimum plugin-supervisor surface Classifier needs:
// a Call method that dispatches a JSON-RPC request to a named plugin and
// unmarshals the result into result. internal/plugin.Manager provides
// this via a tiny adapter; tests substitute a fake.
type PluginInvoker interface {
	// Call invokes method on plugin with params; result is populated
	// from the plugin's JSON result.
	Call(ctx context.Context, plugin, method string, params any, result any) error
}

// DefaultTimeout is applied when the caller's ctx has no deadline.
const DefaultTimeout = 5 * time.Second

// DefaultBodyExcerptBytes caps the body excerpt sent to the plugin at
// ~4 KiB per REQ-FILT-30.
const DefaultBodyExcerptBytes = 4 * 1024

// ClassifyMethod is the JSON-RPC method name the plugin must expose.
const ClassifyMethod = "spam.classify"

// Classifier orchestrates one classify call. Callers construct a single
// Classifier and reuse it across deliveries; it is safe for concurrent
// use.
type Classifier struct {
	invoker PluginInvoker
	logger  *slog.Logger
	clock   clock.Clock
	timeout time.Duration
}

// New returns a Classifier that invokes methods on the supplied
// PluginInvoker. logger is used for structured log lines; clock is used
// for deadline computation so tests are deterministic.
func New(invoker PluginInvoker, logger *slog.Logger, clk clock.Clock) *Classifier {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	return &Classifier{
		invoker: invoker,
		logger:  logger,
		clock:   clk,
		timeout: DefaultTimeout,
	}
}

// WithTimeout returns a Classifier with an overridden default timeout for
// ctx values that carry no deadline of their own.
func (c *Classifier) WithTimeout(d time.Duration) *Classifier {
	cp := *c
	cp.timeout = d
	return &cp
}

// Classify builds the prompt and invokes the plugin under pluginName. A
// non-nil error always pairs with Classification{Verdict: Unclassified},
// never with a real verdict: the delivery path can therefore switch on
// Verdict alone. Timeout / plugin-unavailable / parse failures are
// reported as errors but not raised as panics.
func (c *Classifier) Classify(ctx context.Context, msg mailparse.Message, auth *mailauth.AuthResults, pluginName string) (Classification, error) {
	if c.invoker == nil {
		return Classification{Verdict: Unclassified, Score: -1}, errors.New("spam: no plugin invoker configured")
	}
	ctx, cancel := c.deadline(ctx)
	defer cancel()

	req := BuildRequest(msg, auth)
	var raw map[string]any
	err := c.invoker.Call(ctx, pluginName, ClassifyMethod, req, &raw)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			c.logger.WarnContext(ctx, "spam classifier deadline", "plugin", pluginName, "err", err)
		} else {
			c.logger.WarnContext(ctx, "spam classifier error", "plugin", pluginName, "err", err)
		}
		return Classification{Verdict: Unclassified, Score: -1}, err
	}
	return parseClassification(raw)
}

// deadline ensures ctx carries a deadline; if it does not, a DefaultTimeout
// one is attached.
func (c *Classifier) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

// parseClassification distills the plugin's JSON object into a
// Classification. It is lenient: unrecognised fields are preserved in
// RawResponse.
func parseClassification(raw map[string]any) (Classification, error) {
	out := Classification{Verdict: Unclassified, Score: -1, RawResponse: raw}
	if raw == nil {
		return out, errors.New("spam: plugin returned nil response")
	}
	if v, ok := raw["verdict"].(string); ok {
		out.Verdict = parseVerdict(v)
	}
	if s, ok := raw["score"].(float64); ok {
		out.Score = s
	} else if s, ok := raw["confidence"].(float64); ok {
		out.Score = s
	}
	if out.Verdict == Unclassified {
		return out, errors.New("spam: plugin returned unrecognised verdict")
	}
	return out, nil
}

// Request is the JSON shape sent to the plugin. Fields follow
// docs/requirements/06-filtering.md §Prompt shape.
type Request struct {
	From         []string `json:"from"`
	To           []string `json:"to"`
	Cc           []string `json:"cc,omitempty"`
	Subject      string   `json:"subject"`
	ReceivedDate string   `json:"received_date,omitempty"`
	DKIMPass     bool     `json:"dkim_pass"`
	SPFPass      bool     `json:"spf_pass"`
	DMARCPass    bool     `json:"dmarc_pass"`
	FromDomain   string   `json:"from_domain,omitempty"`
	BodyExcerpt  string   `json:"body_excerpt"`
}

// BuildRequest assembles the Request from a parsed message + auth
// results. The excerpt is capped to DefaultBodyExcerptBytes and HTML is
// stripped to text. URLs and email addresses are preserved because the
// classifier prompt specifically wants them. A nil auth argument
// collapses every did-pass boolean to false and FromDomain to "".
func BuildRequest(msg mailparse.Message, auth *mailauth.AuthResults) Request {
	from := addrsToStrings(msg.Envelope.From)
	to := addrsToStrings(msg.Envelope.To)
	cc := addrsToStrings(msg.Envelope.Cc)
	body := collectTextBody(msg.Body, DefaultBodyExcerptBytes)
	req := Request{
		From:         from,
		To:           to,
		Cc:           cc,
		Subject:      msg.Envelope.Subject,
		ReceivedDate: msg.Envelope.Date,
		BodyExcerpt:  body,
	}
	if auth != nil {
		req.DKIMPass = auth.BestDKIMStatus() == mailauth.AuthPass
		req.SPFPass = auth.SPF.Status == mailauth.AuthPass
		req.DMARCPass = auth.DMARC.Status == mailauth.AuthPass
		req.FromDomain = auth.FromDomain()
	}
	return req
}

// MarshalJSON on Request is default; this helper exists for tests that
// want the canonical on-wire representation.
func (r Request) Canonical() (json.RawMessage, error) {
	return json.Marshal(r)
}

// addrsToStrings converts []mail.Address into canonical "name <addr>"
// strings for the plugin payload. Empty name folds to bare address.
func addrsToStrings(addrs []mail.Address) []string {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Name == "" {
			out = append(out, a.Address)
			continue
		}
		out = append(out, fmt.Sprintf("%s <%s>", a.Name, a.Address))
	}
	return out
}

// collectTextBody concatenates decoded text/* parts up to cap bytes. HTML
// parts are rendered to plain text with a naive tag-strip; URLs and
// addresses survive because they sit outside <> brackets.
func collectTextBody(p mailparse.Part, cap int) string {
	var b strings.Builder
	collectTextBodyInto(p, &b, cap)
	s := b.String()
	if len(s) > cap {
		s = s[:cap]
	}
	return s
}

func collectTextBodyInto(p mailparse.Part, b *strings.Builder, cap int) {
	if b.Len() >= cap {
		return
	}
	ct := strings.ToLower(p.ContentType)
	switch {
	case strings.HasPrefix(ct, "text/html"):
		b.WriteString(stripHTMLTags(p.Text))
		b.WriteByte('\n')
	case strings.HasPrefix(ct, "text/"):
		b.WriteString(p.Text)
		b.WriteByte('\n')
	}
	for _, c := range p.Children {
		if b.Len() >= cap {
			return
		}
		collectTextBodyInto(c, b, cap)
	}
}

// stripHTMLTags is a deliberately simple HTML-to-text converter. The
// classifier prompt tolerates noise; a pure-Go dependency-free stripper
// is preferable to pulling in an HTML parser here.
func stripHTMLTags(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	skip := 0
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch c {
		case '<':
			skip++
			continue
		case '>':
			if skip > 0 {
				skip--
			}
			continue
		}
		if skip == 0 {
			b.WriteByte(c)
		}
	}
	return b.String()
}
