package gmail

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GmailFilter is one entry in the takeout Filter.json file. JSON keys
// are stable across all Gmail UI locales (the file format is locale-
// independent — only the values inside labelsToAdd / labelsToRemove
// are localized to the user's UI language).
type GmailFilter struct {
	// Query is the Gmail search expression that selects matching mail
	// (see https://support.google.com/mail/answer/7190 for syntax).
	Query string `json:"query"`
	// LabelsToAdd is the list of labels (system or user) Gmail attaches
	// when the query matches. System-label values are localized; user
	// labels pass through verbatim.
	LabelsToAdd []string `json:"labelsToAdd,omitempty"`
	// LabelsToRemove is the list of labels Gmail removes on match. The
	// only commonly-seen value is the localized Inbox label
	// (`labelsToRemove: ["Posteingang"]`), which Gmail uses to express
	// "skip the inbox".
	LabelsToRemove []string `json:"labelsToRemove,omitempty"`
	// ForwardTo names a destination address for filter-driven
	// forwarding rules; rare but emitted when set.
	ForwardTo string `json:"forwardTo,omitempty"`
	// ShouldArchive / ShouldTrash / ShouldMarkAsRead / ShouldStar are
	// Gmail's secondary filter actions; we surface them but the v1
	// translator handles only labels.
	ShouldArchive    bool `json:"shouldArchive,omitempty"`
	ShouldTrash      bool `json:"shouldTrash,omitempty"`
	ShouldMarkAsRead bool `json:"shouldMarkAsRead,omitempty"`
	ShouldStar       bool `json:"shouldStar,omitempty"`
}

// FiltersFile is the top-level shape of Filter.json. Gmail emits the
// file with a single top-level array under "filter" (singular).
type FiltersFile struct {
	Filter []GmailFilter `json:"filter"`
}

// AddressList is the shape used by Blockierte Adressen.json,
// Weiterleitungsadressen.json, and Delegierte Absenderadressen.json.
// All three are a JSON object with a single "addresses" array.
type AddressList struct {
	Addresses []string `json:"addresses"`
}

// Settings is the parsed bundle of all four settings files. Any field
// may be nil if the corresponding file was absent or unidentifiable
// (REQ-IMPORT-08: settings are optional). The importer surfaces the
// counts in the per-job summary regardless of which files were found.
type Settings struct {
	// Filters is parsed from Filter.json; nil when the file is absent.
	Filters []GmailFilter
	// BlockedAddresses is parsed from the locale-named blocked-addresses
	// file (German: "Blockierte Adressen.json"; English: "Blocked
	// Addresses.json"). Detected by content shape, not name.
	BlockedAddresses []string
	// ForwardingAddresses is parsed from the forwarding-addresses file.
	ForwardingAddresses []string
	// DelegatedAddresses is parsed from the delegated-send-as file.
	DelegatedAddresses []string
}

// ClassifyJSON inspects the parsed JSON shape of a settings file and
// returns its kind. Used by the importer to identify settings files
// without depending on locale-dependent filenames (REQ-IMPORT-16).
type SettingsKind int

const (
	// SettingsKindUnknown means the file could not be identified.
	SettingsKindUnknown SettingsKind = iota
	// SettingsKindFilters is a Filter.json equivalent.
	SettingsKindFilters
	// SettingsKindAddressList is a {"addresses": [...]} file. The
	// caller must distinguish blocked / forwarding / delegated by some
	// other signal (filename hint, position in the archive). Three
	// disambiguation hints are exported as best-effort matchers.
	SettingsKindAddressList
)

// ClassifyJSON identifies a settings file from its raw JSON content.
// The decision is based on the top-level keys; locale plays no part.
//
// Returns the kind plus one of two parsed shapes (FiltersFile or
// AddressList) wrapped in a Settings-friendly interface. Callers use
// the kind to route into the right Settings field.
func ClassifyJSON(raw []byte) (SettingsKind, *FiltersFile, *AddressList, error) {
	// First, look at the top-level keys to decide which struct to use.
	// json.Unmarshal into a map[string]json.RawMessage is the cheapest
	// way to peek at the keys without committing to a schema.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return SettingsKindUnknown, nil, nil, fmt.Errorf("settings: %w", err)
	}
	if _, ok := probe["filter"]; ok {
		var f FiltersFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return SettingsKindUnknown, nil, nil, fmt.Errorf("settings: filter: %w", err)
		}
		return SettingsKindFilters, &f, nil, nil
	}
	if _, ok := probe["addresses"]; ok {
		var a AddressList
		if err := json.Unmarshal(raw, &a); err != nil {
			return SettingsKindUnknown, nil, nil, fmt.Errorf("settings: addresses: %w", err)
		}
		return SettingsKindAddressList, nil, &a, nil
	}
	return SettingsKindUnknown, nil, nil, errors.New("settings: unrecognised JSON shape")
}

// AddressListHint is a best-effort filename heuristic that disambiguates
// between blocked / forwarding / delegated when only the JSON content
// is available. Returns one of "blocked", "forwarding", "delegated" or
// the empty string when nothing matches.
//
// Exported for testability; the importer calls this on the source
// filename. The matcher is intentionally generous — it accepts
// English plus the locales we ship system-label tables for.
func AddressListHint(filename string) string {
	f := strings.ToLower(filename)
	switch {
	case strings.Contains(f, "blocked"),
		strings.Contains(f, "blockiert"),
		strings.Contains(f, "bloqu"),
		strings.Contains(f, "bloque"),
		strings.Contains(f, "blocco"),
		strings.Contains(f, "geblokk"),
		strings.Contains(f, "ブロック"):
		return "blocked"
	case strings.Contains(f, "forward"),
		strings.Contains(f, "weiterleit"),
		strings.Contains(f, "transf"),
		strings.Contains(f, "reenv"),
		strings.Contains(f, "inoltr"),
		strings.Contains(f, "doorstu"),
		strings.Contains(f, "encam"),
		strings.Contains(f, "転送"):
		return "forwarding"
	case strings.Contains(f, "delegate"),
		strings.Contains(f, "delegier"),
		strings.Contains(f, "déléguée"),
		strings.Contains(f, "deleguee"),
		strings.Contains(f, "delega"),
		strings.Contains(f, "委任"):
		return "delegated"
	default:
		return ""
	}
}
