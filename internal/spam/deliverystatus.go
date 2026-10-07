package spam

// deliverystatus.go extracts the delivery-status-report facts the
// classifier needs to judge a bounce by what bounced (re #513): a
// multipart/report; report-type=delivery-status message (RFC 3464)
// previously reached the classifier with its enclosed original
// invisible -- BuildRequest's body_excerpt held only the human-readable
// notice text, since mailparse exposed the message/rfc822 part as an
// opaque leaf and collectTextBody gathers text/* parts only. A model
// left to judge such a report purely from the notice's free-text
// wording hallucinated an own-address match that was not in the data
// (message 4247's stored reason named "bounces@ingeasoto.com" as an
// owner-used address despite it being absent from own_addresses).
//
// buildDeliveryStatusInfo reads the enclosed original's curated headers
// and a text excerpt of it, plus the structured per-recipient fields
// off the message/delivery-status part, into DeliveryStatusInfo.
// isBackscatter then turns that into the decisive "backscatter" fact
// (DefaultDecisiveSpamSignals): true when the enclosed original's From
// and Return-Path are neither an own address nor an own identity, so a
// report bouncing mail the owner never sent resolves to Spam
// deterministically rather than depending on a model's reading of the
// notice text; a bounce of the owner's own mail keeps the model's
// verdict.

import (
	"mime"
	"net/mail"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// DefaultEnclosedExcerptBytes caps the text excerpt BuildRequest takes
// from a delivery-status report's enclosed original (re #513), kept
// smaller than DefaultBodyExcerptBytes since it rides alongside the
// report's own notice-text body_excerpt in the same request.
const DefaultEnclosedExcerptBytes = 2 * 1024

// DeliveryStatusInfo carries the facts BuildRequest extracts from a
// multipart/report; report-type=delivery-status message (RFC 3464): the
// enclosed original's curated headers and a text excerpt of it, and the
// structured delivery-status fields read off the message's own
// message/delivery-status part. Nil on Request when msg is not such a
// report. A report whose message/rfc822 part is absent, or failed to
// parse, still populates the Action/Status/Diagnostic fields when a
// message/delivery-status part is present -- the Enclosed* fields and
// isBackscatter's fact are what is missing in that case, not the whole
// struct.
type DeliveryStatusInfo struct {
	// EnclosedFrom, EnclosedReturnPath, EnclosedTo, EnclosedSubject, and
	// EnclosedDate are the enclosed original's own From, Return-Path,
	// To, Subject, and Date, exactly as BuildRequest renders the outer
	// message's equivalent fields (addrsToStrings' "Name <addr>" form
	// for addresses). Empty when the report carries no message/rfc822
	// part, or that part failed to parse.
	EnclosedFrom       string `json:"enclosed_from,omitempty"`
	EnclosedReturnPath string `json:"enclosed_return_path,omitempty"`
	EnclosedTo         string `json:"enclosed_to,omitempty"`
	EnclosedSubject    string `json:"enclosed_subject,omitempty"`
	EnclosedDate       string `json:"enclosed_date,omitempty"`
	// EnclosedExcerpt is a text excerpt of the enclosed original's body,
	// capped at DefaultEnclosedExcerptBytes via the same collectTextBody
	// collector BuildRequest uses for the outer message.
	EnclosedExcerpt string `json:"enclosed_excerpt,omitempty"`
	// Action, Status, and Diagnostic are read from the report's
	// message/delivery-status part: Action and Status are its
	// "Action:"/"Status:" per-recipient fields verbatim; Diagnostic
	// combines "Remote-MTA:" and "Diagnostic-Code:" into one
	// semicolon-joined string (the "remote-MTA diagnostic" the ticket
	// asks for) since both describe the same failure and a classifier
	// has no use for them as two separate fields. Empty when the report
	// carries no message/delivery-status part.
	Action     string `json:"action,omitempty"`
	Status     string `json:"status,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`

	// enclosedAddresses holds the enclosed original's From and
	// Return-Path addresses, bare and lower-cased, for isBackscatter's
	// own-address comparison. Deliberately unexported: it is never sent
	// to the plugin or persisted on the transparency record -- only the
	// EnclosedFrom/EnclosedReturnPath display strings above are -- since
	// it exists purely to let the server compare against own_addresses
	// without re-parsing those display strings.
	enclosedAddresses []string
}

// buildDeliveryStatusInfo reports whether msg is a multipart/report;
// report-type=delivery-status message (RFC 3464) and, when it is,
// extracts the facts described on DeliveryStatusInfo. Returns nil when
// msg's top-level Content-Type is not multipart/report with
// report-type=delivery-status, OR when it is but neither a
// message/delivery-status nor a (successfully parsed) message/rfc822
// child part was found -- there is nothing this fact-gathering found to
// report in that case.
func buildDeliveryStatusInfo(msg mailparse.Message) *DeliveryStatusInfo {
	mt, params, err := mime.ParseMediaType(msg.Headers.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mt, "multipart/report") {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(params["report-type"]), "delivery-status") {
		return nil
	}

	info := &DeliveryStatusInfo{}
	for _, c := range msg.Body.Children {
		switch strings.ToLower(c.ContentType) {
		case "message/delivery-status":
			applyDeliveryStatusFields(c.Text, info)
		case "message/rfc822", "message/global":
			if c.Enclosed != nil {
				applyEnclosedOriginal(*c.Enclosed, info)
			}
		}
	}

	if info.EnclosedFrom == "" && info.EnclosedReturnPath == "" && info.EnclosedTo == "" &&
		info.EnclosedSubject == "" && info.EnclosedExcerpt == "" &&
		info.Action == "" && info.Status == "" && info.Diagnostic == "" {
		return nil
	}
	return info
}

// applyEnclosedOriginal fills the Enclosed* display fields and the
// internal enclosedAddresses comparison set from the report's enclosed
// original.
func applyEnclosedOriginal(enclosed mailparse.Message, info *DeliveryStatusInfo) {
	if len(enclosed.Envelope.From) > 0 {
		info.EnclosedFrom = addrsToStrings(enclosed.Envelope.From)[0]
		if a := strings.ToLower(strings.TrimSpace(enclosed.Envelope.From[0].Address)); a != "" {
			info.enclosedAddresses = append(info.enclosedAddresses, a)
		}
	}
	if rp := strings.TrimSpace(enclosed.Headers.Get("Return-Path")); rp != "" {
		info.EnclosedReturnPath = rp
		info.enclosedAddresses = append(info.enclosedAddresses, returnPathAddress(rp))
	}
	if len(enclosed.Envelope.To) > 0 {
		info.EnclosedTo = strings.Join(addrsToStrings(enclosed.Envelope.To), ", ")
	}
	info.EnclosedSubject = enclosed.Envelope.Subject
	info.EnclosedDate = enclosed.Envelope.Date
	info.EnclosedExcerpt = collectTextBody(enclosed.Body, DefaultEnclosedExcerptBytes)
}

// returnPathAddress extracts the bare, lower-cased address from a
// Return-Path header value. Return-Path is conventionally a bare
// "<addr>" with no display name -- sometimes without angle brackets at
// all -- so mail.ParseAddress is tried first and a trimmed fallback
// covers the bracket-less case.
func returnPathAddress(rp string) string {
	if addr, err := mail.ParseAddress(rp); err == nil {
		return strings.ToLower(strings.TrimSpace(addr.Address))
	}
	return strings.ToLower(strings.Trim(strings.TrimSpace(rp), "<>"))
}

// applyDeliveryStatusFields parses a message/delivery-status part's
// text (RFC 3464: one per-message field block, then one or more
// per-recipient field blocks, each a sequence of "Field: value" lines
// optionally folded onto a following whitespace-led continuation line)
// and sets info.Action/Status/Diagnostic from the last occurrence of
// each field across the whole text -- a delivery-status report for a
// single bounced recipient (the case this ticket addresses) has exactly
// one occurrence of each; "last wins" is a deliberate, simple choice for
// the rare multi-recipient report rather than a claim about which
// recipient matters most.
func applyDeliveryStatusFields(text string, info *DeliveryStatusInfo) {
	fields := parseFieldBlock(text)
	info.Action = fields["action"]
	info.Status = fields["status"]
	var diag []string
	if v := fields["remote-mta"]; v != "" {
		diag = append(diag, "Remote-MTA: "+v)
	}
	if v := fields["diagnostic-code"]; v != "" {
		diag = append(diag, "Diagnostic-Code: "+v)
	}
	info.Diagnostic = strings.Join(diag, "; ")
}

// parseFieldBlock parses RFC 822-style "Field: value" lines (with
// whitespace-led folded continuations), returning the last value seen
// per lower-cased field name. Blank lines separate blocks but do not
// otherwise affect parsing: every field across every block lands in the
// same map.
func parseFieldBlock(text string) map[string]string {
	fields := make(map[string]string)
	var name, value string
	flush := func() {
		if name != "" {
			fields[strings.ToLower(name)] = strings.TrimSpace(value)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			name, value = "", ""
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && name != "" {
			value += " " + strings.TrimSpace(line)
			continue
		}
		flush()
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			name, value = "", ""
			continue
		}
		name = strings.TrimSpace(line[:idx])
		value = strings.TrimSpace(line[idx+1:])
	}
	flush()
	return fields
}

// isBackscatter reports whether ds describes a delivery-status report
// whose enclosed original was not sent by the owner (re #513): neither
// its From nor its Return-Path address appears in ownAddresses. False
// when ds is nil (msg is not a delivery-status report at all), when the
// report carries no enclosed original to judge (ds.enclosedAddresses
// empty -- nothing to assert ownership of either way, matching the "a
// report with no enclosed original sets no fact" requirement), or when
// ownAddresses itself is empty -- mirroring RecipientNotOwn, there is no
// ownership fact to assert without an own-address set to compare
// against.
func isBackscatter(ds *DeliveryStatusInfo, ownAddresses []string) bool {
	if ds == nil || len(ds.enclosedAddresses) == 0 || len(ownAddresses) == 0 {
		return false
	}
	own := make(map[string]struct{}, len(ownAddresses))
	for _, a := range ownAddresses {
		own[strings.ToLower(strings.TrimSpace(a))] = struct{}{}
	}
	for _, addr := range ds.enclosedAddresses {
		if _, ok := own[addr]; ok {
			return false
		}
	}
	return true
}
