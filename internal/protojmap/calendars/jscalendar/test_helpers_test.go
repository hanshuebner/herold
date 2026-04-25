package jscalendar_test

import (
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap/calendars/jscalendar"
)

// mustParseEvent unmarshals the JSON literal into an Event or fatals
// the test. Used by both jscalendar_test.go and recurrence_test.go to
// avoid duplicating the boilerplate across cases.
func mustParseEvent(t *testing.T, body string) jscalendar.Event {
	t.Helper()
	var ev jscalendar.Event
	if err := ev.UnmarshalJSON([]byte(body)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	return ev
}
