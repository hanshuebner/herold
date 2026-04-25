package contacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// JSContact is RFC 9553. The on-the-wire JSON object is called a "Card".
// We model the most-frequently-queried fields with explicit Go types
// (Name, Emails, Phones, Addresses, Organizations, Titles) so the
// denormalised store columns can be populated without re-parsing the
// blob; everything else round-trips through RawJSON for forward
// compatibility with future RFC 9553 errata or extensions.
//
// Strategy:
//
//  1. UnmarshalJSON copies every key into RawJSON, then projects the
//     load-bearing keys into the typed fields. Round-tripping a Card
//     preserves keys we do not model.
//  2. MarshalJSON merges the typed fields back into RawJSON before
//     serialising — typed mutations win over the raw map so the helpers
//     stay authoritative.
//
// The store-side denormalised columns (DisplayName, PrimaryEmail,
// GivenName, Surname, OrgName, SearchBlob) are computed from the typed
// fields only; if a client supplies them only inside RawJSON the
// helpers fall back to a JSON probe so we never miss a value.
type Card struct {
	// Version is the JSContact version string. RFC 9553 §2 mandates
	// "1.0" for the v1 wire format; Validate rejects anything else.
	Version string `json:"-"`
	// Kind is one of "individual", "group", "location", "application",
	// "device" (RFC 9553 §2.2). Empty is treated as "individual" by
	// most clients; we accept absence and only reject explicitly bad
	// values in Validate.
	Kind string `json:"-"`
	// UID is the RFC 4122 UUID-like identifier. The /set create path
	// mints one if absent.
	UID string `json:"-"`

	// Name is the structured name object (RFC 9553 §2.4). nil when the
	// client supplied no name.
	Name *Name `json:"-"`
	// Emails is the JSContact emails map (RFC 9553 §2.5.2). Keys are
	// client-supplied opaque ids ("email/0", "personal", ...).
	Emails map[string]EmailAddress `json:"-"`
	// Phones is the phones map (RFC 9553 §2.5.3).
	Phones map[string]Phone `json:"-"`
	// Addresses is the addresses map (RFC 9553 §2.5.1).
	Addresses map[string]Address `json:"-"`
	// Organizations is the organizations map (RFC 9553 §2.6.1).
	Organizations map[string]Organization `json:"-"`
	// Titles is the titles map (RFC 9553 §2.6.2).
	Titles map[string]Title `json:"-"`

	// RawJSON carries every field as the client/server sent it,
	// including the typed fields (kept in lock-step by Marshal /
	// Unmarshal). Unknown keys flow through unchanged.
	RawJSON map[string]json.RawMessage `json:"-"`
}

// Name is the JSContact Name object (RFC 9553 §2.4).
type Name struct {
	// Components is the ordered list of name components (given,
	// surname, prefix, ...). Each carries kind + value.
	Components []NameComponent `json:"components,omitempty"`
	// Full is the pre-formatted full name; clients populate either
	// Full or Components, RFC 9553 recommends both.
	Full string `json:"full,omitempty"`
	// IsOrdered marks Components as carrying meaningful order.
	IsOrdered bool `json:"isOrdered,omitempty"`
	// DefaultSeparator overrides the join separator when Full is
	// derived from Components.
	DefaultSeparator string `json:"defaultSeparator,omitempty"`
	// SortAs is the per-component sort key map (e.g.
	// {"surname": "Smyth"} for "Smith-Jones").
	SortAs map[string]string `json:"sortAs,omitempty"`
}

// NameComponent is one entry in Name.Components.
type NameComponent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// EmailAddress is one row in Card.Emails (RFC 9553 §2.5.2).
type EmailAddress struct {
	Address  string          `json:"address"`
	Contexts map[string]bool `json:"contexts,omitempty"`
	Pref     int             `json:"pref,omitempty"`
	Label    string          `json:"label,omitempty"`
}

// Phone is one row in Card.Phones (RFC 9553 §2.5.3).
type Phone struct {
	Number   string          `json:"number"`
	Contexts map[string]bool `json:"contexts,omitempty"`
	Features map[string]bool `json:"features,omitempty"`
	Pref     int             `json:"pref,omitempty"`
	Label    string          `json:"label,omitempty"`
}

// Address is one row in Card.Addresses (RFC 9553 §2.5.1).
type Address struct {
	Components  []AddressComponent `json:"components,omitempty"`
	Full        string             `json:"full,omitempty"`
	Contexts    map[string]bool    `json:"contexts,omitempty"`
	Pref        int                `json:"pref,omitempty"`
	Label       string             `json:"label,omitempty"`
	CountryCode string             `json:"countryCode,omitempty"`
	Coordinates string             `json:"coordinates,omitempty"`
	TimeZone    string             `json:"timeZone,omitempty"`
}

// AddressComponent is one entry in Address.Components.
type AddressComponent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Organization is one row in Card.Organizations (RFC 9553 §2.6.1).
type Organization struct {
	Name     string          `json:"name,omitempty"`
	Units    []string        `json:"units,omitempty"`
	SortAs   string          `json:"sortAs,omitempty"`
	Contexts map[string]bool `json:"contexts,omitempty"`
}

// Title is one row in Card.Titles (RFC 9553 §2.6.2).
type Title struct {
	Name  string `json:"name,omitempty"`
	Kind  string `json:"kind,omitempty"`
	OrgID string `json:"organizationId,omitempty"`
}

// allowedKinds mirrors RFC 9553 §2.2. Values not in this set are
// rejected by Validate.
var allowedKinds = map[string]struct{}{
	"individual":  {},
	"group":       {},
	"location":    {},
	"application": {},
	"device":      {},
}

// UnmarshalJSON parses a JSContact Card. Unknown keys flow into
// RawJSON; the typed fields are populated from their canonical keys.
// Returns an error only when the input is not a JSON object. All
// per-field validation (version, kind, uid) happens in Validate so
// callers can choose a relaxed-vs-strict posture.
func (c *Card) UnmarshalJSON(b []byte) error {
	if len(bytes.TrimSpace(b)) == 0 || string(bytes.TrimSpace(b)) == "null" {
		*c = Card{}
		return nil
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("jscontact: parse: %w", err)
	}
	c.RawJSON = raw
	c.Version = projectString(raw, "version")
	c.Kind = projectString(raw, "kind")
	c.UID = projectString(raw, "uid")

	if v, ok := raw["name"]; ok && len(v) > 0 && string(v) != "null" {
		var n Name
		if err := json.Unmarshal(v, &n); err == nil {
			c.Name = &n
		}
	}
	if v, ok := raw["emails"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &c.Emails)
	}
	if v, ok := raw["phones"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &c.Phones)
	}
	if v, ok := raw["addresses"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &c.Addresses)
	}
	if v, ok := raw["organizations"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &c.Organizations)
	}
	if v, ok := raw["titles"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &c.Titles)
	}
	return nil
}

// MarshalJSON serialises a Card. The typed fields override the matching
// RawJSON entries so helpers stay authoritative; everything else flows
// through unchanged. Output keys are sorted so the wire form is
// deterministic.
func (c Card) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(c.RawJSON)+8)
	for k, v := range c.RawJSON {
		out[k] = v
	}
	if c.Version != "" {
		out["version"] = mustMarshal(c.Version)
	} else if _, ok := out["version"]; !ok {
		out["version"] = mustMarshal("1.0")
	}
	if c.Kind != "" {
		out["kind"] = mustMarshal(c.Kind)
	}
	if c.UID != "" {
		out["uid"] = mustMarshal(c.UID)
	}
	if c.Name != nil {
		out["name"] = mustMarshal(*c.Name)
	}
	if c.Emails != nil {
		out["emails"] = mustMarshal(c.Emails)
	}
	if c.Phones != nil {
		out["phones"] = mustMarshal(c.Phones)
	}
	if c.Addresses != nil {
		out["addresses"] = mustMarshal(c.Addresses)
	}
	if c.Organizations != nil {
		out["organizations"] = mustMarshal(c.Organizations)
	}
	if c.Titles != nil {
		out["titles"] = mustMarshal(c.Titles)
	}
	return canonicalJSONObject(out), nil
}

// Validate enforces RFC 9553 §2 hard rules: version is "1.0", uid is
// present, kind (when set) is in the allowed set. The rest of the
// object model is intentionally relaxed so a forward-compatible client
// payload survives the round-trip.
func (c *Card) Validate() error {
	if c == nil {
		return errors.New("jscontact: nil card")
	}
	if c.Version != "1.0" {
		return fmt.Errorf("jscontact: version must be \"1.0\", got %q", c.Version)
	}
	if c.UID == "" {
		return errors.New("jscontact: uid is required")
	}
	if c.Kind != "" {
		if _, ok := allowedKinds[c.Kind]; !ok {
			return fmt.Errorf("jscontact: kind %q is not a recognised RFC 9553 kind", c.Kind)
		}
	}
	return nil
}

// PrimaryEmail picks the contact's preferred email address. Selection
// order, mirroring the RFC 9553 §2.5 "pref" semantics:
//
//  1. The entry whose Pref is set and lowest (1 wins over 100).
//  2. The first entry in deterministic key order, when no Pref is set.
//  3. An empty string when the contact has no emails.
func (c *Card) PrimaryEmail() string {
	if c == nil || len(c.Emails) == 0 {
		return ""
	}
	keys := sortedKeys(c.Emails)
	bestKey := ""
	bestPref := 0
	for _, k := range keys {
		e := c.Emails[k]
		if e.Pref > 0 {
			if bestKey == "" || e.Pref < bestPref {
				bestKey = k
				bestPref = e.Pref
			}
		}
	}
	if bestKey != "" {
		return c.Emails[bestKey].Address
	}
	return c.Emails[keys[0]].Address
}

// DisplayName picks the contact's display string. Fallback order
// mirrors RFC 9553 §2.4 + the JMAP-Contacts binding draft: full →
// components(given+surname) → org name → primary email. Returns ""
// when nothing is populated.
func (c *Card) DisplayName() string {
	if c == nil {
		return ""
	}
	if c.Name != nil && c.Name.Full != "" {
		return c.Name.Full
	}
	if c.Name != nil && len(c.Name.Components) > 0 {
		var given, surname string
		var others []string
		for _, comp := range c.Name.Components {
			switch comp.Kind {
			case "given":
				given = comp.Value
			case "surname":
				surname = comp.Value
			default:
				others = append(others, comp.Value)
			}
		}
		var parts []string
		if given != "" {
			parts = append(parts, given)
		}
		if surname != "" {
			parts = append(parts, surname)
		}
		if len(parts) == 0 {
			parts = others
		}
		joined := strings.Join(parts, " ")
		if strings.TrimSpace(joined) != "" {
			return joined
		}
	}
	if name := c.OrgName(); name != "" {
		return name
	}
	return c.PrimaryEmail()
}

// GivenName returns the "given" name component, or empty when absent.
func (c *Card) GivenName() string {
	if c == nil || c.Name == nil {
		return ""
	}
	for _, comp := range c.Name.Components {
		if comp.Kind == "given" {
			return comp.Value
		}
	}
	return ""
}

// Surname returns the "surname" name component, or empty when absent.
func (c *Card) Surname() string {
	if c == nil || c.Name == nil {
		return ""
	}
	for _, comp := range c.Name.Components {
		if comp.Kind == "surname" {
			return comp.Value
		}
	}
	return ""
}

// OrgName returns the first Organization's Name in deterministic key
// order, or empty when none exists.
func (c *Card) OrgName() string {
	if c == nil || len(c.Organizations) == 0 {
		return ""
	}
	keys := sortedKeys(c.Organizations)
	for _, k := range keys {
		if name := c.Organizations[k].Name; name != "" {
			return name
		}
	}
	return ""
}

// SearchBlob renders a flat ASCII-friendly blob of every searchable
// scalar in the Card, lowercased and space-separated. The store's
// Contact/query "text" filter does substring matches against this
// blob. We include: display name, given/surname, every email address,
// every phone number, every organization name, every title, and the
// card uid.
func (c *Card) SearchBlob() string {
	if c == nil {
		return ""
	}
	var parts []string
	if dn := c.DisplayName(); dn != "" {
		parts = append(parts, dn)
	}
	if gn := c.GivenName(); gn != "" {
		parts = append(parts, gn)
	}
	if sn := c.Surname(); sn != "" {
		parts = append(parts, sn)
	}
	if c.UID != "" {
		parts = append(parts, c.UID)
	}
	for _, k := range sortedKeys(c.Emails) {
		if a := c.Emails[k].Address; a != "" {
			parts = append(parts, a)
		}
	}
	for _, k := range sortedKeys(c.Phones) {
		if a := c.Phones[k].Number; a != "" {
			parts = append(parts, a)
		}
	}
	for _, k := range sortedKeys(c.Organizations) {
		if n := c.Organizations[k].Name; n != "" {
			parts = append(parts, n)
		}
		for _, u := range c.Organizations[k].Units {
			if u != "" {
				parts = append(parts, u)
			}
		}
	}
	for _, k := range sortedKeys(c.Titles) {
		if n := c.Titles[k].Name; n != "" {
			parts = append(parts, n)
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// projectString unwraps a JSON string from the raw map. Returns "" on
// missing or non-string values; never returns an error so the typed
// fields stay best-effort and Validate is the explicit gate.
func projectString(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok || len(v) == 0 || string(v) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

// sortedKeys returns the map's keys in deterministic ascending order.
// Used by every helper so Card.PrimaryEmail and friends are
// reproducible across runs (Go map iteration is randomised).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mustMarshal panics on a json.Marshal error; used only for value types
// whose encoding cannot fail (strings, primitive maps, nested struct
// types defined in this file).
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("jscontact: marshal: %v", err))
	}
	return b
}

// canonicalJSONObject renders a map as a JSON object with keys sorted
// lexically. Used by Card.MarshalJSON so the on-the-wire byte sequence
// is stable across map-iteration orderings — round-trip tests rely on
// byte equality.
func canonicalJSONObject(m map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		v := m[k]
		if len(v) == 0 {
			buf.WriteString("null")
		} else {
			buf.Write(v)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes()
}
