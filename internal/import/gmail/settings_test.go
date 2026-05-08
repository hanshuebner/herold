package gmail

import "testing"

func TestClassifyFilters(t *testing.T) {
	raw := []byte(`{"filter":[{"query":"subject:foo","labelsToAdd":["bar"]}]}`)
	kind, f, a, err := ClassifyJSON(raw)
	if err != nil {
		t.Fatalf("ClassifyJSON: %v", err)
	}
	if kind != SettingsKindFilters {
		t.Fatalf("kind = %v, want Filters", kind)
	}
	if a != nil {
		t.Errorf("AddressList should be nil for filters")
	}
	if len(f.Filter) != 1 || f.Filter[0].Query != "subject:foo" {
		t.Errorf("Filter parse mismatch: %+v", f)
	}
}

func TestClassifyAddressList(t *testing.T) {
	raw := []byte(`{"addresses":["a@b.com","c@d.com"]}`)
	kind, f, a, err := ClassifyJSON(raw)
	if err != nil {
		t.Fatalf("ClassifyJSON: %v", err)
	}
	if kind != SettingsKindAddressList {
		t.Fatalf("kind = %v, want AddressList", kind)
	}
	if f != nil {
		t.Errorf("Filters should be nil for addresses")
	}
	if len(a.Addresses) != 2 {
		t.Errorf("Addresses parse mismatch: %+v", a)
	}
}

func TestClassifyUnknownShape(t *testing.T) {
	raw := []byte(`{"unrecognised":true}`)
	kind, _, _, err := ClassifyJSON(raw)
	if err == nil {
		t.Fatalf("want error, got nil (kind=%v)", kind)
	}
	if kind != SettingsKindUnknown {
		t.Errorf("kind = %v, want Unknown", kind)
	}
}

func TestAddressListHint(t *testing.T) {
	cases := map[string]string{
		"Blocked Addresses.json":           "blocked",
		"Blockierte Adressen.json":         "blocked",
		"Forwarding Addresses.json":        "forwarding",
		"Weiterleitungsadressen.json":      "forwarding",
		"Delegated Sender Addresses.json":  "delegated",
		"Delegierte Absenderadressen.json": "delegated",
		"Adresses transférées.json":        "forwarding",
		"Filter.json":                      "",
	}
	for name, want := range cases {
		if got := AddressListHint(name); got != want {
			t.Errorf("AddressListHint(%q) = %q, want %q", name, got, want)
		}
	}
}
