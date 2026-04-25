package jscalendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RecurrenceRule is one entry in Event.RecurrenceRules per RFC 8984
// §5.3.1. We carry every field the spec defines so a round-trip is
// lossless even when Expand cannot evaluate the rule (sub-daily
// frequencies, BYSETPOS in master, ...). RawJSON preserves
// forward-compat fields the same way Event.RawJSON does.
type RecurrenceRule struct {
	Frequency     string     `json:"frequency,omitempty"`
	Interval      int        `json:"interval,omitempty"`
	Count         *int       `json:"count,omitempty"`
	Until         *time.Time `json:"until,omitempty"`
	ByMonth       []int      `json:"byMonth,omitempty"`
	ByMonthDay    []int      `json:"byMonthDay,omitempty"`
	ByDay         []ByDay    `json:"byDay,omitempty"`
	ByHour        []int      `json:"byHour,omitempty"`
	ByMinute      []int      `json:"byMinute,omitempty"`
	BySecond      []int      `json:"bySecond,omitempty"`
	BySetPosition []int      `json:"bySetPosition,omitempty"`
	WeekStart     string     `json:"firstDayOfWeek,omitempty"`

	RawJSON map[string]json.RawMessage `json:"-"`
}

// ByDay is one entry in RecurrenceRule.ByDay. Day is the lower-case
// two-letter weekday ("mo".."su"); NthOfPeriod is the optional ordinal
// selector (e.g. -1 for "last Friday", 2 for "second Tuesday"). The
// RFC names the field "nthOfPeriod"; we keep that on the wire and
// expose Nth as the natural Go name.
type ByDay struct {
	Day         string `json:"day"`
	NthOfPeriod int    `json:"nthOfPeriod,omitempty"`
}

// MarshalJSON / UnmarshalJSON handle RecurrenceRule's hybrid model the
// same way Event does. We need a custom shape because Until is a
// LocalDateTime string on the wire and *time.Time isn't auto-handled.
func (r RecurrenceRule) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(r.RawJSON)+12)
	for k, v := range r.RawJSON {
		out[k] = v
	}
	if r.Frequency != "" {
		out["frequency"] = mustMarshal(r.Frequency)
	}
	if r.Interval > 0 {
		out["interval"] = mustMarshal(r.Interval)
	}
	if r.Count != nil {
		out["count"] = mustMarshal(*r.Count)
	}
	if r.Until != nil {
		out["until"] = mustMarshal(r.Until.UTC().Format("2006-01-02T15:04:05"))
	}
	if r.ByMonth != nil {
		out["byMonth"] = mustMarshal(r.ByMonth)
	}
	if r.ByMonthDay != nil {
		out["byMonthDay"] = mustMarshal(r.ByMonthDay)
	}
	if r.ByDay != nil {
		out["byDay"] = mustMarshal(r.ByDay)
	}
	if r.ByHour != nil {
		out["byHour"] = mustMarshal(r.ByHour)
	}
	if r.ByMinute != nil {
		out["byMinute"] = mustMarshal(r.ByMinute)
	}
	if r.BySecond != nil {
		out["bySecond"] = mustMarshal(r.BySecond)
	}
	if r.BySetPosition != nil {
		out["bySetPosition"] = mustMarshal(r.BySetPosition)
	}
	if r.WeekStart != "" {
		out["firstDayOfWeek"] = mustMarshal(r.WeekStart)
	}
	return canonicalJSONObject(out), nil
}

// UnmarshalJSON populates a RecurrenceRule. Unknown keys flow through
// RawJSON; Until is parsed as a LocalDateTime in the rule's "in zone"
// (we treat it as UTC here since the recurrence engine resolves
// everything to UTC at expansion time).
func (r *RecurrenceRule) UnmarshalJSON(b []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("jscalendar: rrule: %w", err)
	}
	r.RawJSON = raw
	r.Frequency = projectString(raw, "frequency")
	r.WeekStart = projectString(raw, "firstDayOfWeek")
	if v, ok := raw["interval"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &r.Interval)
	}
	if v, ok := raw["count"]; ok && len(v) > 0 && string(v) != "null" {
		var c int
		if err := json.Unmarshal(v, &c); err == nil {
			r.Count = &c
		}
	}
	if v, ok := raw["until"]; ok && len(v) > 0 && string(v) != "null" {
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			if t, err := resolveLocalDateTimeString(s, ""); err == nil {
				r.Until = &t
			}
		}
	}
	for _, key := range []string{"byMonth", "byMonthDay", "byHour", "byMinute", "bySecond", "bySetPosition"} {
		if v, ok := raw[key]; ok && len(v) > 0 && string(v) != "null" {
			var arr []int
			if err := json.Unmarshal(v, &arr); err == nil {
				switch key {
				case "byMonth":
					r.ByMonth = arr
				case "byMonthDay":
					r.ByMonthDay = arr
				case "byHour":
					r.ByHour = arr
				case "byMinute":
					r.ByMinute = arr
				case "bySecond":
					r.BySecond = arr
				case "bySetPosition":
					r.BySetPosition = arr
				}
			}
		}
	}
	if v, ok := raw["byDay"]; ok && len(v) > 0 && string(v) != "null" {
		var arr []ByDay
		if err := json.Unmarshal(v, &arr); err == nil {
			r.ByDay = arr
		}
	}
	return nil
}

// Occurrence is one expansion of a RecurrenceRule (or, for non-
// recurring events, the master itself). The store keeps a denormalised
// projection of upcoming occurrences for the calendar/query method.
type Occurrence struct {
	Start    time.Time
	End      time.Time
	EventID  string
	Override bool
}

// ErrUnsupportedRecurrence is returned by Expand for rule shapes the
// v1 engine intentionally does not evaluate (sub-daily frequencies,
// pathological BYSETPOS interactions). Callers map this to a degraded
// user-facing experience: the master event still appears, but the
// recurring instances do not. See doc.go for the deferred shape.
var ErrUnsupportedRecurrence = errors.New("jscalendar: unsupported recurrence shape")

// defaultMaxOccurrences caps Expand at one year of daily events so a
// pathological "every second forever" rule cannot OOM the server. The
// figure mirrors what Apple Calendar uses internally for client-side
// expansion.
const defaultMaxOccurrences = 366

// Expand returns the start times of each occurrence within
// [from, until] inclusive. The master Event must have a non-zero Start
// and at least one RecurrenceRule (or the master is treated as a
// single-shot event and returned only when it falls in range).
//
// Honours RecurrenceOverrides: an override block with "@removed":true
// or "excluded":true is skipped entirely; an override with a "start"
// shifts the occurrence to that time.
func (e *Event) Expand(from, until time.Time, maxOccurrences int) ([]Occurrence, error) {
	if e == nil {
		return nil, errors.New("jscalendar: expand: nil event")
	}
	if e.Start.IsZero() {
		return nil, errors.New("jscalendar: expand: event has no start")
	}
	if maxOccurrences <= 0 {
		maxOccurrences = defaultMaxOccurrences
	}
	if !e.IsRecurring() {
		// Single-shot path: emit the master if it overlaps the window.
		if !e.Start.Before(from) && !e.Start.After(until) {
			return []Occurrence{{
				Start:   e.Start,
				End:     e.Start.Add(e.Duration.Value),
				EventID: e.UID,
			}}, nil
		}
		return nil, nil
	}

	// We expand each rule independently and merge. RFC 8984 says
	// multiple rules union (excludedRecurrenceRules subtract); v1
	// implements the union for inclusion rules. Subtraction is a TODO.
	var raw []time.Time
	for _, rule := range e.RecurrenceRules {
		got, err := expandRule(rule, e.Start, from, until, maxOccurrences*2)
		if err != nil {
			return nil, err
		}
		raw = append(raw, got...)
	}

	// Deduplicate via a map keyed by RFC 3339 second-resolution string.
	seen := map[time.Time]struct{}{}
	deduped := raw[:0]
	for _, t := range raw {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		deduped = append(deduped, t)
	}
	sort.Slice(deduped, func(i, j int) bool { return deduped[i].Before(deduped[j]) })
	if len(deduped) > maxOccurrences {
		deduped = deduped[:maxOccurrences]
	}

	out := make([]Occurrence, 0, len(deduped))
	for _, t := range deduped {
		// Apply override: removal short-circuits, start-shift updates.
		if override, ok := e.RecurrenceOverrides[t]; ok {
			if isRemovalOverride(override) {
				continue
			}
			occ := Occurrence{Start: t, EventID: e.UID, Override: true}
			if v, ok := override["start"]; ok && len(v) > 0 {
				if shifted, err := resolveLocalDateTimeString(unquote(v), e.TimeZone); err == nil {
					occ.Start = shifted
				}
			}
			occ.End = occ.Start.Add(e.Duration.Value)
			if v, ok := override["duration"]; ok && len(v) > 0 {
				var d Duration
				if err := json.Unmarshal(v, &d); err == nil && d.Value != 0 {
					occ.End = occ.Start.Add(d.Value)
				}
			}
			out = append(out, occ)
			continue
		}
		out = append(out, Occurrence{
			Start:   t,
			End:     t.Add(e.Duration.Value),
			EventID: e.UID,
		})
	}
	return out, nil
}

// expandRule produces the occurrence set for a single RRULE entry.
// Defers sub-daily frequencies to Phase 3 (returns
// ErrUnsupportedRecurrence). The supplied capLoose is a per-rule cap
// before deduplication; the caller trims to the user-facing
// maxOccurrences after merging.
func expandRule(rule RecurrenceRule, start, from, until time.Time, capLoose int) ([]time.Time, error) {
	freq := strings.ToLower(rule.Frequency)
	switch freq {
	case "yearly", "monthly", "weekly", "daily":
		// ok
	case "hourly", "minutely", "secondly":
		// TODO(phase3): sub-daily expansion.
		return nil, fmt.Errorf("%w: frequency %q", ErrUnsupportedRecurrence, rule.Frequency)
	default:
		return nil, fmt.Errorf("%w: unknown frequency %q", ErrUnsupportedRecurrence, rule.Frequency)
	}
	interval := rule.Interval
	if interval <= 0 {
		interval = 1
	}
	var (
		out      []time.Time
		emitted  int
		periodAt = start
	)
	target := capLoose
	if rule.Count != nil && *rule.Count < target {
		target = *rule.Count
	}
	// Periods are stepped relative to `start`, not the window's `from`,
	// so the same series produces the same emission set regardless of
	// query window. We then filter by `from`/`until`.
	guard := 0
	for guard < 10000 {
		guard++
		// Per-period candidates.
		candidates := candidatesForPeriod(rule, periodAt)
		// Filter by Until and emit.
		for _, t := range candidates {
			if rule.Until != nil && t.After(*rule.Until) {
				return out, nil
			}
			// Honour Count against emitted-since-start, even when the
			// query window discards the early ones.
			if rule.Count != nil && emitted >= *rule.Count {
				return out, nil
			}
			emitted++
			if t.Before(from) {
				continue
			}
			if t.After(until) {
				return out, nil
			}
			out = append(out, t)
			if len(out) >= target {
				return out, nil
			}
		}
		// Step to next period.
		next, err := stepPeriod(periodAt, freq, interval)
		if err != nil {
			return out, err
		}
		// Stop early when we are clearly past the query window so
		// pathological "until = year 9999, no count" rules terminate.
		if next.After(until) && (rule.Until == nil || next.After(*rule.Until)) {
			return out, nil
		}
		periodAt = next
	}
	return out, nil
}

// stepPeriod advances by Interval units of the rule's frequency.
// Calendar arithmetic uses time.AddDate so month-end / leap-year
// behaviour matches Go's stdlib (rolls forward on overflow).
func stepPeriod(t time.Time, freq string, interval int) (time.Time, error) {
	switch freq {
	case "yearly":
		return t.AddDate(interval, 0, 0), nil
	case "monthly":
		return t.AddDate(0, interval, 0), nil
	case "weekly":
		return t.AddDate(0, 0, 7*interval), nil
	case "daily":
		return t.AddDate(0, 0, interval), nil
	}
	return time.Time{}, fmt.Errorf("%w: stepPeriod %q", ErrUnsupportedRecurrence, freq)
}

// candidatesForPeriod expands a single period (year/month/week/day) of
// the rule. The cardinality is bounded by the period (max 31 days,
// 12 months, 7 weekdays) so allocation stays predictable.
func candidatesForPeriod(rule RecurrenceRule, periodAt time.Time) []time.Time {
	freq := strings.ToLower(rule.Frequency)
	switch freq {
	case "daily":
		// One slot per period; the BY* filters are still honoured.
		if rule.ByMonth != nil && !intIn(int(periodAt.Month()), rule.ByMonth) {
			return nil
		}
		if rule.ByMonthDay != nil && !intIn(periodAt.Day(), rule.ByMonthDay) {
			return nil
		}
		if rule.ByDay != nil && !weekdayIn(periodAt.Weekday(), rule.ByDay) {
			return nil
		}
		return []time.Time{periodAt}
	case "weekly":
		// Expand the seven days of the week containing periodAt.
		// Week start defaults to Monday per RFC 8984; we honour
		// rule.WeekStart when present.
		ws := weekdayFromName(rule.WeekStart, time.Monday)
		// Walk back to the start of the week.
		offset := (int(periodAt.Weekday()) - int(ws) + 7) % 7
		weekStart := periodAt.AddDate(0, 0, -offset)
		var out []time.Time
		for i := 0; i < 7; i++ {
			day := weekStart.AddDate(0, 0, i)
			if rule.ByMonth != nil && !intIn(int(day.Month()), rule.ByMonth) {
				continue
			}
			if rule.ByDay != nil && !weekdayIn(day.Weekday(), rule.ByDay) {
				continue
			}
			// When ByDay is unset, only the "anchor" weekday of the
			// series fires — the day matching the master Start.
			if rule.ByDay == nil && day.Weekday() != periodAt.Weekday() {
				continue
			}
			out = append(out, day)
		}
		return out
	case "monthly":
		// Expand the days of the month containing periodAt.
		first := time.Date(periodAt.Year(), periodAt.Month(), 1,
			periodAt.Hour(), periodAt.Minute(), periodAt.Second(), periodAt.Nanosecond(), periodAt.Location())
		if rule.ByMonth != nil && !intIn(int(first.Month()), rule.ByMonth) {
			return nil
		}
		var out []time.Time
		// Walk every day in this month.
		for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
			if rule.ByMonthDay != nil && !intIn(d.Day(), rule.ByMonthDay) {
				continue
			}
			if rule.ByDay != nil {
				if !weekdayMatchesByDay(d, rule.ByDay, "monthly") {
					continue
				}
			} else if rule.ByMonthDay == nil {
				// No BY filters -> only the anchor day-of-month fires.
				if d.Day() != periodAt.Day() {
					continue
				}
			}
			out = append(out, d)
		}
		// Apply BySetPosition narrowing (a small subset: pick the Nth
		// element of the per-period set, 1-indexed; negative selects
		// from the end).
		if len(rule.BySetPosition) > 0 {
			out = applyBySetPosition(out, rule.BySetPosition)
		}
		return out
	case "yearly":
		// Expand by month/day combinations within the year of periodAt.
		months := rule.ByMonth
		if len(months) == 0 {
			months = []int{int(periodAt.Month())}
		}
		var out []time.Time
		for _, m := range months {
			first := time.Date(periodAt.Year(), time.Month(m), 1,
				periodAt.Hour(), periodAt.Minute(), periodAt.Second(), periodAt.Nanosecond(), periodAt.Location())
			for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
				if rule.ByMonthDay != nil && !intIn(d.Day(), rule.ByMonthDay) {
					continue
				}
				if rule.ByDay != nil {
					if !weekdayMatchesByDay(d, rule.ByDay, "monthly") {
						continue
					}
				} else if rule.ByMonthDay == nil {
					if d.Day() != periodAt.Day() {
						continue
					}
				}
				out = append(out, d)
			}
		}
		if len(rule.BySetPosition) > 0 {
			out = applyBySetPosition(out, rule.BySetPosition)
		}
		return out
	}
	return nil
}

// weekdayMatchesByDay decides whether the candidate day matches any
// entry in rule.ByDay, honouring the optional NthOfPeriod ordinal
// (e.g. -1FR = "the last Friday of the period"). The period scope is
// supplied so we can compute "last X" relative to month vs year.
func weekdayMatchesByDay(d time.Time, byDay []ByDay, scope string) bool {
	for _, bd := range byDay {
		if !weekdayNameMatches(d.Weekday(), bd.Day) {
			continue
		}
		if bd.NthOfPeriod == 0 {
			return true
		}
		// Compute the ordinal of this weekday within the period.
		ordinal := nthOfWeekdayInPeriod(d, scope)
		if bd.NthOfPeriod > 0 {
			if ordinal == bd.NthOfPeriod {
				return true
			}
		} else {
			// Negative: count from the end. -1 is "last".
			total := totalOfWeekdayInPeriod(d, scope)
			if ordinal == total+bd.NthOfPeriod+1 {
				return true
			}
		}
	}
	return false
}

// nthOfWeekdayInPeriod returns the 1-indexed ordinal of d among
// same-weekday days in the surrounding period.
func nthOfWeekdayInPeriod(d time.Time, scope string) int {
	first := time.Date(d.Year(), d.Month(), 1, d.Hour(), d.Minute(), d.Second(), d.Nanosecond(), d.Location())
	if scope == "yearly" {
		first = time.Date(d.Year(), time.January, 1, d.Hour(), d.Minute(), d.Second(), d.Nanosecond(), d.Location())
	}
	count := 0
	for cur := first; !cur.After(d); cur = cur.AddDate(0, 0, 1) {
		if cur.Weekday() == d.Weekday() {
			count++
		}
		if scope == "monthly" && cur.Month() != d.Month() {
			break
		}
	}
	return count
}

// totalOfWeekdayInPeriod returns the total count of same-weekday days
// in the surrounding period.
func totalOfWeekdayInPeriod(d time.Time, scope string) int {
	first := time.Date(d.Year(), d.Month(), 1, d.Hour(), d.Minute(), d.Second(), d.Nanosecond(), d.Location())
	end := first.AddDate(0, 1, 0)
	if scope == "yearly" {
		first = time.Date(d.Year(), time.January, 1, d.Hour(), d.Minute(), d.Second(), d.Nanosecond(), d.Location())
		end = first.AddDate(1, 0, 0)
	}
	count := 0
	for cur := first; cur.Before(end); cur = cur.AddDate(0, 0, 1) {
		if cur.Weekday() == d.Weekday() {
			count++
		}
	}
	return count
}

// applyBySetPosition narrows a per-period candidate set per RFC 5545
// BYSETPOS. 1-indexed, negative selects from end.
func applyBySetPosition(in []time.Time, positions []int) []time.Time {
	if len(in) == 0 {
		return in
	}
	var out []time.Time
	for _, p := range positions {
		var idx int
		if p > 0 {
			idx = p - 1
		} else if p < 0 {
			idx = len(in) + p
		} else {
			continue
		}
		if idx < 0 || idx >= len(in) {
			continue
		}
		out = append(out, in[idx])
	}
	return out
}

// isRemovalOverride checks for either of the two flag-spellings we
// accept on a recurrenceOverrides block: "@removed" (legacy Apple) and
// "excluded" (RFC 8984 §5.3.4 canonical).
func isRemovalOverride(block map[string]json.RawMessage) bool {
	for _, k := range []string{"@removed", "excluded"} {
		if v, ok := block[k]; ok {
			if string(v) == "true" {
				return true
			}
		}
	}
	return false
}

// unquote strips JSON string quotes; helper for the override-start
// extraction path so we don't allocate via Unmarshal for a single
// string.
func unquote(v json.RawMessage) string {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

// intIn reports whether target is in the slice.
func intIn(target int, in []int) bool {
	for _, v := range in {
		if v == target {
			return true
		}
	}
	return false
}

// weekdayIn reports whether the calendar weekday matches any ByDay
// entry (ignoring the ordinal selector — used by the daily/weekly
// frequencies where ordinal is meaningless).
func weekdayIn(d time.Weekday, in []ByDay) bool {
	for _, bd := range in {
		if weekdayNameMatches(d, bd.Day) {
			return true
		}
	}
	return false
}

// weekdayNameMatches maps the JSCalendar two-letter weekday code to a
// time.Weekday and compares.
func weekdayNameMatches(d time.Weekday, name string) bool {
	want := weekdayFromName(name, -1)
	return want == d
}

// weekdayFromName maps a JSCalendar two-letter day code to a
// time.Weekday. Returns fallback for unknown codes.
func weekdayFromName(name string, fallback time.Weekday) time.Weekday {
	switch strings.ToLower(name) {
	case "su":
		return time.Sunday
	case "mo":
		return time.Monday
	case "tu":
		return time.Tuesday
	case "we":
		return time.Wednesday
	case "th":
		return time.Thursday
	case "fr":
		return time.Friday
	case "sa":
		return time.Saturday
	}
	return fallback
}
