package apps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ManifestAPIVersion is the herold_app manifest version this server
// implements (REQ-APP-21, REQ-APP-23).
const ManifestAPIVersion = 1

// MaxManifestBytes bounds a fetched manifest body (REQ-APP-20).
const MaxManifestBytes = 64 * 1024

// Extension points and scopes defined in v1 (REQ-APP-22).
const (
	PointThreadAction = "thread.action"
	ScopeThreadRaw    = "thread.raw"
)

// DefaultIcon is the icon a manifest extension gets when it names none.
const DefaultIcon = "app"

// Manifest field bounds (REQ-APP-21).
const (
	maxNameRunes      = 60
	maxLabelRunes     = 40
	maxEndpointBytes  = 512
	maxExtensions     = 50
	maxLanguages      = 50
	maxScopesPerExt   = 16
	maxIconBytes      = 40
	maxLanguageTagLen = 35
)

var (
	appKeyRE      = regexp.MustCompile(`^[a-z0-9.-]{3,100}$`)
	extensionIDRE = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	iconRE        = regexp.MustCompile(`^[a-z0-9-]*$`)
	languageTagRE = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)
)

var knownPoints = map[string]bool{PointThreadAction: true}

var knownScopes = map[string]bool{ScopeThreadRaw: true}

// LocalizedText maps a BCP 47 language tag to a string. encoding/json
// marshals it with sorted keys, which keeps the canonical form stable.
type LocalizedText map[string]string

// Extension is one validated manifest extension (REQ-APP-21).
type Extension struct {
	Point    string        `json:"point"`
	ID       string        `json:"id"`
	Label    LocalizedText `json:"label"`
	Icon     string        `json:"icon"`
	Scopes   []string      `json:"scopes"`
	Endpoint string        `json:"endpoint"`
}

// HasScope reports whether the extension was granted scope.
func (e Extension) HasScope(scope string) bool {
	for _, s := range e.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Manifest is a validated v1 manifest: unknown fields are gone,
// extensions with unknown points are dropped, and every remaining field
// satisfies REQ-APP-21.
type Manifest struct {
	HeroldApp  int           `json:"herold_app"`
	AppKey     string        `json:"app_key"`
	Name       LocalizedText `json:"name"`
	Extensions []Extension   `json:"extensions"`
}

// Extension returns the extension with the given id.
func (m *Manifest) Extension(id string) (Extension, bool) {
	for _, e := range m.Extensions {
		if e.ID == id {
			return e, true
		}
	}
	return Extension{}, false
}

// Canonical returns the canonical JSON form of m and its hex SHA-256
// digest (REQ-APP-01). Approval binds to this digest.
func (m *Manifest) Canonical() ([]byte, string) {
	b, err := json.Marshal(m)
	if err != nil {
		// Manifest holds only strings, ints, slices and string maps.
		panic("apps: marshal manifest: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:])
}

// ManifestError reports why a manifest was rejected. Problems lists
// every violation found, each prefixed with the JSON path it concerns.
type ManifestError struct {
	Problems []string
}

func (e *ManifestError) Error() string {
	return "apps: invalid manifest: " + strings.Join(e.Problems, "; ")
}

// rawManifest is the decode shape: every field optional so validation
// reports each missing one instead of the decoder stopping at the first.
type rawManifest struct {
	HeroldApp  *int              `json:"herold_app"`
	AppKey     *string           `json:"app_key"`
	Name       map[string]string `json:"name"`
	Extensions []json.RawMessage `json:"extensions"`
}

type rawExtension struct {
	Point    *string           `json:"point"`
	ID       *string           `json:"id"`
	Label    map[string]string `json:"label"`
	Icon     *string           `json:"icon"`
	Scopes   []string          `json:"scopes"`
	Endpoint *string           `json:"endpoint"`
}

// ParseManifest decodes and validates a v1 manifest (REQ-APP-20..22).
// It returns the validated manifest and the warnings for extensions it
// dropped (unknown point), or a *ManifestError listing every violation.
// Unknown top-level and extension fields are ignored.
func ParseManifest(data []byte) (*Manifest, []string, error) {
	if len(data) > MaxManifestBytes {
		return nil, nil, &ManifestError{Problems: []string{
			fmt.Sprintf("manifest exceeds %d bytes", MaxManifestBytes)}}
	}
	if !utf8.Valid(data) {
		return nil, nil, &ManifestError{Problems: []string{"manifest is not valid UTF-8"}}
	}
	var raw rawManifest
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return nil, nil, &ManifestError{Problems: []string{"manifest is not a JSON object: " + err.Error()}}
	}
	if dec.More() {
		return nil, nil, &ManifestError{Problems: []string{"trailing data after the manifest object"}}
	}

	var problems, warnings []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	m := &Manifest{HeroldApp: ManifestAPIVersion}
	switch {
	case raw.HeroldApp == nil:
		add("herold_app: required")
	case *raw.HeroldApp != ManifestAPIVersion:
		// A version this server does not implement: nothing else in the
		// document can be interpreted.
		return nil, nil, &ManifestError{Problems: []string{
			fmt.Sprintf("herold_app: version %d is not supported (this server implements %d)",
				*raw.HeroldApp, ManifestAPIVersion)}}
	}
	if raw.AppKey == nil {
		add("app_key: required")
	} else if !appKeyRE.MatchString(*raw.AppKey) {
		add("app_key: must match [a-z0-9.-]{3,100}")
	} else {
		m.AppKey = *raw.AppKey
	}
	m.Name = validateLocalized("name", raw.Name, maxNameRunes, add)

	if len(raw.Extensions) > maxExtensions {
		add("extensions: at most %d entries", maxExtensions)
		raw.Extensions = raw.Extensions[:maxExtensions]
	}
	m.Extensions = []Extension{}
	seen := make(map[string]bool)
	for i, rawExt := range raw.Extensions {
		path := fmt.Sprintf("extensions[%d]", i)
		var re rawExtension
		if err := json.Unmarshal(rawExt, &re); err != nil {
			add("%s: not an extension object: %s", path, err)
			continue
		}
		if re.Point == nil || *re.Point == "" {
			add("%s.point: required", path)
			continue
		}
		if !knownPoints[*re.Point] {
			warnings = append(warnings, fmt.Sprintf("%s: unknown extension point %q dropped", path, *re.Point))
			continue
		}
		ext := Extension{Point: *re.Point}
		if re.ID == nil || !extensionIDRE.MatchString(*re.ID) {
			add("%s.id: required, must match [a-z0-9-]{1,40}", path)
		} else if seen[*re.ID] {
			add("%s.id: duplicate id %q", path, *re.ID)
		} else {
			seen[*re.ID] = true
			ext.ID = *re.ID
		}
		ext.Label = validateLocalized(path+".label", re.Label, maxLabelRunes, add)
		ext.Icon = DefaultIcon
		if re.Icon != nil && *re.Icon != "" {
			if len(*re.Icon) > maxIconBytes || !iconRE.MatchString(*re.Icon) {
				add("%s.icon: must match [a-z0-9-]{1,40}", path)
			} else {
				ext.Icon = *re.Icon
			}
		}
		ext.Scopes = validateScopes(path, re.Scopes, add)
		if re.Endpoint == nil {
			add("%s.endpoint: required", path)
		} else if err := validateEndpoint(*re.Endpoint); err != nil {
			add("%s.endpoint: %s", path, err)
		} else {
			ext.Endpoint = *re.Endpoint
		}
		m.Extensions = append(m.Extensions, ext)
	}
	if len(problems) > 0 {
		return nil, warnings, &ManifestError{Problems: problems}
	}
	return m, warnings, nil
}

func validateLocalized(path string, in map[string]string, maxRunes int, add func(string, ...any)) LocalizedText {
	if in == nil {
		add("%s: required", path)
		return nil
	}
	if len(in) > maxLanguages {
		add("%s: at most %d languages", path, maxLanguages)
		return nil
	}
	out := make(LocalizedText, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := in[k]
		if len(k) > maxLanguageTagLen || !languageTagRE.MatchString(k) {
			add("%s: %q is not a BCP 47 language tag", path, k)
			continue
		}
		if err := validateText(v, maxRunes); err != nil {
			add("%s.%s: %s", path, k, err)
			continue
		}
		out[k] = v
	}
	if _, ok := in["en"]; !ok {
		add("%s.en: required", path)
	}
	return out
}

func validateText(s string, maxRunes int) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("must not be empty")
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return fmt.Errorf("at most %d characters", maxRunes)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

func validateScopes(path string, in []string, add func(string, ...any)) []string {
	if len(in) > maxScopesPerExt {
		add("%s.scopes: at most %d entries", path, maxScopesPerExt)
		return []string{}
	}
	out := []string{}
	seen := make(map[string]bool, len(in))
	for _, sc := range in {
		if !knownScopes[sc] {
			// REQ-APP-22: a grant the server cannot explain rejects the
			// whole manifest.
			add("%s.scopes: unknown scope %q", path, sc)
			continue
		}
		if seen[sc] {
			continue
		}
		seen[sc] = true
		out = append(out, sc)
	}
	sort.Strings(out)
	return out
}

// validateEndpoint enforces REQ-APP-21: an absolute path with no
// scheme, host, query or fragment, and no dot segments.
func validateEndpoint(ep string) error {
	if ep == "" || len(ep) > maxEndpointBytes {
		return fmt.Errorf("must be 1..%d bytes", maxEndpointBytes)
	}
	if !strings.HasPrefix(ep, "/") || strings.HasPrefix(ep, "//") {
		return errors.New("must be an absolute path starting with a single /")
	}
	for _, r := range ep {
		if r <= ' ' || r == 0x7f || r == '\\' {
			return errors.New("must not contain whitespace, control characters or backslashes")
		}
	}
	u, err := url.Parse(ep)
	if err != nil {
		return fmt.Errorf("not a valid path: %s", err)
	}
	if u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return errors.New("must not carry a scheme or host")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(ep, "?#") {
		return errors.New("must not carry a query or fragment")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return errors.New("must not contain . or .. segments")
		}
	}
	return nil
}

// ManifestDiff summarises what changed between an approved manifest
// and a pending one (REQ-APP-12): extensions and scopes added or
// removed, and changed endpoints.
type ManifestDiff struct {
	AddedExtensions   []string         `json:"added_extensions"`
	RemovedExtensions []string         `json:"removed_extensions"`
	AddedScopes       []ScopeChange    `json:"added_scopes"`
	RemovedScopes     []ScopeChange    `json:"removed_scopes"`
	ChangedEndpoints  []EndpointChange `json:"changed_endpoints"`
}

// ScopeChange names one scope added to or removed from one extension.
type ScopeChange struct {
	ExtensionID string `json:"extension_id"`
	Scope       string `json:"scope"`
}

// EndpointChange names one extension whose endpoint changed.
type EndpointChange struct {
	ExtensionID string `json:"extension_id"`
	From        string `json:"from"`
	To          string `json:"to"`
}

// DiffManifests compares the approved manifest from with the pending
// manifest to. A nil from (never approved) reports every extension of
// to as added.
func DiffManifests(from, to *Manifest) ManifestDiff {
	d := ManifestDiff{
		AddedExtensions:   []string{},
		RemovedExtensions: []string{},
		AddedScopes:       []ScopeChange{},
		RemovedScopes:     []ScopeChange{},
		ChangedEndpoints:  []EndpointChange{},
	}
	old := map[string]Extension{}
	if from != nil {
		for _, e := range from.Extensions {
			old[e.ID] = e
		}
	}
	cur := map[string]Extension{}
	if to != nil {
		for _, e := range to.Extensions {
			cur[e.ID] = e
		}
	}
	for _, e := range sortedExtensions(cur) {
		prev, ok := old[e.ID]
		if !ok {
			d.AddedExtensions = append(d.AddedExtensions, e.ID)
			for _, sc := range e.Scopes {
				d.AddedScopes = append(d.AddedScopes, ScopeChange{ExtensionID: e.ID, Scope: sc})
			}
			continue
		}
		for _, sc := range e.Scopes {
			if !prev.HasScope(sc) {
				d.AddedScopes = append(d.AddedScopes, ScopeChange{ExtensionID: e.ID, Scope: sc})
			}
		}
		for _, sc := range prev.Scopes {
			if !e.HasScope(sc) {
				d.RemovedScopes = append(d.RemovedScopes, ScopeChange{ExtensionID: e.ID, Scope: sc})
			}
		}
		if prev.Endpoint != e.Endpoint {
			d.ChangedEndpoints = append(d.ChangedEndpoints,
				EndpointChange{ExtensionID: e.ID, From: prev.Endpoint, To: e.Endpoint})
		}
	}
	for _, e := range sortedExtensions(old) {
		if _, ok := cur[e.ID]; !ok {
			d.RemovedExtensions = append(d.RemovedExtensions, e.ID)
			for _, sc := range e.Scopes {
				d.RemovedScopes = append(d.RemovedScopes, ScopeChange{ExtensionID: e.ID, Scope: sc})
			}
		}
	}
	return d
}

func sortedExtensions(m map[string]Extension) []Extension {
	out := make([]Extension, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DecodeStoredManifest decodes a canonical manifest previously produced
// by Canonical and stored in the database. It returns nil for an empty
// input.
func DecodeStoredManifest(b []byte) (*Manifest, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("apps: decode stored manifest: %w", err)
	}
	return &m, nil
}
