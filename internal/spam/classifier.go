package spam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
)

// Verdict is the classifier's normalized verdict. We intentionally keep the
// enum small — detailed fields live in Classification.RawResponse.
type Verdict int

// Verdict values (REQ-FILT-01). Ham / Suspect / Spam are the three
// classifier outcomes; Unclassified covers plugin timeouts, crashes, and
// unparseable responses. The delivery path treats Unclassified as "not
// spam" by default (REQ-FILT-40). Suspect is appended after Spam, not
// inserted, so the numeric value of the pre-existing constants is stable.
const (
	Unclassified Verdict = iota
	Ham
	Spam
	Suspect
)

// String returns the canonical lower-case token used in logs and the
// JSON payload sent to plugins.
func (v Verdict) String() string {
	switch v {
	case Ham:
		return "ham"
	case Spam:
		return "spam"
	case Suspect:
		return "suspect"
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
	case "suspect", "likely_spam", "likely-spam":
		return Suspect
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

	// Pre-scope the call-local logger so every log line in this invocation
	// carries subsystem=spam and classifier=<pluginName> (REQ-OPS-86).
	log := c.logger.With("subsystem", "spam", "classifier", pluginName)

	req := BuildRequest(msg, auth)
	log.DebugContext(ctx, "spam classification request",
		"activity", observe.ActivitySystem,
		"method", ClassifyMethod)

	var raw map[string]any
	err := c.invoker.Call(ctx, pluginName, ClassifyMethod, req, &raw)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			log.WarnContext(ctx, "spam classifier timeout",
				"activity", observe.ActivitySystem,
				"err", err)
		} else {
			log.WarnContext(ctx, "spam classifier error",
				"activity", observe.ActivitySystem,
				"err", err)
		}
		return Classification{Verdict: Unclassified, Score: -1}, err
	}

	cl, err := parseClassification(raw)
	if err != nil {
		log.WarnContext(ctx, "spam classifier unparseable verdict",
			"activity", observe.ActivitySystem,
			"err", err)
		return cl, err
	}
	log.DebugContext(ctx, "spam classification verdict",
		"activity", observe.ActivitySystem,
		"verdict", cl.Verdict.String(),
		"confidence", cl.Score)
	return cl, nil
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
// docs/design/server/requirements/06-filtering.md §Prompt shape.
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

// accumulateMultiplier bounds how much raw (pre-normalization) text
// collectTextBodyInto gathers before giving up on a message, expressed
// as a multiple of the final excerpt cap. Normalization only shrinks
// text (tag-stripping, entity decoding, whitespace collapse), so a
// modest multiple leaves headroom for that shrinkage to still fill the
// cap without letting a pathologically large message balloon memory.
const accumulateMultiplier = 4

// collectTextBody concatenates the message's text content into a single
// excerpt (REQ-FILT-30/31). Within a multipart/alternative branch only
// one representation contributes -- the text/plain part when present,
// else the tag-stripped text/html part -- so the two alternative
// renderings of the same content are not both appended (re #299). Other
// multipart containers keep concatenating every text/* child. The
// result has HTML entities decoded and whitespace runs collapsed to at
// most one blank line, then is capped to capBytes bytes on a rune
// boundary.
func collectTextBody(p mailparse.Part, capBytes int) string {
	var b strings.Builder
	collectTextBodyInto(p, &b, capBytes*accumulateMultiplier)
	s := normalizeExcerpt(b.String())
	return truncateUTF8(s, capBytes)
}

func collectTextBodyInto(p mailparse.Part, b *strings.Builder, limit int) {
	if b.Len() >= limit {
		return
	}
	ct := strings.ToLower(p.ContentType)
	if strings.HasPrefix(ct, "multipart/alternative") {
		if t := alternativeText(p); t != "" {
			b.WriteString(t)
			b.WriteByte('\n')
		}
		return
	}
	switch {
	case strings.HasPrefix(ct, "text/html"):
		b.WriteString(stripHTMLTags(p.Text))
		b.WriteByte('\n')
	case strings.HasPrefix(ct, "text/"):
		b.WriteString(p.Text)
		b.WriteByte('\n')
	}
	for _, c := range p.Children {
		if b.Len() >= limit {
			return
		}
		collectTextBodyInto(c, b, limit)
	}
}

// alternativeText resolves a multipart/alternative part to the single
// text rendering the excerpt should carry: the first text/plain leaf
// found anywhere under it, or -- when there is none -- the tag-stripped
// first text/html leaf. Mirrors how a mail client picks one alternative
// to render rather than showing both.
func alternativeText(p mailparse.Part) string {
	if t := firstLeafText(p, "text/plain"); t != "" {
		return t
	}
	if t := firstLeafText(p, "text/html"); t != "" {
		return stripHTMLTags(t)
	}
	return ""
}

// firstLeafText walks the part tree depth-first and returns the Text of
// the first leaf whose Content-Type starts with ctPrefix. Returns "" when
// no such leaf exists.
func firstLeafText(p mailparse.Part, ctPrefix string) string {
	if len(p.Children) == 0 {
		if strings.HasPrefix(strings.ToLower(p.ContentType), ctPrefix) {
			return p.Text
		}
		return ""
	}
	for _, c := range p.Children {
		if t := firstLeafText(c, ctPrefix); t != "" {
			return t
		}
	}
	return ""
}

// normalizeExcerpt decodes HTML entities left over from stripHTMLTags and
// collapses whitespace: each line is trimmed and its internal whitespace
// runs collapsed to single spaces, leading/trailing blank lines are
// dropped, and runs of blank lines collapse to at most one -- so the
// excerpt's byte budget is spent on content, not layout whitespace
// (re #299).
func normalizeExcerpt(s string) string {
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// truncateUTF8 returns the longest prefix of s that is at most n bytes
// and valid UTF-8, so a multi-byte rune straddling the cap is dropped
// whole rather than split into an invalid trailing fragment.
func truncateUTF8(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
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
