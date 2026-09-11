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
	// Reason is the plugin's one-sentence explanation (REQ-FILT-66),
	// shown to the user. Empty when the plugin did not supply one.
	Reason string
	// Category is the classifier's category assignment (Wave 4.3,
	// REQ-FILT-210), empty when the plugin returned none, named a
	// category outside the principal's set (rejected and logged inside
	// Classify), or the plugin is a TypeSpam classifier (which never
	// returns one). The delivery path applies the server's structural
	// fallback categoriser when this is empty (ADR-0002).
	Category string
	// RawResponse carries the plugin's full JSON response so callers
	// can log extra fields (e.g. "model") the classifier reported.
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

// Sentinel errors Classify returns so callers can classify an
// Unclassified outcome's reason without string-matching (re #326). Every
// non-nil error Classify returns wraps one of these via errors.Is.
var (
	// ErrNotConfigured means no plugin invoker was wired at all (the
	// Classifier was constructed with a nil PluginInvoker) or the
	// caller chose not to invoke Classify because no spam/classifier
	// plugin is configured. classifyMessage-style callers that never
	// call Classify in this situation still report it via ReasonClass
	// for a consistent operator-facing reason string.
	ErrNotConfigured = errors.New("spam: no plugin invoker configured")
	// ErrUnparseableVerdict means the plugin's JSON-RPC call succeeded
	// but the response carried no verdict Classify recognizes.
	ErrUnparseableVerdict = errors.New("spam: plugin returned unrecognised verdict")
	// ErrNilResponse means the plugin's JSON-RPC call succeeded but
	// returned no result object at all.
	ErrNilResponse = errors.New("spam: plugin returned nil response")
)

// rpcTimeoutMarker is the exact message internal/plugin/client.go's
// Client.Call sets on the *plugin.Error it returns when the caller's ctx
// deadline fires before a response arrives (ErrCodeTimeout, JSON-RPC code
// -32001, rendered as "json-rpc error -32001: rpc deadline exceeded").
// ReasonClass matches on this substring rather than importing
// internal/plugin: PluginInvoker is deliberately the only coupling this
// package has to the plugin supervisor (see classifierPluginType above),
// and every non-test invoker in this codebase routes through
// internal/plugin, so the substring always appears verbatim on an actual
// RPC timeout.
const rpcTimeoutMarker = "rpc deadline exceeded"

// ReasonClass categorizes a Classify error into the small, stable
// vocabulary surfaced to operators: the INFO delivery-outcome log line
// (re #326) and the persisted llm_classifications.spam_reason prefix
// both use these tokens. Returns "" for a nil error.
func ReasonClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotConfigured):
		return "not_configured"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case strings.Contains(err.Error(), rpcTimeoutMarker):
		return "timeout"
	case errors.Is(err, ErrUnparseableVerdict), errors.Is(err, ErrNilResponse):
		return "unparseable"
	default:
		return "plugin_error"
	}
}

// reasonText renders the "<class>: <error text>" string stored in
// Classification.Reason for an Unclassified outcome (re #326): the
// class alone (ReasonClass) is a stable, joinable vocabulary; the error
// text keeps the specific detail (which plugin, which RPC code, which
// parse failure) an operator needs to act on.
func reasonText(err error) string {
	return ReasonClass(err) + ": " + err.Error()
}

// DefaultBodyExcerptBytes caps the body excerpt sent to the plugin at
// ~4 KiB per REQ-FILT-30.
const DefaultBodyExcerptBytes = 4 * 1024

// ClassifyMethod is the JSON-RPC method name a TypeSpam plugin exposes
// (REQ-FILT-13, legacy contract retained for one release per issue #304
// Decision 3).
const ClassifyMethod = "spam.classify"

// MailClassifyMethod is the JSON-RPC method name a TypeClassifier plugin
// exposes (Wave 4.3, REQ-FILT-13). One call returns both the spam
// verdict and the category, replacing ClassifyMethod and
// internal/categorise's direct HTTP call.
const MailClassifyMethod = "mail.classify"

// classifierPluginType is the wire value of internal/plugin.TypeClassifier,
// duplicated here as a plain string so this package does not need to
// import internal/plugin merely to compare a manifest type tag.
const classifierPluginType = "classifier"

// PluginTypeResolver is an optional PluginInvoker extension a caller may
// implement so Classify can choose between the spam.classify and
// mail.classify wire contracts based on what the plugin process itself
// declared at handshake (Plugin.Type()) -- not what the operator wrote
// in system.toml, since issue #304 Decision 3 makes "spam" and
// "classifier" interchangeable there for one release. An invoker that
// does not implement this always gets the legacy spam.classify call.
type PluginTypeResolver interface {
	// PluginType returns the plugin's declared manifest type and true,
	// or ("", false) when the plugin is unknown.
	PluginType(name string) (string, bool)
}

// CategoryOption is one entry in the principal's stored category set
// (REQ-FILT-210), passed to a TypeClassifier plugin so it names a
// category the principal owns rather than inventing vocabulary
// (ADR-0004).
type CategoryOption struct {
	Name        string
	Description string
}

// ClassifyContext carries what the operator granted a TypeClassifier
// plugin for one mail.classify call, beyond the message itself: who it
// is for, where it is headed, the principal's own policy prose, and the
// category vocabulary the plugin may pick from. The zero value carries
// no context and is what a TypeSpam call always uses.
type ClassifyContext struct {
	// Principal is an opaque identifier (the recipient principal's
	// numeric id, as a string) a plugin may use as a state-scoping
	// key. Empty when the message has no local recipient.
	Principal string
	// RecipientDomain is the domain part of the recipient address.
	RecipientDomain string
	// Prompt is the principal's own categorisation policy in their own
	// words (REQ-FILT-211), with any operator guardrail prepended
	// (REQ-FILT-67). Empty when categorisation is disabled for this
	// principal.
	Prompt string
	// Categories is the principal's stored category set (REQ-FILT-210).
	// A category the plugin returns that does not name an entry here
	// is ignored and logged (REQ-FILT-230); empty means categorisation
	// is disabled and the returned category is always dropped.
	Categories []CategoryOption
}

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
//
// clsCtx supplies the per-principal category context (REQ-FILT-210); its
// zero value is valid and means "no category context" -- the call still
// runs, but a TypeClassifier plugin's returned category is dropped since
// there is no principal set to validate it against. Whether the wire
// call is spam.classify or mail.classify is decided by the plugin's OWN
// declared manifest type (via invoker's optional PluginTypeResolver),
// not by clsCtx being non-zero.
func (c *Classifier) Classify(ctx context.Context, msg mailparse.Message, auth *mailauth.AuthResults, pluginName string, clsCtx ClassifyContext) (Classification, error) {
	if c.invoker == nil {
		return Classification{Verdict: Unclassified, Score: -1, Reason: reasonText(ErrNotConfigured)}, ErrNotConfigured
	}
	ctx, cancel := c.deadline(ctx)
	defer cancel()

	// Pre-scope the call-local logger so every log line in this invocation
	// carries subsystem=spam and classifier=<pluginName> (REQ-OPS-86).
	log := c.logger.With("subsystem", "spam", "classifier", pluginName)

	useClassifier := false
	if tr, ok := c.invoker.(PluginTypeResolver); ok {
		if t, found := tr.PluginType(pluginName); found && t == classifierPluginType {
			useClassifier = true
		}
	}

	method := ClassifyMethod
	var req any = BuildRequest(msg, auth)
	if useClassifier {
		method = MailClassifyMethod
		req = MailClassifyRequest{
			Request: BuildRequest(msg, auth),
			Context: requestContextFrom(clsCtx),
		}
	}
	log.DebugContext(ctx, "spam classification request",
		"activity", observe.ActivitySystem,
		"method", method)

	var raw map[string]any
	err := c.invoker.Call(ctx, pluginName, method, req, &raw)
	if err != nil {
		if ReasonClass(err) == "timeout" {
			log.WarnContext(ctx, "spam classifier timeout",
				"activity", observe.ActivitySystem,
				"err", err)
		} else {
			log.WarnContext(ctx, "spam classifier error",
				"activity", observe.ActivitySystem,
				"err", err)
		}
		return Classification{Verdict: Unclassified, Score: -1, Reason: reasonText(err)}, err
	}

	cl, err := parseClassification(raw)
	if err != nil {
		log.WarnContext(ctx, "spam classifier unparseable verdict",
			"activity", observe.ActivitySystem,
			"err", err)
		cl.Reason = reasonText(err)
		return cl, err
	}
	if !useClassifier {
		// A TypeSpam plugin never carries a category (Decision 3): drop
		// one defensively even if a rogue plugin sent it.
		cl.Category = ""
	} else if cl.Category != "" && !categoryInSet(cl.Category, clsCtx.Categories) {
		log.WarnContext(ctx, "classifier returned category outside principal's set; ignoring",
			"activity", observe.ActivitySystem,
			"category", cl.Category)
		cl.Category = ""
	}
	log.DebugContext(ctx, "spam classification verdict",
		"activity", observe.ActivitySystem,
		"verdict", cl.Verdict.String(),
		"confidence", cl.Score,
		"category", cl.Category)
	return cl, nil
}

// categoryInSet reports whether name matches an entry in set, by
// case-insensitive name comparison. An empty set means the principal has
// no stored categories (or categorisation is disabled) -- nothing
// validates against it, so a returned category is always rejected by the
// caller (Classify passes an empty set exactly in that situation).
func categoryInSet(name string, set []CategoryOption) bool {
	for _, c := range set {
		if strings.EqualFold(c.Name, name) {
			return true
		}
	}
	return false
}

// requestContextFrom converts a ClassifyContext into the wire shape sent
// to a TypeClassifier plugin.
func requestContextFrom(clsCtx ClassifyContext) RequestContext {
	out := RequestContext{
		Principal:       clsCtx.Principal,
		RecipientDomain: clsCtx.RecipientDomain,
		Prompt:          clsCtx.Prompt,
	}
	if len(clsCtx.Categories) > 0 {
		out.Categories = make([]RequestCategory, len(clsCtx.Categories))
		for i, c := range clsCtx.Categories {
			out.Categories[i] = RequestCategory(c)
		}
	}
	return out
}

// deadline ensures ctx carries a deadline; if it does not, the classifier's
// configured budget (WithTimeout, default DefaultTimeout) is attached
// regardless of what the plugin's own model-call timeout is set to
// (Wave 4.1, REQ-FILT-40/42). The cutoff is driven by the injected Clock
// rather than the runtime's wall-clock timers, so a test can assert it
// deterministically with a FakeClock instead of a real sleep.
func (c *Classifier) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	// context.WithDeadline (rather than WithCancel) keeps ctx.Deadline()
	// reporting a real value for callers/plugins that inspect it, and in
	// production (clock.Real) its built-in wall-clock timer is the
	// actual cutoff mechanism. The clock.Timer below is what makes the
	// cutoff happen when the injected Clock is a FakeClock: real time
	// does not advance during a test, so WithDeadline's own timer never
	// fires there, and the explicit Advance() below does the job
	// instead.
	cctx, cancel := context.WithDeadline(ctx, c.clock.Now().Add(c.timeout))
	timer := c.clock.NewTimer(c.timeout)
	go func() {
		select {
		case <-timer.C():
			cancel()
		case <-cctx.Done():
			timer.Stop()
		}
	}()
	return cctx, cancel
}

// parseClassification distills the plugin's JSON object into a
// Classification. It is lenient: unrecognised fields are preserved in
// RawResponse.
func parseClassification(raw map[string]any) (Classification, error) {
	out := Classification{Verdict: Unclassified, Score: -1, RawResponse: raw}
	if raw == nil {
		return out, ErrNilResponse
	}
	if v, ok := raw["verdict"].(string); ok {
		out.Verdict = parseVerdict(v)
	}
	if s, ok := raw["score"].(float64); ok {
		out.Score = s
	} else if s, ok := raw["confidence"].(float64); ok {
		out.Score = s
	}
	if r, ok := raw["reason"].(string); ok {
		out.Reason = r
	}
	if cat, ok := raw["category"].(string); ok {
		out.Category = strings.TrimSpace(cat)
	}
	if out.Verdict == Unclassified {
		return out, ErrUnparseableVerdict
	}
	return out, nil
}

// Request is the JSON shape sent to the plugin. Fields follow
// docs/design/server/requirements/06-filtering.md §Prompt shape
// (REQ-FILT-20/30) and the classifier data grant in
// docs/design/server/implementation/08-classifier-plugin.md: envelope
// From/To/Cc/Subject/Date, the forwarding/list headers Reply-To,
// Return-Path, List-Id, List-Unsubscribe, Precedence and
// Auto-Submitted, the server's own Authentication-Results verdict, and
// a body excerpt. No message.raw, no attachments, no full body.
type Request struct {
	From            []string `json:"from"`
	To              []string `json:"to"`
	Cc              []string `json:"cc,omitempty"`
	Subject         string   `json:"subject"`
	ReplyTo         string   `json:"reply_to,omitempty"`
	ReturnPath      string   `json:"return_path,omitempty"`
	ReceivedDate    string   `json:"received_date,omitempty"`
	ListID          string   `json:"list_id,omitempty"`
	ListUnsubscribe string   `json:"list_unsubscribe,omitempty"`
	Precedence      string   `json:"precedence,omitempty"`
	AutoSubmitted   string   `json:"auto_submitted,omitempty"`
	AuthResults     string   `json:"auth_results,omitempty"`
	DKIMPass        bool     `json:"dkim_pass"`
	SPFPass         bool     `json:"spf_pass"`
	DMARCPass       bool     `json:"dmarc_pass"`
	FromDomain      string   `json:"from_domain,omitempty"`
	BodyExcerpt     string   `json:"body_excerpt"`
}

// BuildRequest assembles the Request from a parsed message + auth
// results. The excerpt is capped to DefaultBodyExcerptBytes and HTML is
// stripped to text. URLs and email addresses are preserved because the
// classifier prompt specifically wants them. ReplyTo, ReturnPath,
// ListID, ListUnsubscribe, Precedence and AutoSubmitted are read
// straight off msg.Headers (single, unfolded occurrence) so a message
// carrying none of them simply omits those keys from the wire payload.
//
// AuthResults is herold's own rendered Authentication-Results value
// (auth.Raw, the same string protosmtp's stamping path writes) whenever
// the caller has a verified auth result: the classifier data grant
// (docs/design/server/implementation/08-classifier-plugin.md) specifies
// the server's own SPF/DKIM/DMARC verdict, not a forgeable upstream
// header. auth.Raw is populated before classification runs and therefore
// never carries the x-herold-spam token (the verdict does not exist
// yet). A nil auth argument — the IMAP import path, which stores bytes
// as-synced and performs no server-side verification (REQ-IMAP-IMP-33)
// — falls back to msg.AuthResultsRaw, the upstream header content as
// received, and collapses every did-pass boolean to false and
// FromDomain to "" (re #298).
func BuildRequest(msg mailparse.Message, auth *mailauth.AuthResults) Request {
	from := addrsToStrings(msg.Envelope.From)
	to := addrsToStrings(msg.Envelope.To)
	cc := addrsToStrings(msg.Envelope.Cc)
	replyTo := strings.Join(addrsToStrings(msg.Envelope.ReplyTo), ", ")
	body := collectTextBody(msg.Body, DefaultBodyExcerptBytes)
	req := Request{
		From:            from,
		To:              to,
		Cc:              cc,
		Subject:         msg.Envelope.Subject,
		ReplyTo:         replyTo,
		ReturnPath:      msg.Headers.Get("Return-Path"),
		ReceivedDate:    msg.Envelope.Date,
		ListID:          msg.Headers.Get("List-Id"),
		ListUnsubscribe: msg.Headers.Get("List-Unsubscribe"),
		Precedence:      msg.Headers.Get("Precedence"),
		AutoSubmitted:   msg.Headers.Get("Auto-Submitted"),
		AuthResults:     msg.AuthResultsRaw,
		BodyExcerpt:     body,
	}
	if auth != nil {
		req.DKIMPass = auth.BestDKIMStatus() == mailauth.AuthPass
		req.SPFPass = auth.SPF.Status == mailauth.AuthPass
		req.DMARCPass = auth.DMARC.Status == mailauth.AuthPass
		req.FromDomain = auth.FromDomain()
		req.AuthResults = auth.Raw
	}
	return req
}

// MarshalJSON on Request is default; this helper exists for tests that
// want the canonical on-wire representation.
func (r Request) Canonical() (json.RawMessage, error) {
	return json.Marshal(r)
}

// RequestCategory is one entry in RequestContext.Categories, mirroring
// plugins/sdk.MailClassifyCategory field-for-field.
type RequestCategory struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// RequestContext is the "context" object sent alongside a Request in a
// MailClassifyRequest, mirroring plugins/sdk.MailClassifyContext
// field-for-field.
type RequestContext struct {
	Principal       string            `json:"principal,omitempty"`
	RecipientDomain string            `json:"recipient_domain,omitempty"`
	Prompt          string            `json:"prompt,omitempty"`
	Categories      []RequestCategory `json:"categories,omitempty"`
}

// MailClassifyRequest is the JSON shape sent to a TypeClassifier plugin's
// mail.classify method: the same message projection Request carries
// (embedded, so its fields marshal at the top level), plus Context.
type MailClassifyRequest struct {
	Request
	Context RequestContext `json:"context"`
}

// Canonical returns the on-wire representation, used for transparency
// records (REQ-FILT-66/216) and tests.
func (r MailClassifyRequest) Canonical() (json.RawMessage, error) {
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
