package jscalendar_test

import (
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap/calendars/jscalendar"
)

// makeEvent is a tiny helper for the recurrence tests; the JSCalendar
// fixture path goes through Unmarshal, but a struct-direct fixture is
// clearer when the assertion is "given this rule, expect these
// timestamps".
func makeEvent(start time.Time, dur time.Duration, rule jscalendar.RecurrenceRule) jscalendar.Event {
	return jscalendar.Event{
		Type:            "Event",
		UID:             "test-uid",
		Start:           start,
		Duration:        jscalendar.Duration{Value: dur},
		RecurrenceRules: []jscalendar.RecurrenceRule{rule},
	}
}

func TestExpand_DailyCount(t *testing.T) {
	count := 5
	start := time.Date(2025, 4, 24, 10, 0, 0, 0, time.UTC)
	ev := makeEvent(start, time.Hour, jscalendar.RecurrenceRule{
		Frequency: "daily",
		Count:     &count,
	})
	occs, err := ev.Expand(start, start.AddDate(0, 0, 30), 100)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(occs) != 5 {
		t.Fatalf("len(occs) = %d, want 5", len(occs))
	}
	for i, occ := range occs {
		want := start.AddDate(0, 0, i)
		if !occ.Start.Equal(want) {
			t.Errorf("occs[%d].Start = %v, want %v", i, occ.Start, want)
		}
		if occ.End.Sub(occ.Start) != time.Hour {
			t.Errorf("occs[%d].End - Start = %v, want 1h", i, occ.End.Sub(occ.Start))
		}
	}
}

func TestExpand_WeeklyMondays(t *testing.T) {
	start := time.Date(2025, 4, 28, 9, 0, 0, 0, time.UTC) // a Monday
	ev := makeEvent(start, 30*time.Minute, jscalendar.RecurrenceRule{
		Frequency: "weekly",
		Interval:  1,
		ByDay:     []jscalendar.ByDay{{Day: "mo"}},
	})
	occs, err := ev.Expand(start, start.AddDate(0, 0, 28), 100)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(occs) != 5 {
		t.Fatalf("len(occs) = %d (want 5: 4 weeks of Mondays incl. start)", len(occs))
	}
	for _, occ := range occs {
		if occ.Start.Weekday() != time.Monday {
			t.Errorf("occurrence on %v, want Monday", occ.Start.Weekday())
		}
	}
}

func TestExpand_MonthlyLastFriday(t *testing.T) {
	// Series anchored to last Friday of April 2025 (April 25); the
	// expected sequence is the last Friday of each month for 12 months.
	start := time.Date(2025, 4, 25, 14, 0, 0, 0, time.UTC)
	count := 12
	ev := makeEvent(start, time.Hour, jscalendar.RecurrenceRule{
		Frequency: "monthly",
		Interval:  1,
		ByDay:     []jscalendar.ByDay{{Day: "fr", NthOfPeriod: -1}},
		Count:     &count,
	})
	occs, err := ev.Expand(start, start.AddDate(2, 0, 0), 100)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(occs) != 12 {
		t.Fatalf("len(occs) = %d, want 12 (one last-Friday per month)", len(occs))
	}
	for _, occ := range occs {
		if occ.Start.Weekday() != time.Friday {
			t.Errorf("occurrence on %v, want Friday", occ.Start.Weekday())
		}
		// Verify "last": the next Friday should be in the next month.
		next := occ.Start.AddDate(0, 0, 7)
		if next.Month() == occ.Start.Month() {
			t.Errorf("Friday %v is not the last of its month", occ.Start)
		}
	}
}

func TestExpand_Until(t *testing.T) {
	start := time.Date(2025, 4, 24, 10, 0, 0, 0, time.UTC)
	until := time.Date(2025, 4, 28, 23, 0, 0, 0, time.UTC)
	ev := makeEvent(start, time.Hour, jscalendar.RecurrenceRule{
		Frequency: "daily",
		Until:     &until,
	})
	occs, err := ev.Expand(start, start.AddDate(0, 0, 30), 100)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// 24, 25, 26, 27, 28 = 5
	if len(occs) != 5 {
		t.Fatalf("len(occs) = %d, want 5 (until inclusive)", len(occs))
	}
}

func TestExpand_RemovalOverride(t *testing.T) {
	start := time.Date(2025, 4, 24, 10, 0, 0, 0, time.UTC)
	removed := time.Date(2025, 4, 25, 10, 0, 0, 0, time.UTC)
	ev := mustParseEvent(t, `{
		"@type":"Event","uid":"u","start":"2025-04-24T10:00:00","duration":"PT1H",
		"recurrenceRules":[{"frequency":"daily","count":3}],
		"recurrenceOverrides":{"2025-04-25T10:00:00":{"@removed":true}}
	}`)
	occs, err := ev.Expand(start, start.AddDate(0, 0, 5), 100)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	for _, occ := range occs {
		if occ.Start.Equal(removed) {
			t.Errorf("removed occurrence still present: %v", occ.Start)
		}
	}
	if len(occs) != 2 {
		t.Fatalf("len(occs) = %d, want 2 (3 - 1 removed)", len(occs))
	}
}

func TestExpand_StartShiftOverride(t *testing.T) {
	start := time.Date(2025, 4, 24, 10, 0, 0, 0, time.UTC)
	ev := mustParseEvent(t, `{
		"@type":"Event","uid":"u","start":"2025-04-24T10:00:00","duration":"PT1H",
		"recurrenceRules":[{"frequency":"daily","count":3}],
		"recurrenceOverrides":{"2025-04-25T10:00:00":{"start":"2025-04-25T15:00:00"}}
	}`)
	occs, err := ev.Expand(start, start.AddDate(0, 0, 5), 100)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	found := false
	for _, occ := range occs {
		if occ.Override && occ.Start.Hour() == 15 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an override occurrence at 15:00, got: %v", occs)
	}
}

func TestExpand_MaxOccurrencesCap(t *testing.T) {
	count := 1000
	start := time.Date(2025, 4, 24, 10, 0, 0, 0, time.UTC)
	ev := makeEvent(start, time.Hour, jscalendar.RecurrenceRule{
		Frequency: "daily",
		Count:     &count,
	})
	occs, err := ev.Expand(start, start.AddDate(10, 0, 0), 7)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(occs) != 7 {
		t.Fatalf("len(occs) = %d, want 7 (cap)", len(occs))
	}
}

func TestExpand_HourlyDeferred(t *testing.T) {
	count := 5
	ev := makeEvent(time.Now().UTC(), time.Hour, jscalendar.RecurrenceRule{
		Frequency: "hourly",
		Count:     &count,
	})
	_, err := ev.Expand(time.Now(), time.Now().AddDate(0, 1, 0), 100)
	if !errors.Is(err, jscalendar.ErrUnsupportedRecurrence) {
		t.Fatalf("hourly expand err = %v, want ErrUnsupportedRecurrence", err)
	}
}

func TestExpand_NonRecurring(t *testing.T) {
	start := time.Date(2025, 4, 24, 10, 0, 0, 0, time.UTC)
	ev := jscalendar.Event{
		Type:     "Event",
		UID:      "u",
		Start:    start,
		Duration: jscalendar.Duration{Value: time.Hour},
	}
	occs, err := ev.Expand(start.AddDate(0, 0, -1), start.AddDate(0, 0, 1), 100)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(occs) != 1 {
		t.Fatalf("len(occs) = %d, want 1", len(occs))
	}
}
