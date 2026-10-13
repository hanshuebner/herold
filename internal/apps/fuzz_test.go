package apps

import (
	"errors"
	"testing"
)

// FuzzParseManifest exercises the manifest parser (REQ-APP-20..22) on
// arbitrary input. It must never panic; an accepted manifest must
// round-trip through its canonical form to the same digest, and every
// accepted extension must satisfy the v1 constraints.
func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(specExample))
	f.Add([]byte(`{"herold_app":1,"app_key":"a.b.c","name":{"en":"N"},"extensions":[]}`))
	f.Add([]byte(`{"herold_app":2}`))
	f.Add([]byte(`{"herold_app":1,"app_key":"a.b.c","name":{"en":"N"},"extensions":[{"point":"message.action"}]}`))
	f.Add([]byte(`{"herold_app":1,"extensions":[{"endpoint":"//x/../y?q#f","scopes":["thread.raw","x"]}]}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, _, err := ParseManifest(data)
		if err != nil {
			var me *ManifestError
			if !errors.As(err, &me) || len(me.Problems) == 0 {
				t.Fatalf("error %v is not a *ManifestError with problems", err)
			}
			return
		}
		canon, digest := m.Canonical()
		back, _, err := ParseManifest(canon)
		if err != nil {
			t.Fatalf("canonical form %s rejected: %v", canon, err)
		}
		if _, d2 := back.Canonical(); d2 != digest {
			t.Fatalf("canonical form not a fixed point: %s", canon)
		}
		for _, e := range m.Extensions {
			if e.Point != PointThreadAction || validateEndpoint(e.Endpoint) != nil || e.Label["en"] == "" {
				t.Fatalf("accepted invalid extension %+v", e)
			}
			for _, s := range e.Scopes {
				if !knownScopes[s] {
					t.Fatalf("accepted unknown scope %q", s)
				}
			}
		}
	})
}
