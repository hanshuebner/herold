package dsn

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// Classification is the coarse-grained outcome REQ-MLIST-51 requires:
// hard (permanent, 5.x.x) vs soft (transient, 4.x.x) vs unknown (no
// conclusive signal -- not itself a failure classification part 2 acts
// on, since scoring a bounce it cannot confidently characterise would
// defeat the conservative-classification requirement).
type Classification int

const (
	// ClassificationUnknown is the zero value: no conformant DSN
	// structure was found, the report described success/relay/expansion
	// rather than a failure, or the per-recipient fields were present
	// but not internally consistent enough to classify with confidence.
	ClassificationUnknown Classification = iota
	// ClassificationSoft is a transient failure (RFC 3463 4.x.x, or an
	// RFC 3464 Action: delayed).
	ClassificationSoft
	// ClassificationHard is a permanent failure (RFC 3463 5.x.x together
	// with an RFC 3464 Action: failed).
	ClassificationHard
)

// String returns the lowercase token used in log lines and (by a later
// stage) metric labels.
func (c Classification) String() string {
	switch c {
	case ClassificationHard:
		return "hard"
	case ClassificationSoft:
		return "soft"
	default:
		return "unknown"
	}
}

// RecipientStatus is one RFC 3464 §2.3 per-recipient field block from a
// message/delivery-status part. Fields that carry a "type;value" form
// (Original-Recipient, Final-Recipient, Remote-MTA, Diagnostic-Code)
// have the leading type token stripped; Action and Status are the bare
// field values.
type RecipientStatus struct {
	Action            string
	Status            string
	DiagnosticCode    string
	RemoteMTA         string
	FinalRecipient    string
	OriginalRecipient string
}

// Report is the result of parsing one inbound message as a delivery
// status notification.
type Report struct {
	// Classification is the aggregate outcome across every recipient
	// block found (see aggregate's doc comment). ClassificationUnknown
	// when no message/delivery-status part was found at all.
	Classification Classification
	// ReportingMTA is the per-message Reporting-MTA field (type prefix
	// stripped), when present. Diagnostic/audit context only.
	ReportingMTA string
	// Recipients holds every per-recipient field block found, in
	// document order. Empty when the message carried no conformant
	// message/delivery-status part.
	Recipients []RecipientStatus
}

// Parse parses raw as a complete RFC 5322 message and classifies it as
// a delivery status notification (REQ-MLIST-51). Parse itself only
// returns an error when raw is not even a well-formed RFC 5322 message
// (mailparse.Parse's own error) -- a message that IS well-formed but is
// not a conformant RFC 3464 report is not an error, it classifies as
// ClassificationUnknown with zero Recipients, so a non-conformant
// "bounce-ish" reply degrades gracefully rather than failing the
// caller.
func Parse(raw []byte) (Report, error) {
	msg, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewLenientParseOptions())
	if err != nil {
		return Report{}, fmt.Errorf("dsn: parse message: %w", err)
	}
	part, ok := findDeliveryStatusPart(msg.Body)
	if !ok {
		return Report{Classification: ClassificationUnknown}, nil
	}
	body, err := readPartBytes(part, bytes.NewReader(raw))
	if err != nil {
		// The part was located but its raw byte range could not be
		// read (should not happen for a part mailparse itself produced;
		// treated as "no usable report" rather than a hard error).
		return Report{Classification: ClassificationUnknown}, nil
	}
	return classifyDeliveryStatus(body), nil
}

// findDeliveryStatusPart searches p and its descendants (depth-first,
// document order) for a message/delivery-status leaf. Real-world
// senders vary in how they wrap this part -- almost always directly
// under a top-level multipart/report, but some nest an extra
// multipart/mixed layer or omit the report-type parameter entirely --
// so the search does not require any particular container shape above
// the leaf.
func findDeliveryStatusPart(p mailparse.Part) (mailparse.Part, bool) {
	if p.ContentType == "message/delivery-status" {
		return p, true
	}
	for _, c := range p.Children {
		if found, ok := findDeliveryStatusPart(c); ok {
			return found, true
		}
	}
	return mailparse.Part{}, false
}

// readPartBytes reads the full CTE-decoded body of leaf part p.
func readPartBytes(p mailparse.Part, src io.ReaderAt) ([]byte, error) {
	rc, err := p.OpenBody(src)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// classifyDeliveryStatus parses the field-block structure of a
// message/delivery-status part's decoded body and aggregates a
// Report from it.
func classifyDeliveryStatus(body []byte) Report {
	groups := splitFieldGroups(body)
	if len(groups) == 0 {
		return Report{Classification: ClassificationUnknown}
	}
	report := Report{ReportingMTA: stripTypedPrefix(groups[0].Get("Reporting-MTA"))}
	for _, g := range groups[1:] {
		rs := RecipientStatus{
			Action:            strings.TrimSpace(g.Get("Action")),
			Status:            strings.TrimSpace(g.Get("Status")),
			DiagnosticCode:    stripTypedPrefix(g.Get("Diagnostic-Code")),
			RemoteMTA:         stripTypedPrefix(g.Get("Remote-MTA")),
			FinalRecipient:    stripTypedPrefix(g.Get("Final-Recipient")),
			OriginalRecipient: stripTypedPrefix(g.Get("Original-Recipient")),
		}
		if rs == (RecipientStatus{}) {
			// A trailing empty block (e.g. blank lines after the last
			// real block); not a recipient.
			continue
		}
		report.Recipients = append(report.Recipients, rs)
	}
	report.Classification = aggregate(report.Recipients)
	return report
}

// aggregate combines the per-recipient classifications of statuses into
// one overall Report.Classification. A VERP bounce address addresses
// exactly one original recipient (the member), so in practice there is
// exactly one group; the aggregation rule below only matters for the
// rare non-conformant report describing more than one, and stays on the
// conservative side required by REQ-MLIST-51: Hard is asserted only
// when every group agrees; a mix of Hard with anything less certain
// downgrades the whole report to Soft rather than risk a wrong hard
// classification.
func aggregate(statuses []RecipientStatus) Classification {
	if len(statuses) == 0 {
		return ClassificationUnknown
	}
	sawHard, sawSoft, sawOther := false, false, false
	for _, s := range statuses {
		switch classifyOne(s) {
		case ClassificationHard:
			sawHard = true
		case ClassificationSoft:
			sawSoft = true
		default:
			sawOther = true
		}
	}
	switch {
	case sawHard && !sawSoft && !sawOther:
		return ClassificationHard
	case sawHard || sawSoft:
		return ClassificationSoft
	default:
		return ClassificationUnknown
	}
}

// classifyOne classifies a single RFC 3464 per-recipient field block.
// Conservative by design (STANDARDS.md-mandated: a mistaken Hard
// classification later drives auto-suspend of a real subscriber,
// REQ-MLIST-53/54's damaging direction):
//
//   - Hard requires the unambiguous, mutually consistent combination of
//     Action: failed with a Status in class 5 (5.x.x).
//   - Action: delayed classifies Soft regardless of Status (RFC 3464's
//     own definition of a transient-failure notification).
//   - Action: delivered / relayed / expanded classifies Unknown -- these
//     report a non-failure outcome, never a bounce.
//   - When Action is missing or not one of the RFC 3464 §2.3.3 values
//     (a non-conformant sender), fall back to the bare Status class:
//     both 5.x.x and 4.x.x classify Soft (not Hard -- Action's absence
//     is itself a reason for reduced confidence) and anything else
//     classifies Unknown.
func classifyOne(s RecipientStatus) Classification {
	action := strings.ToLower(s.Action)
	class, ok := statusClass(s.Status)

	switch action {
	case "failed":
		if ok && class == 5 {
			return ClassificationHard
		}
		return ClassificationUnknown
	case "delayed":
		return ClassificationSoft
	case "delivered", "relayed", "expanded":
		return ClassificationUnknown
	default:
		if ok && (class == 5 || class == 4) {
			return ClassificationSoft
		}
		return ClassificationUnknown
	}
}

// statusClass parses an RFC 3463 enhanced status code ("class.subject.detail",
// e.g. "5.1.1") and returns its class digit (2, 4, or 5). Tolerates a
// trailing free-text comment ("5.1.1 (bad address)"); returns ok=false
// for anything that does not parse as at least "<class-digit>.<rest>"
// with class in {2,4,5}.
func statusClass(status string) (int, bool) {
	status = strings.TrimSpace(status)
	if status == "" {
		return 0, false
	}
	if i := strings.IndexAny(status, " \t"); i >= 0 {
		status = status[:i]
	}
	parts := strings.SplitN(status, ".", 3)
	if len(parts) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || (n != 2 && n != 4 && n != 5) {
		return 0, false
	}
	return n, true
}

// stripTypedPrefix strips a leading RFC 3464 "type;" token (e.g.
// "rfc822;bob@example.com" -> "bob@example.com", "smtp; 550 ..." ->
// "550 ..."). Only strips when the segment before the first ';' looks
// like a bare type token (letters/digits/hyphen/underscore only);
// otherwise v is returned unchanged, so a value that merely happens to
// contain a semicolon is not corrupted.
func stripTypedPrefix(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	i := strings.IndexByte(v, ';')
	if i < 0 {
		return v
	}
	typeTok := strings.TrimSpace(v[:i])
	if !isTypeToken(typeTok) {
		return v
	}
	return strings.TrimSpace(v[i+1:])
}

func isTypeToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// fieldGroup is one blank-line-delimited block of a message/delivery-status
// body, parsed as RFC 5322-style headers (case-insensitive, fold-aware).
type fieldGroup struct {
	header textproto.MIMEHeader
}

// Get returns the first value of key, case-insensitive, or "" if absent.
func (g fieldGroup) Get(key string) string {
	if g.header == nil {
		return ""
	}
	return g.header.Get(key)
}

// splitFieldGroups splits a message/delivery-status body into its
// blank-line-delimited field groups (RFC 3464 §2.2: one per-message
// group followed by one-or-more per-recipient groups) and parses each
// as a structured header block via net/textproto -- the same
// fold-aware, case-insensitive reader RFC 5322 headers use elsewhere in
// this codebase, never raw substring scanning. A block that fails to
// parse as headers at all (binary garbage, a non-conformant sender) is
// dropped rather than treated as a valid empty group.
func splitFieldGroups(body []byte) []fieldGroup {
	blocks := splitBlankLineBlocks(body)
	groups := make([]fieldGroup, 0, len(blocks))
	for _, b := range blocks {
		b = bytes.TrimRight(b, "\r\n")
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		framed := make([]byte, 0, len(b)+4)
		framed = append(framed, b...)
		framed = append(framed, '\r', '\n', '\r', '\n')
		tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(framed)))
		hdr, err := tp.ReadMIMEHeader()
		if err != nil && len(hdr) == 0 {
			continue
		}
		groups = append(groups, fieldGroup{header: hdr})
	}
	return groups
}

// splitBlankLineBlocks splits body at every blank-line boundary
// (CRLFCRLF or LFLF), returning the blocks in order including the
// final one (which need not be terminated by a blank line).
func splitBlankLineBlocks(body []byte) [][]byte {
	var blocks [][]byte
	start := 0
	for start <= len(body) {
		idx, sep := indexBlankLine(body[start:])
		if idx < 0 {
			blocks = append(blocks, body[start:])
			break
		}
		blocks = append(blocks, body[start:start+idx])
		start += idx + sep
	}
	return blocks
}

// indexBlankLine returns the offset and length of the first blank-line
// separator (CRLFCRLF or LFLF) in b, or (-1, 0) if none is present.
func indexBlankLine(b []byte) (idx, seplen int) {
	if i := bytes.Index(b, []byte("\r\n\r\n")); i >= 0 {
		return i, 4
	}
	if i := bytes.Index(b, []byte("\n\n")); i >= 0 {
		return i, 2
	}
	return -1, 0
}
