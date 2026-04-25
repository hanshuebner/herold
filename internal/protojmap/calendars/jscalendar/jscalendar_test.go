package jscalendar_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap/calendars/jscalendar"
)

// canonicalize re-serialises a JSON value through map[string]any so
// the result is byte-identical regardless of map iteration order. The
// JSCalendar round-trip tests rely on this for byte equality.
func canonicalize(t *testing.T, b []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("canonicalize: %v: %s", err, b)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("canonicalize re-marshal: %v", err)
	}
	return out
}

func TestEvent_RoundTrip_PreservesUnknownFields(t *testing.T) {
	in := []byte(`{
		"@type":"Event",
		"uid":"event-1@example.test",
		"title":"Project sync",
		"description":"Weekly catchup",
		"start":"2025-04-24T10:00:00",
		"timeZone":"Europe/Berlin",
		"duration":"PT1H",
		"status":"confirmed",
		"sequence":2,
		"showWithoutTime":false,
		"categories":{"work":true,"sync":true},
		"keywords":{"foo":true},
		"locations":{"l1":{"name":"Room 7"}},
		"participants":{
			"p1":{
				"name":"Ada",
				"sendTo":{"imip":"mailto:ada@example.test"},
				"roles":{"owner":true},
				"kind":"individual",
				"participationStatus":"accepted",
				"expectReply":true
			}
		},
		"futureField":{"foo":"bar"}
	}`)
	var ev jscalendar.Event
	if err := ev.UnmarshalJSON(in); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if ev.Type != "Event" {
		t.Fatalf("Type = %q, want Event", ev.Type)
	}
	if ev.UID != "event-1@example.test" {
		t.Fatalf("UID = %q", ev.UID)
	}
	if ev.Duration.Value != time.Hour {
		t.Fatalf("Duration = %v, want 1h", ev.Duration.Value)
	}
	if ev.Start.IsZero() {
		t.Fatalf("Start zero")
	}
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(canonicalize(t, in)) != string(canonicalize(t, out)) {
		t.Fatalf("round-trip differs:\n in: %s\nout: %s", canonicalize(t, in), canonicalize(t, out))
	}
}

func TestEvent_Validate(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "missing @type",
			body:    `{"uid":"u","start":"2025-04-24T10:00:00"}`,
			wantErr: "@type",
		},
		{
			name:    "task type rejected",
			body:    `{"@type":"Task","uid":"u","start":"2025-04-24T10:00:00"}`,
			wantErr: "Event-only",
		},
		{
			name:    "missing uid",
			body:    `{"@type":"Event","start":"2025-04-24T10:00:00"}`,
			wantErr: "uid",
		},
		{
			name:    "zero start",
			body:    `{"@type":"Event","uid":"u"}`,
			wantErr: "start",
		},
		{
			name:    "valid",
			body:    `{"@type":"Event","uid":"u","start":"2025-04-24T10:00:00"}`,
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ev jscalendar.Event
			if err := ev.UnmarshalJSON([]byte(tc.body)); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			err := ev.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate: want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate err = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestEvent_StatusDefault(t *testing.T) {
	in := []byte(`{"@type":"Event","uid":"u","start":"2025-04-24T10:00:00"}`)
	var ev jscalendar.Event
	if err := ev.UnmarshalJSON(in); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if ev.Status() != "confirmed" {
		t.Fatalf("Status() = %q, want confirmed", ev.Status())
	}
	ev.StatusValue = "tentative"
	if ev.Status() != "tentative" {
		t.Fatalf("Status() = %q, want tentative", ev.Status())
	}
}

func TestEvent_OrganizerEmail(t *testing.T) {
	in := []byte(`{
		"@type":"Event","uid":"u","start":"2025-04-24T10:00:00",
		"participants":{
			"a":{"sendTo":{"imip":"mailto:attendee@example.test"},"roles":{"attendee":true}},
			"o":{"sendTo":{"imip":"mailto:owner@example.test"},"roles":{"owner":true}}
		}
	}`)
	var ev jscalendar.Event
	if err := ev.UnmarshalJSON(in); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := ev.OrganizerEmail(); got != "owner@example.test" {
		t.Fatalf("OrganizerEmail = %q, want owner@example.test", got)
	}
}

func TestEvent_IsRecurring(t *testing.T) {
	in := []byte(`{
		"@type":"Event","uid":"u","start":"2025-04-24T10:00:00",
		"recurrenceRules":[{"frequency":"daily","count":5}]
	}`)
	var ev jscalendar.Event
	if err := ev.UnmarshalJSON(in); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !ev.IsRecurring() {
		t.Fatalf("IsRecurring = false, want true")
	}
}

func TestDuration_RoundTrip(t *testing.T) {
	cases := []struct {
		s string
		d time.Duration
	}{
		{"PT1H", time.Hour},
		{"PT30M", 30 * time.Minute},
		{"PT1H30M", 90 * time.Minute},
		{"P1D", 24 * time.Hour},
		{"P1W", 7 * 24 * time.Hour},
		{"P1DT2H", 26 * time.Hour},
		{"PT0S", 0},
		{"-PT1H", -time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.s, func(t *testing.T) {
			b, _ := json.Marshal(tc.s)
			var d jscalendar.Duration
			if err := d.UnmarshalJSON(b); err != nil {
				t.Fatalf("Unmarshal %q: %v", tc.s, err)
			}
			if d.Value != tc.d {
				t.Fatalf("%q -> %v, want %v", tc.s, d.Value, tc.d)
			}
		})
	}
}

func TestEvent_RecurrenceOverrides(t *testing.T) {
	in := []byte(`{
		"@type":"Event","uid":"u","start":"2025-04-24T10:00:00",
		"recurrenceRules":[{"frequency":"daily","count":3}],
		"recurrenceOverrides":{
			"2025-04-25T10:00:00":{"@removed":true},
			"2025-04-26T10:00:00":{"start":"2025-04-26T11:00:00"}
		}
	}`)
	var ev jscalendar.Event
	if err := ev.UnmarshalJSON(in); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(ev.RecurrenceOverrides) != 2 {
		t.Fatalf("RecurrenceOverrides len = %d, want 2", len(ev.RecurrenceOverrides))
	}
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), "@removed") {
		t.Fatalf("Marshal lost @removed: %s", out)
	}
	if !strings.Contains(string(out), "2025-04-26T11:00:00") {
		t.Fatalf("Marshal lost shifted start: %s", out)
	}
}
