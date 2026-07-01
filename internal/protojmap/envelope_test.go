package protojmap

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestResolveCreationReferences exercises the RFC 8620 §5.3 creation-
// reference resolver: string values of shape "#<creationId>" are
// substituted with the real id from a prior /set response's "created"
// map, and unrelated strings (including "#<not-an-id>" or "#<unknown>")
// are left alone.
func TestResolveCreationReferences(t *testing.T) {
	priorEmailSet := Invocation{
		Name: "Email/set",
		Args: json.RawMessage(`{
			"accountId": "a1",
			"created": {
				"draft1": {"id": "msg-42"}
			}
		}`),
		CallID: "c0",
	}
	prior := []Invocation{priorEmailSet}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "top-level string",
			in:   `{"emailId":"#draft1"}`,
			want: `{"emailId":"msg-42"}`,
		},
		{
			name: "nested in create object",
			in:   `{"create":{"sub1":{"emailId":"#draft1","identityId":"i7"}}}`,
			want: `{"create":{"sub1":{"emailId":"msg-42","identityId":"i7"}}}`,
		},
		{
			name: "inside array value",
			in:   `{"destroy":["#draft1","msg-99"]}`,
			want: `{"destroy":["msg-42","msg-99"]}`,
		},
		{
			name: "unknown creationId left untouched",
			in:   `{"emailId":"#unknown"}`,
			want: `{"emailId":"#unknown"}`,
		},
		{
			name: "non-id-shaped suffix left untouched (e.g. hashtag in subject)",
			in:   `{"subject":"#friday/morning roundup"}`,
			want: `{"subject":"#friday/morning roundup"}`,
		},
		{
			name: "bare hash left untouched",
			in:   `{"label":"#"}`,
			want: `{"label":"#"}`,
		},
		{
			// RFC 8620 §5.3: creation references may appear as object keys
			// (e.g. mailboxIds: {"#newMb": true} where #newMb was a prior
			// Mailbox/set creation id). The resolver must substitute the key.
			name: "creation ref as object key (mailboxIds pattern)",
			in:   `{"mailboxIds":{"#draft1":true,"other-id":false}}`,
			want: `{"mailboxIds":{"msg-42":true,"other-id":false}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creations := gatherCreations(prior, nil)
			got, err := resolveCreationReferences(json.RawMessage(tc.in), creations)
			if err != nil {
				t.Fatalf("resolveCreationReferences: %v", err)
			}
			if !equalJSON(t, got, []byte(tc.want)) {
				t.Fatalf("got %s, want %s", string(got), tc.want)
			}
		})
	}
}

// TestGatherCreations_RequestEnvelope confirms the request-level
// createdIds map is merged into the resolution namespace alongside any
// /set "created" entries from prior invocations.
func TestGatherCreations_RequestEnvelope(t *testing.T) {
	prior := []Invocation{
		{
			Name:   "Email/set",
			Args:   json.RawMessage(`{"created":{"draft1":{"id":"msg-1"}}}`),
			CallID: "c0",
		},
	}
	envelope := map[Id]Id{"earlier": "msg-9"}
	creations := gatherCreations(prior, envelope)
	if creations["draft1"] != "msg-1" {
		t.Errorf("draft1 from prior /set: got %q, want msg-1", creations["draft1"])
	}
	if creations["earlier"] != "msg-9" {
		t.Errorf("earlier from envelope: got %q, want msg-9", creations["earlier"])
	}
}

// TestGatherCreations_SkipsErrorEntries confirms an "error" invocation
// in prior is not parsed for creation entries — its body is the JMAP
// error envelope, not a /set response.
func TestGatherCreations_SkipsErrorEntries(t *testing.T) {
	prior := []Invocation{
		{
			Name:   "error",
			Args:   json.RawMessage(`{"type":"unknownMethod"}`),
			CallID: "c0",
		},
	}
	got := gatherCreations(prior, nil)
	if len(got) != 0 {
		t.Errorf("error invocations must contribute no creations, got %v", got)
	}
}

// TestEvalJSONPointer_WildcardFlattening verifies RFC 8620 §3.7.1 one-level
// flattening: when a wildcard sub-evaluation returns an array (e.g. the
// emailIds field of a Thread object), its elements are inserted flat into the
// result instead of being nested. This is the behaviour required for a chained
// Email/get whose #ids references Thread/get's /list/*/emailIds (re #88).
func TestEvalJSONPointer_WildcardFlattening(t *testing.T) {
	// Two Thread objects each with two emailIds.
	doc := json.RawMessage(`[
		{"id":"t1","emailIds":["e1","e2"]},
		{"id":"t2","emailIds":["e3","e4"]}
	]`)

	got, err := evalJSONPointer(doc, "/*/emailIds")
	if err != nil {
		t.Fatalf("evalJSONPointer: %v", err)
	}
	// Flattened: all four IDs in a single array, not [[e1,e2],[e3,e4]].
	want := json.RawMessage(`["e1","e2","e3","e4"]`)
	if !equalJSON(t, got, want) {
		t.Errorf("got %s, want %s", string(got), string(want))
	}
}

// TestEvalJSONPointer_WildcardScalar confirms that the existing scalar case
// is unaffected: /list/*/id (where id is a string, not an array) still
// produces a flat array of strings without double-flattening.
func TestEvalJSONPointer_WildcardScalar(t *testing.T) {
	doc := json.RawMessage(`[{"id":"t1"},{"id":"t2"}]`)
	got, err := evalJSONPointer(doc, "/*/id")
	if err != nil {
		t.Fatalf("evalJSONPointer: %v", err)
	}
	want := json.RawMessage(`["t1","t2"]`)
	if !equalJSON(t, got, want) {
		t.Errorf("got %s, want %s", string(got), string(want))
	}
}

func equalJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v (%s)", err, string(a))
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v (%s)", err, string(b))
	}
	return reflect.DeepEqual(av, bv)
}
