package apps

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// specExample is the REQ-APP-21 manifest example verbatim.
const specExample = `{
  "herold_app": 1,
  "app_key": "de.classic-computing.forum.donations",
  "name": {"en": "VzEkC donation offers", "de": "VzEkC-Spendenangebote"},
  "extensions": [
    {
      "point": "thread.action",
      "id": "donation-offer",
      "label": {"en": "Create donation offer", "de": "Spendenangebot erstellen"},
      "icon": "gift",
      "scopes": ["thread.raw"],
      "endpoint": "/vzekc-verlosung/herold/actions/donation-offer"
    }
  ]
}`

func mustParse(t *testing.T, doc string) (*Manifest, []string) {
	t.Helper()
	m, warnings, err := ParseManifest([]byte(doc))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return m, warnings
}

func problemsOf(t *testing.T, doc string) []string {
	t.Helper()
	_, _, err := ParseManifest([]byte(doc))
	var me *ManifestError
	if !errors.As(err, &me) {
		t.Fatalf("ParseManifest(%s) err = %v, want *ManifestError", doc, err)
	}
	return me.Problems
}

func TestParseManifest_SpecExample(t *testing.T) {
	m, warnings := mustParse(t, specExample)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if m.AppKey != "de.classic-computing.forum.donations" || m.Name["de"] != "VzEkC-Spendenangebote" {
		t.Fatalf("manifest = %+v", m)
	}
	e, ok := m.Extension("donation-offer")
	if !ok || e.Point != PointThreadAction || e.Icon != "gift" || !e.HasScope(ScopeThreadRaw) ||
		e.Endpoint != "/vzekc-verlosung/herold/actions/donation-offer" || e.Label["en"] != "Create donation offer" {
		t.Fatalf("extension = %+v", e)
	}
}

func TestParseManifest_CanonicalDigestStable(t *testing.T) {
	a, _ := mustParse(t, specExample)
	// Same content, different key order, whitespace and an unknown field.
	b, _ := mustParse(t, `{"extensions":[{"endpoint":"/vzekc-verlosung/herold/actions/donation-offer",
		"scopes":["thread.raw","thread.raw"],"icon":"gift","label":{"de":"Spendenangebot erstellen","en":"Create donation offer"},
		"id":"donation-offer","point":"thread.action","future":true}],
		"name":{"de":"VzEkC-Spendenangebote","en":"VzEkC donation offers"},
		"app_key":"de.classic-computing.forum.donations","herold_app":1,"homepage":"https://x"}`)
	ca, da := a.Canonical()
	cb, db := b.Canonical()
	if string(ca) != string(cb) || da != db {
		t.Fatalf("canonical forms differ:\n%s\n%s", ca, cb)
	}
	if len(da) != 64 {
		t.Fatalf("digest %q is not hex SHA-256", da)
	}
	back, err := DecodeStoredManifest(ca)
	if err != nil {
		t.Fatalf("DecodeStoredManifest: %v", err)
	}
	if _, d := back.Canonical(); d != da {
		t.Fatalf("stored manifest digest changed: %s vs %s", d, da)
	}
}

func TestParseManifest_UnknownPointDroppedWithWarning(t *testing.T) {
	m, warnings := mustParse(t, `{"herold_app":1,"app_key":"a.b.c","name":{"en":"N"},
		"extensions":[
		 {"point":"message.action","id":"x","label":{"en":"X"},"scopes":["message.raw"],"endpoint":"/x"},
		 {"point":"thread.action","id":"y","label":{"en":"Y"},"scopes":[],"endpoint":"/y"}]}`)
	if len(m.Extensions) != 1 || m.Extensions[0].ID != "y" || m.Extensions[0].Icon != DefaultIcon {
		t.Fatalf("extensions = %+v", m.Extensions)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "message.action") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestParseManifest_Rejections(t *testing.T) {
	base := func(ext string) string {
		return `{"herold_app":1,"app_key":"a.b.c","name":{"en":"N"},"extensions":[` + ext + `]}`
	}
	good := `{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":["thread.raw"],"endpoint":"/x"}`
	cases := map[string]struct {
		doc  string
		want string
	}{
		"not json":           {`nope`, "not a JSON object"},
		"trailing":           {base(good) + `{}`, "trailing"},
		"missing version":    {`{"app_key":"a.b.c","name":{"en":"N"},"extensions":[]}`, "herold_app: required"},
		"bad app key":        {`{"herold_app":1,"app_key":"A_B","name":{"en":"N"},"extensions":[]}`, "app_key"},
		"short app key":      {`{"herold_app":1,"app_key":"ab","name":{"en":"N"},"extensions":[]}`, "app_key"},
		"name without en":    {`{"herold_app":1,"app_key":"a.b.c","name":{"de":"N"},"extensions":[]}`, "name.en: required"},
		"name too long":      {`{"herold_app":1,"app_key":"a.b.c","name":{"en":"` + strings.Repeat("n", 61) + `"},"extensions":[]}`, "at most 60"},
		"bad language tag":   {`{"herold_app":1,"app_key":"a.b.c","name":{"en":"N","1x":"N"},"extensions":[]}`, "language tag"},
		"control char":       {`{"herold_app":1,"app_key":"a.b.c","name":{"en":"N\u0007"},"extensions":[]}`, "control"},
		"unknown scope":      {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":["thread.metadata"],"endpoint":"/x"}`), "unknown scope"},
		"label too long":     {base(`{"point":"thread.action","id":"x","label":{"en":"` + strings.Repeat("l", 41) + `"},"scopes":[],"endpoint":"/x"}`), "at most 40"},
		"bad id":             {base(`{"point":"thread.action","id":"X!","label":{"en":"X"},"scopes":[],"endpoint":"/x"}`), ".id"},
		"duplicate id":       {base(good + `,` + good), "duplicate id"},
		"endpoint with host": {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":[],"endpoint":"https://evil/x"}`), "endpoint"},
		"endpoint relative":  {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":[],"endpoint":"x"}`), "endpoint"},
		"endpoint authority": {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":[],"endpoint":"//evil/x"}`), "endpoint"},
		"endpoint query":     {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":[],"endpoint":"/x?a=1"}`), "query"},
		"endpoint fragment":  {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":[],"endpoint":"/x#f"}`), "query or fragment"},
		"endpoint dotdot":    {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":[],"endpoint":"/a/../b"}`), "segments"},
		"endpoint missing":   {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"scopes":[]}`), "endpoint: required"},
		"bad icon":           {base(`{"point":"thread.action","id":"x","label":{"en":"X"},"icon":"<svg>","scopes":[],"endpoint":"/x"}`), "icon"},
		"point missing":      {base(`{"id":"x","label":{"en":"X"},"scopes":[],"endpoint":"/x"}`), "point: required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probs := problemsOf(t, tc.doc)
			if !strings.Contains(strings.Join(probs, "\n"), tc.want) {
				t.Fatalf("problems %v do not mention %q", probs, tc.want)
			}
		})
	}
}

func TestParseManifest_UnsupportedVersion(t *testing.T) {
	probs := problemsOf(t, `{"herold_app":2,"whatever":true}`)
	if len(probs) != 1 || !strings.Contains(probs[0], "version 2 is not supported") {
		t.Fatalf("problems = %v", probs)
	}
}

func TestParseManifest_ReportsEveryProblem(t *testing.T) {
	probs := problemsOf(t, `{"herold_app":1,"app_key":"!","name":{},"extensions":[
		{"point":"thread.action","id":"","label":{},"scopes":["nope"],"endpoint":"x"}]}`)
	if len(probs) < 5 {
		t.Fatalf("expected one problem per violation, got %v", probs)
	}
}

func TestParseManifest_SizeLimit(t *testing.T) {
	big := `{"herold_app":1,"pad":"` + strings.Repeat("x", MaxManifestBytes) + `"}`
	probs := problemsOf(t, big)
	if !strings.Contains(probs[0], "exceeds") {
		t.Fatalf("problems = %v", probs)
	}
}

func TestDiffManifests(t *testing.T) {
	from, _ := mustParse(t, `{"herold_app":1,"app_key":"a.b.c","name":{"en":"N"},"extensions":[
		{"point":"thread.action","id":"keep","label":{"en":"K"},"scopes":["thread.raw"],"endpoint":"/old"},
		{"point":"thread.action","id":"gone","label":{"en":"G"},"scopes":["thread.raw"],"endpoint":"/g"}]}`)
	to, _ := mustParse(t, `{"herold_app":1,"app_key":"a.b.c","name":{"en":"N"},"extensions":[
		{"point":"thread.action","id":"keep","label":{"en":"K"},"scopes":[],"endpoint":"/new"},
		{"point":"thread.action","id":"added","label":{"en":"A"},"scopes":["thread.raw"],"endpoint":"/a"}]}`)
	d := DiffManifests(from, to)
	b, _ := json.Marshal(d)
	want := `{"added_extensions":["added"],"removed_extensions":["gone"],` +
		`"added_scopes":[{"extension_id":"added","scope":"thread.raw"}],` +
		`"removed_scopes":[{"extension_id":"keep","scope":"thread.raw"},{"extension_id":"gone","scope":"thread.raw"}],` +
		`"changed_endpoints":[{"extension_id":"keep","from":"/old","to":"/new"}]}`
	if string(b) != want {
		t.Fatalf("diff =\n%s\nwant\n%s", b, want)
	}
	first := DiffManifests(nil, from)
	if len(first.AddedExtensions) != 2 || len(first.RemovedExtensions) != 0 {
		t.Fatalf("diff from nil = %+v", first)
	}
}
