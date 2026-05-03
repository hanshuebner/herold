package jscalendar

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Calendar is the top-level VCALENDAR object the iMIP parser returns.
// Only the fields we need for iMIP intake (PRODID, METHOD, the contained
// VEVENTs) are populated; VTODO/VJOURNAL/VFREEBUSY/VTIMEZONE objects
// are silently skipped per the v1 scope (see doc.go).
type Calendar struct {
	ProdID string
	Method string
	Events []VEvent
}

// VEvent is the subset of RFC 5545 VEVENT properties we need to bridge
// into a JSCalendar Event for iMIP. Unknown properties round-trip
// through Other so the JSCalendar Event produced by ToJSCalendarEvent
// can preserve them in RawJSON.
type VEvent struct {
	UID          string
	Sequence     int
	Status       string
	Summary      string
	Description  string
	DTStamp      time.Time
	DTStart      time.Time
	DTEnd        time.Time
	TZID         string
	Organizer    Attendee
	Attendees    []Attendee
	RRule        string
	ExDates      []time.Time
	Categories   []string
	Location     string
	URL          string
	Class        string
	Transp       string
	LastModified time.Time

	Other []ICSProperty
}

// Attendee carries the iCalendar ATTENDEE / ORGANIZER property and its
// commonly-seen parameters. CalAddress holds the raw "mailto:foo@bar"
// value; the JSCalendar bridge strips the prefix.
type Attendee struct {
	CalAddress string
	CN         string
	Role       string
	PartStat   string
	RSVP       bool
}

// ICSProperty preserves an unknown iCalendar property line so the
// bridge can copy it into Event.RawJSON.
type ICSProperty struct {
	Name   string
	Params map[string]string
	Value  string
}

// ParseICS parses an iCalendar VCALENDAR stream. Tolerant of unknown
// properties (preserved via VEvent.Other); strict on the structural
// rules (BEGIN/END pairing, line-folding). Multiple VEVENTs (e.g. a
// REQUEST plus its overrides) are returned in source order; v1
// downstream consumers should ignore RECURRENCE-ID-bearing entries
// per the documented limitation.
func ParseICS(r io.Reader) (Calendar, error) {
	lines, err := readUnfoldedLines(r)
	if err != nil {
		return Calendar{}, err
	}
	var cal Calendar
	stack := make([]string, 0, 4)
	var currentEvent *VEvent
	for _, line := range lines {
		if line == "" {
			continue
		}
		name, params, value, splitErr := splitContentLine(line)
		if splitErr != nil {
			return Calendar{}, splitErr
		}
		switch strings.ToUpper(name) {
		case "BEGIN":
			stack = append(stack, strings.ToUpper(value))
			if strings.ToUpper(value) == "VEVENT" {
				currentEvent = &VEvent{}
			}
			continue
		case "END":
			if len(stack) == 0 {
				return Calendar{}, fmt.Errorf("icalendar: END without matching BEGIN: %q", value)
			}
			top := stack[len(stack)-1]
			if !strings.EqualFold(top, value) {
				return Calendar{}, fmt.Errorf("icalendar: END/BEGIN mismatch: BEGIN:%s END:%s", top, value)
			}
			stack = stack[:len(stack)-1]
			if strings.EqualFold(value, "VEVENT") && currentEvent != nil {
				cal.Events = append(cal.Events, *currentEvent)
				currentEvent = nil
			}
			continue
		}
		if len(stack) == 0 {
			continue
		}
		top := stack[len(stack)-1]
		switch top {
		case "VCALENDAR":
			switch strings.ToUpper(name) {
			case "PRODID":
				cal.ProdID = value
			case "METHOD":
				cal.Method = strings.ToUpper(value)
			}
		case "VEVENT":
			if currentEvent != nil {
				applyVEventProperty(currentEvent, name, params, value)
			}
		default:
			// VTIMEZONE / VALARM / VTODO / VJOURNAL / VFREEBUSY are
			// silently consumed in v1 (see doc.go).
		}
	}
	if len(stack) != 0 {
		return Calendar{}, fmt.Errorf("icalendar: unterminated %v", stack)
	}
	return cal, nil
}

// applyVEventProperty dispatches a single content line into the
// VEvent's typed fields. Unknown names land in Other for the bridge.
func applyVEventProperty(ev *VEvent, name string, params map[string]string, value string) {
	upper := strings.ToUpper(name)
	switch upper {
	case "UID":
		ev.UID = value
	case "SEQUENCE":
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			ev.Sequence = n
		}
	case "STATUS":
		ev.Status = strings.ToUpper(value)
	case "SUMMARY":
		ev.Summary = unescapeText(value)
	case "DESCRIPTION":
		ev.Description = unescapeText(value)
	case "DTSTAMP":
		ev.DTStamp = parseICSTime(value, params)
	case "DTSTART":
		ev.DTStart = parseICSTime(value, params)
		if tzid, ok := params["TZID"]; ok {
			ev.TZID = tzid
		}
	case "DTEND":
		ev.DTEnd = parseICSTime(value, params)
		if ev.TZID == "" {
			if tzid, ok := params["TZID"]; ok {
				ev.TZID = tzid
			}
		}
	case "RRULE":
		ev.RRule = value
	case "EXDATE":
		for _, raw := range strings.Split(value, ",") {
			t := parseICSTime(strings.TrimSpace(raw), params)
			if !t.IsZero() {
				ev.ExDates = append(ev.ExDates, t)
			}
		}
	case "CATEGORIES":
		for _, c := range strings.Split(value, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				ev.Categories = append(ev.Categories, c)
			}
		}
	case "LOCATION":
		ev.Location = unescapeText(value)
	case "URL":
		ev.URL = value
	case "CLASS":
		ev.Class = strings.ToUpper(value)
	case "TRANSP":
		ev.Transp = strings.ToUpper(value)
	case "LAST-MODIFIED":
		ev.LastModified = parseICSTime(value, params)
	case "ORGANIZER":
		ev.Organizer = parseAttendee(value, params)
	case "ATTENDEE":
		ev.Attendees = append(ev.Attendees, parseAttendee(value, params))
	default:
		// Forward-compat: preserve so the bridge can carry it.
		ev.Other = append(ev.Other, ICSProperty{
			Name:   upper,
			Params: copyParams(params),
			Value:  value,
		})
	}
}

// parseAttendee turns "mailto:foo@bar" + parameters into an Attendee.
// Unknown parameters are dropped (we don't need DELEGATED-TO, SENT-BY,
// etc. for v1).
func parseAttendee(value string, params map[string]string) Attendee {
	a := Attendee{
		CalAddress: strings.TrimSpace(value),
	}
	if v, ok := params["CN"]; ok {
		a.CN = v
	}
	if v, ok := params["ROLE"]; ok {
		a.Role = strings.ToUpper(v)
	}
	if v, ok := params["PARTSTAT"]; ok {
		a.PartStat = strings.ToUpper(v)
	}
	if v, ok := params["RSVP"]; ok {
		a.RSVP = strings.EqualFold(v, "TRUE")
	}
	return a
}

// readUnfoldedLines reads the iCalendar stream and applies RFC 5545
// §3.1 line unfolding: a CRLF followed by a SPACE or TAB continues the
// previous logical line. Returns the unfolded logical lines.
func readUnfoldedLines(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var out []string
	var cur strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		// Strip a trailing CR (CRLF was the canonical wire form;
		// bufio.Scanner already drops the LF).
		line = strings.TrimRight(line, "\r")
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			cur.WriteString(line[1:])
			continue
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("icalendar: read: %w", err)
	}
	return out, nil
}

// splitContentLine separates a logical iCalendar line into name,
// parameters, and value. Property names and parameter names are
// returned upper-case; parameter values pass through unchanged. The
// parser is permissive about quoting — RFC 5545 §3.1.1 says quoted
// strings only need quoting when the value contains "," ";" or ":";
// we strip a single set of surrounding double quotes if present.
//
// Embedded CR (0x0D) or LF (0x0A) bytes inside a parameter value are
// rejected: RFC 5545 §3.1 forbids raw control characters in parameter
// text, and accepting them lets a malicious sender smuggle iCalendar
// header injection through outbound iMIP re-emission. The whole-line
// CR/LF check is performed by the caller; we additionally guard each
// parameter value here so the rule is applied even if a future caller
// hands us a line that already had trailing folds stripped.
func splitContentLine(line string) (name string, params map[string]string, value string, err error) {
	if strings.ContainsAny(line, "\r\n") {
		return "", nil, "", fmt.Errorf("icalendar: embedded CR or LF in content line")
	}
	colon := indexUnquoted(line, ':')
	if colon < 0 {
		return strings.ToUpper(line), nil, "", nil
	}
	left := line[:colon]
	value = line[colon+1:]
	semicolon := strings.IndexByte(left, ';')
	if semicolon < 0 {
		return strings.ToUpper(strings.TrimSpace(left)), nil, value, nil
	}
	name = strings.ToUpper(strings.TrimSpace(left[:semicolon]))
	params = map[string]string{}
	rest := left[semicolon+1:]
	for _, p := range splitParamList(rest) {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		pname := strings.ToUpper(strings.TrimSpace(p[:eq]))
		pval := strings.TrimSpace(p[eq+1:])
		if len(pval) >= 2 && pval[0] == '"' && pval[len(pval)-1] == '"' {
			pval = pval[1 : len(pval)-1]
		}
		if strings.ContainsAny(pval, "\r\n") {
			return "", nil, "", fmt.Errorf("icalendar: embedded CR or LF in parameter %q value", pname)
		}
		params[pname] = pval
	}
	return name, params, value, nil
}

// indexUnquoted finds the first occurrence of c outside a double-quoted
// region. iCalendar permits quoted parameter values that contain ":" /
// ";" — we must not split inside one.
func indexUnquoted(s string, c byte) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case c:
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

// splitParamList splits a parameter region on ";" honouring quoted
// values. Empty entries are dropped.
func splitParamList(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '"':
			inQuote = !inQuote
			cur.WriteByte(ch)
		case ';':
			if inQuote {
				cur.WriteByte(ch)
			} else {
				if cur.Len() > 0 {
					out = append(out, cur.String())
					cur.Reset()
				}
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// copyParams duplicates the params map so the parser can mutate the
// scratch map without sharing state with the produced ICSProperty.
func copyParams(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// unescapeText reverses the RFC 5545 §3.3.11 TEXT escapes.
func unescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
				i++
				continue
			case ',', ';', '\\':
				b.WriteByte(s[i+1])
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseICSTime resolves an iCalendar DATE / DATE-TIME value to a UTC
// time.Time. Honours the TZID parameter (Windows TZID names are mapped
// via windowsToIANA). A trailing Z marks UTC; absence of both Z and
// TZID is "floating" — which iCalendar effectively pins to the
// receiver's local zone, but for storage we resolve it as UTC and let
// the ToJSCalendarEvent bridge carry the floating-ness via TimeZone="".
//
// Pre-year-1 timestamps (e.g. LAST-MODIFIED:00000101T000000Z) are
// rejected and returned as the zero time.Time. Go's time.Parse accepts
// year 0000 without error, producing a non-zero time.Time whose
// Year() == 0; such values are not meaningful in calendar data and
// would violate the JSCalendar Updated invariant checked in
// FuzzVEventToJSCalendar (regression seed: regression_year_zero).
func parseICSTime(value string, params map[string]string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	// DATE only — all-day.
	if len(value) == 8 {
		if t, err := time.Parse("20060102", value); err == nil {
			if t.Year() < 1 {
				return time.Time{}
			}
			return t.UTC()
		}
	}
	if strings.HasSuffix(value, "Z") {
		if t, err := time.Parse("20060102T150405Z", value); err == nil {
			if t.Year() < 1 {
				return time.Time{}
			}
			return t.UTC()
		}
	}
	loc := time.UTC
	if tzid, ok := params["TZID"]; ok && tzid != "" {
		loc = resolveICSTimezone(tzid)
	}
	if t, err := time.ParseInLocation("20060102T150405", value, loc); err == nil {
		if t.Year() < 1 {
			return time.Time{}
		}
		return t.UTC()
	}
	return time.Time{}
}

// windowsToIANA maps the most-frequent Windows time-zone identifiers
// emitted by Microsoft Exchange / Outlook to their IANA equivalents.
// Unmapped names fall back to UTC + a slog.Warn so operators can spot
// novel senders.
//
// Coverage rationale: ~20 zones cover the bulk of inbound iMIP volume
// observed at typical hosters; the long tail can be added on demand
// without breaking forward-compat (the unknown path still parses).
var windowsToIANA = map[string]string{
	"Pacific Standard Time":          "America/Los_Angeles",
	"Mountain Standard Time":         "America/Denver",
	"Central Standard Time":          "America/Chicago",
	"Eastern Standard Time":          "America/New_York",
	"Atlantic Standard Time":         "America/Halifax",
	"Alaskan Standard Time":          "America/Anchorage",
	"Hawaiian Standard Time":         "Pacific/Honolulu",
	"GMT Standard Time":              "Europe/London",
	"Greenwich Standard Time":        "Atlantic/Reykjavik",
	"W. Europe Standard Time":        "Europe/Berlin",
	"Central European Standard Time": "Europe/Warsaw",
	"Romance Standard Time":          "Europe/Paris",
	"Central Europe Standard Time":   "Europe/Budapest",
	"E. Europe Standard Time":        "Europe/Bucharest",
	"FLE Standard Time":              "Europe/Helsinki",
	"GTB Standard Time":              "Europe/Athens",
	"Russian Standard Time":          "Europe/Moscow",
	"China Standard Time":            "Asia/Shanghai",
	"Tokyo Standard Time":            "Asia/Tokyo",
	"India Standard Time":            "Asia/Kolkata",
	"AUS Eastern Standard Time":      "Australia/Sydney",
	"New Zealand Standard Time":      "Pacific/Auckland",
}

// resolveICSTimezone interprets a TZID. IANA names go through
// time.LoadLocation directly; Windows names are mapped first.
// Anything we cannot resolve falls back to UTC (with a warn) so the
// parser stays robust.
func resolveICSTimezone(tzid string) *time.Location {
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc
	}
	if iana, ok := windowsToIANA[tzid]; ok {
		if loc, err := time.LoadLocation(iana); err == nil {
			return loc
		}
	}
	slog.Warn("icalendar: unknown TZID, defaulting to UTC", "tzid", tzid)
	return time.UTC
}

// ResolveTZID exposes the Windows-to-IANA mapping for the bridge.
// Returns ("", false) when the input is not a known Windows name; the
// caller should treat the input as IANA-or-fallback.
func ResolveTZID(tzid string) (string, bool) {
	if tzid == "" {
		return "", false
	}
	if iana, ok := windowsToIANA[tzid]; ok {
		return iana, true
	}
	if _, err := time.LoadLocation(tzid); err == nil {
		return tzid, true
	}
	return "", false
}

// ToJSCalendarEvent converts the parsed VEvent into a JSCalendar Event
// per RFC 8984 Appendix A. Method-aware: a REPLY does not produce a
// "fresh" event but an event whose participants record the responding
// attendee's PARTSTAT. The caller (the JMAP /set bridge) reconciles
// the result against the existing event by UID + SEQUENCE.
func (v VEvent) ToJSCalendarEvent(method string) (Event, error) {
	if v.UID == "" {
		return Event{}, errors.New("icalendar: VEVENT has no UID")
	}
	method = strings.ToUpper(method)
	tz := v.TZID
	if tz != "" {
		if iana, ok := ResolveTZID(tz); ok {
			tz = iana
		}
	}
	e := Event{
		Type:     "Event",
		UID:      v.UID,
		Sequence: v.Sequence,
		Title:    v.Summary,
		Start:    v.DTStart,
		TimeZone: tz,
		Updated:  v.LastModified,
	}
	if !v.DTStamp.IsZero() && e.Updated.IsZero() {
		e.Updated = v.DTStamp
	}
	if v.Description != "" {
		e.Description = v.Description
	}
	if !v.DTStart.IsZero() && !v.DTEnd.IsZero() {
		e.Duration = Duration{Value: v.DTEnd.Sub(v.DTStart)}
	}
	switch method {
	case "CANCEL":
		e.StatusValue = "cancelled"
	default:
		switch v.Status {
		case "CANCELLED":
			e.StatusValue = "cancelled"
		case "TENTATIVE":
			e.StatusValue = "tentative"
		case "CONFIRMED":
			e.StatusValue = "confirmed"
		}
	}
	if v.Categories != nil {
		e.Categories = map[string]bool{}
		for _, c := range v.Categories {
			e.Categories[c] = true
		}
	}
	if v.Location != "" {
		e.Locations = map[string]Location{
			"loc1": {Name: v.Location},
		}
	}
	// Participants: organizer + attendees. The organizer always carries
	// the "owner" role; attendees pick up "attendee" by default but
	// CHAIR/OPT-PARTICIPANT/NON-PARTICIPANT translate to dedicated
	// roles.
	parts := map[string]Participant{}
	if v.Organizer.CalAddress != "" {
		p := attendeeToParticipant(v.Organizer)
		p.Roles = map[string]bool{"owner": true}
		// Organisers are always individuals in iMIP.
		if p.Kind == "" {
			p.Kind = "individual"
		}
		parts["organizer"] = p
	}
	for i, a := range v.Attendees {
		key := fmt.Sprintf("a%d", i+1)
		p := attendeeToParticipant(a)
		if p.Roles == nil {
			p.Roles = map[string]bool{}
		}
		switch a.Role {
		case "CHAIR":
			p.Roles["chair"] = true
			p.Roles["attendee"] = true
		case "OPT-PARTICIPANT":
			p.Roles["optional"] = true
			p.Roles["attendee"] = true
		case "NON-PARTICIPANT":
			p.Roles["informational"] = true
		default:
			p.Roles["attendee"] = true
		}
		if p.Kind == "" {
			p.Kind = "individual"
		}
		parts[key] = p
	}
	if len(parts) > 0 {
		e.Participants = parts
	}
	// RRULE -> RecurrenceRule (best-effort).
	if v.RRule != "" {
		if rule, err := parseRRuleString(v.RRule); err == nil {
			e.RecurrenceRules = []RecurrenceRule{rule}
		}
	}
	// EXDATE -> excluded-occurrence overrides keyed by the original
	// start. Each entry becomes a {"@removed": true} block.
	if len(v.ExDates) > 0 {
		if e.RecurrenceOverrides == nil {
			e.RecurrenceOverrides = map[time.Time]map[string]json.RawMessage{}
		}
		for _, t := range v.ExDates {
			e.RecurrenceOverrides[t] = map[string]json.RawMessage{
				"@removed": json.RawMessage("true"),
			}
		}
	}
	// Forward-compat properties: carry as raw keys so
	// Event.MarshalJSON later includes them. We use a "ical:<NAME>"
	// namespace to avoid colliding with JSCalendar properties.
	if len(v.Other) > 0 {
		if e.RawJSON == nil {
			e.RawJSON = map[string]json.RawMessage{}
		}
		for _, p := range v.Other {
			key := "ical:" + strings.ToLower(p.Name)
			e.RawJSON[key] = mustMarshal(p.Value)
		}
	}
	return e, nil
}

// attendeeToParticipant lifts the iCalendar attendee fields to the
// JSCalendar Participant shape (sans roles, which the caller sets).
func attendeeToParticipant(a Attendee) Participant {
	p := Participant{
		Email:       strings.TrimPrefix(strings.ToLower(strings.TrimSpace(a.CalAddress)), "mailto:"),
		Name:        a.CN,
		ExpectReply: a.RSVP,
	}
	switch a.PartStat {
	case "ACCEPTED":
		p.ParticipationStatus = "accepted"
	case "DECLINED":
		p.ParticipationStatus = "declined"
	case "TENTATIVE":
		p.ParticipationStatus = "tentative"
	case "DELEGATED":
		p.ParticipationStatus = "delegated"
	case "NEEDS-ACTION", "":
		p.ParticipationStatus = "needs-action"
	}
	return p
}

// parseRRuleString parses the RFC 5545 RRULE value (already stripped
// of the property prefix) into a RecurrenceRule. Coverage matches
// what Expand evaluates; unknown parts go into RawJSON so a round-
// trip is lossless even when we cannot expand the rule.
func parseRRuleString(s string) (RecurrenceRule, error) {
	r := RecurrenceRule{RawJSON: map[string]json.RawMessage{}}
	for _, part := range strings.Split(s, ";") {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(part[:eq]))
		value := strings.TrimSpace(part[eq+1:])
		switch key {
		case "FREQ":
			r.Frequency = strings.ToLower(value)
		case "INTERVAL":
			if n, err := strconv.Atoi(value); err == nil {
				r.Interval = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(value); err == nil {
				r.Count = &n
			}
		case "UNTIL":
			t := parseICSTime(value, nil)
			if !t.IsZero() {
				r.Until = &t
			}
		case "BYMONTH":
			r.ByMonth = parseIntList(value)
		case "BYMONTHDAY":
			r.ByMonthDay = parseIntList(value)
		case "BYHOUR":
			r.ByHour = parseIntList(value)
		case "BYMINUTE":
			r.ByMinute = parseIntList(value)
		case "BYSECOND":
			r.BySecond = parseIntList(value)
		case "BYSETPOS":
			r.BySetPosition = parseIntList(value)
		case "BYDAY":
			r.ByDay = parseByDayList(value)
		case "WKST":
			r.WeekStart = strings.ToLower(value)
		default:
			r.RawJSON["ical:"+strings.ToLower(key)] = mustMarshal(value)
		}
	}
	return r, nil
}

// parseIntList parses a comma-separated integer list. Bad entries are
// dropped silently so a partial RRULE still yields a usable subset.
func parseIntList(s string) []int {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// parseByDayList parses iCalendar BYDAY values like "MO,TU,-1FR" into
// JSCalendar ByDay entries.
func parseByDayList(s string) []ByDay {
	parts := strings.Split(s, ",")
	out := make([]ByDay, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ord := 0
		// Pull the leading sign + digits, if present.
		i := 0
		if i < len(p) && (p[i] == '+' || p[i] == '-') {
			i++
		}
		for i < len(p) && p[i] >= '0' && p[i] <= '9' {
			i++
		}
		if i > 0 && i < len(p) {
			if n, err := strconv.Atoi(p[:i]); err == nil {
				ord = n
			}
		}
		day := strings.ToLower(p[i:])
		if len(day) != 2 {
			continue
		}
		out = append(out, ByDay{Day: day, NthOfPeriod: ord})
	}
	return out
}
