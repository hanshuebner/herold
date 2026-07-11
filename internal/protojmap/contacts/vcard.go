package contacts

// vCard 4.0 (RFC 6350) <-> JSContact (RFC 9553) converter.
//
// This file implements REQ-CTS-10..14 (vCard 4.0 converter):
//
//   - ParseVCards: vCard 4.0 bytes -> []ParseResult (REQ-CTS-10, REQ-CTS-11)
//   - GenerateVCard: Card -> vCard 4.0 bytes (REQ-CTS-12)
//
// Round-trip fidelity (REQ-CTS-13) is validated in vcard_test.go.
//
// Photo seam (REQ-CTS-14): PhotoInternFn (import) and PhotoFetchFn
// (export) are caller-supplied hooks. When either is nil the data is
// preserved as-is (data URI or opaque blobId reference) so no bytes are
// lost. Issue #171 wires the blob store; issue #172 wires the transport.
//
// Property mapping summary:
//
//	Typed Card fields:   Name, Emails, Phones, Addresses, Organizations,
//	                     Titles, Kind, UID, Version.
//	Named RawJSON keys:  nicknames, notes, links, anniversaries, media.
//	X-* pass-through:    stored as "vcard:x-..." RawJSON entries and
//	                     re-emitted by GenerateVCard.
//	Other vCard props:   accepted without error but not preserved (the
//	                     mappable-property-set contract in REQ-CTS-13
//	                     covers the properties listed in REQ-CTS-10).
//	Unrepresentable:     JSContact-native RawJSON keys that vCard cannot
//	                     express are returned as GenerateResult.Unrepresentable.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PhotoInternFn is the seam where the blob store plugs in during vCard
// import (REQ-CTS-14, issue #171). It is called with the detected media
// type and raw image bytes for each inline PHOTO encountered. The
// returned blobId is embedded in the Card media entry as a JMAP blob
// reference (REQ-CTS-01). When nil, the raw data URI is stored directly
// in the media entry so no photo data is lost; the import transport
// (issue #172) supplies a non-nil fn that interns blobs into the store.
type PhotoInternFn func(mediaType string, data []byte) (blobId string, err error)

// PhotoFetchFn is the seam where the blob store plugs in during vCard
// export (REQ-CTS-14, issue #171). It is called with the blobId from a
// Card media entry and returns raw bytes and media type for embedding as
// a data URI in the PHOTO property. When nil, blobId values are emitted
// as URI references (PHOTO:<blobId>) so no reference is lost; the export
// transport (issue #172) supplies a non-nil fn that resolves blobs.
type PhotoFetchFn func(blobId string) (mediaType string, data []byte, err error)

// UnrepresentableProperty names a JSContact property that has no
// faithful vCard 4.0 equivalent (REQ-CTS-12). Returned in
// GenerateResult.Unrepresentable rather than dropped silently.
type UnrepresentableProperty struct {
	// Type is the JSContact top-level key, e.g. "keywords", "pronouns".
	Type string
	// Detail summarises the value when a concise form is possible;
	// empty when the value is too large to summarise inline.
	Detail string
}

// ParseResult holds the outcome for one BEGIN:VCARD...END:VCARD block
// (REQ-CTS-11). Exactly one of Card or Error is non-nil.
type ParseResult struct {
	Card  *Card
	Error error
}

// GenerateResult holds the outcome of a Card -> vCard 4.0 conversion
// (REQ-CTS-12).
type GenerateResult struct {
	// VCard is the spec-conformant vCard 4.0 byte sequence with CRLF
	// line endings and RFC 6350 §3.2 line folding at 75 octets.
	VCard []byte
	// Unrepresentable lists JSContact properties omitted because vCard
	// 4.0 has no equivalent representation.
	Unrepresentable []UnrepresentableProperty
}

// ===== Public API =====

// ParseVCards parses one or more vCard 4.0 objects from b. It handles
// line folding (RFC 6350 §3.2), value/param escaping (§3.4), and
// multiple BEGIN:VCARD...END:VCARD blocks in one file (REQ-CTS-11).
//
// Each block yields one ParseResult. A malformed or structurally invalid
// block surfaces as ParseResult.Error; it does not abort the batch. The
// returned slice is nil only when b contains no BEGIN:VCARD blocks.
//
// internerFn may be nil (see PhotoInternFn). ParseVCards is total:
// malformed, truncated, or non-UTF-8 input never causes a panic.
//
// Example:
//
//	vcf := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Alice\r\nUID:urn:uuid:1\r\nEND:VCARD\r\n")
//	results := contacts.ParseVCards(vcf, nil)
//	if results[0].Error != nil {
//	    log.Fatal(results[0].Error)
//	}
//	card := results[0].Card
func ParseVCards(b []byte, internerFn PhotoInternFn) []ParseResult {
	if len(b) == 0 {
		return nil
	}
	defer func() {
		// Panic guard (REQ-CTS-11): any uncaught panic in a sub-function
		// becomes a structured error for the whole batch rather than
		// crashing the server. Under normal operation this should never
		// fire; the fuzz target exercises this boundary.
		_ = recover()
	}()

	b = coerceUTF8(b)
	unfolded := unfoldLines(b)
	blocks := splitVCardBlocks(unfolded)
	if len(blocks) == 0 {
		return nil
	}

	results := make([]ParseResult, 0, len(blocks))
	for _, block := range blocks {
		card, err := parseVCardBlock(block, internerFn)
		if err != nil {
			results = append(results, ParseResult{Error: err})
		} else {
			results = append(results, ParseResult{Card: card})
		}
	}
	return results
}

// GenerateVCard converts a JSContact Card to a vCard 4.0 byte sequence
// (REQ-CTS-12). Output is spec-conformant: VERSION:4.0, CRLF line
// endings, RFC 6350 §3.2 line folding at 75 octets, §3.4 escaping.
//
// Properties the vCard format cannot represent are collected into
// GenerateResult.Unrepresentable rather than being silently dropped.
// fetchFn may be nil (see PhotoFetchFn).
func GenerateVCard(card *Card, fetchFn PhotoFetchFn) (*GenerateResult, error) {
	if card == nil {
		return nil, errors.New("vcard: nil card")
	}

	var result GenerateResult
	var lines []string

	lines = append(lines, "BEGIN:VCARD")
	lines = append(lines, "VERSION:4.0")

	if card.UID != "" {
		lines = append(lines, "UID:"+escapeValue(card.UID))
	}

	if card.Kind != "" {
		if vkind := jscontactKindToVCard(card.Kind); vkind != "" {
			lines = append(lines, "KIND:"+vkind)
		} else {
			result.Unrepresentable = append(result.Unrepresentable, UnrepresentableProperty{
				Type:   "kind",
				Detail: card.Kind,
			})
		}
	}

	// FN (required in vCard 4.0) and N.
	if card.Name != nil {
		fn := card.Name.Full
		if fn == "" {
			fn = deriveFullName(card.Name)
		}
		lines = append(lines, "FN:"+escapeValue(fn))
		if n := generateN(card.Name); n != "" {
			lines = append(lines, n)
		}
	} else {
		// FN is required; fall back to any available display name.
		fn := card.DisplayName()
		lines = append(lines, "FN:"+escapeValue(fn))
	}

	for _, k := range sortedKeys(card.Emails) {
		e := card.Emails[k]
		var params []string
		if types := contextsToEmailTypes(e.Contexts); len(types) > 0 {
			params = append(params, "TYPE="+strings.Join(types, ","))
		}
		if e.Pref > 0 {
			params = append(params, fmt.Sprintf("PREF=%d", e.Pref))
		}
		lines = append(lines, buildLine("EMAIL", params, escapeValue(e.Address)))
	}

	for _, k := range sortedKeys(card.Phones) {
		p := card.Phones[k]
		var types []string
		types = append(types, featuresToTelTypes(p.Features)...)
		types = append(types, contextsToTelTypes(p.Contexts)...)
		var params []string
		if len(types) > 0 {
			params = append(params, "TYPE="+strings.Join(types, ","))
		}
		if p.Pref > 0 {
			params = append(params, fmt.Sprintf("PREF=%d", p.Pref))
		}
		lines = append(lines, buildLine("TEL", params, escapeValue(p.Number)))
	}

	for _, k := range sortedKeys(card.Addresses) {
		a := card.Addresses[k]
		var params []string
		if types := contextsToAddrTypes(a.Contexts); len(types) > 0 {
			params = append(params, "TYPE="+strings.Join(types, ","))
		}
		if a.Pref > 0 {
			params = append(params, fmt.Sprintf("PREF=%d", a.Pref))
		}
		if a.CountryCode != "" {
			params = append(params, "CC="+a.CountryCode)
		}
		lines = append(lines, buildLine("ADR", params, buildADRValue(a)))
	}

	for _, k := range sortedKeys(card.Organizations) {
		org := card.Organizations[k]
		parts := []string{escapeStructuredComponent(org.Name)}
		for _, u := range org.Units {
			parts = append(parts, escapeStructuredComponent(u))
		}
		lines = append(lines, "ORG:"+strings.Join(parts, ";"))
	}

	for _, k := range sortedKeys(card.Titles) {
		t := card.Titles[k]
		if t.Kind == "role" {
			lines = append(lines, "ROLE:"+escapeValue(t.Name))
		} else {
			lines = append(lines, "TITLE:"+escapeValue(t.Name))
		}
	}

	// Named RawJSON fields.
	if card.RawJSON != nil {
		if v, ok := card.RawJSON["nicknames"]; ok {
			lines = append(lines, generateNicknames(v)...)
		}
		if v, ok := card.RawJSON["notes"]; ok {
			lines = append(lines, generateNotes(v)...)
		}
		if v, ok := card.RawJSON["links"]; ok {
			lines = append(lines, generateLinks(v)...)
		}
		if v, ok := card.RawJSON["anniversaries"]; ok {
			lines = append(lines, generateAnniversaries(v)...)
		}
		if v, ok := card.RawJSON["media"]; ok {
			photoLines, err := generateMedia(v, fetchFn)
			if err != nil {
				return nil, err
			}
			lines = append(lines, photoLines...)
		}

		// Re-emit X-* pass-through entries.
		for _, key := range sortedKeys(card.RawJSON) {
			if !strings.HasPrefix(key, "vcard:x-") {
				continue
			}
			propName := strings.ToUpper(key[len("vcard:"):])
			var val string
			if err := json.Unmarshal(card.RawJSON[key], &val); err == nil && val != "" {
				lines = append(lines, propName+":"+escapeValue(val))
			}
		}

		// Collect unrepresentable JSContact-native properties.
		for _, key := range sortedKeys(card.RawJSON) {
			if isKnownRawJSONKey(key) {
				continue
			}
			if strings.HasPrefix(key, "vcard:") {
				continue
			}
			detail := ""
			if raw := card.RawJSON[key]; len(raw) <= 64 {
				detail = string(raw)
			}
			result.Unrepresentable = append(result.Unrepresentable, UnrepresentableProperty{
				Type:   key,
				Detail: detail,
			})
		}
	}

	sort.Slice(result.Unrepresentable, func(i, j int) bool {
		return result.Unrepresentable[i].Type < result.Unrepresentable[j].Type
	})

	lines = append(lines, "END:VCARD")

	var buf bytes.Buffer
	for _, line := range lines {
		buf.WriteString(foldLine(line))
	}
	result.VCard = buf.Bytes()
	return &result, nil
}

// ===== Parser internals =====

// vcardProp is one parsed vCard content line.
type vcardProp struct {
	Group  string
	Name   string              // upper-cased
	Params map[string][]string // upper-cased name -> original-case values
	Value  string
}

// coerceUTF8 replaces invalid UTF-8 byte sequences with U+FFFD so the
// rest of the parser operates on a valid string.
func coerceUTF8(b []byte) []byte {
	if utf8.Valid(b) {
		return b
	}
	var buf strings.Builder
	buf.Grow(len(b))
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		buf.WriteRune(r) // utf8.DecodeRune emits RuneError for bad bytes
		b = b[size:]
	}
	return []byte(buf.String())
}

// unfoldLines removes RFC 6350 §3.2 line folds. CRLF+LWSP and LF+LWSP
// are removed; bare CRs are stripped. Output uses LF as line separator.
func unfoldLines(b []byte) string {
	s := string(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")

	var buf strings.Builder
	buf.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\n' && i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\t') {
			i += 2
			continue
		}
		buf.WriteByte(s[i])
		i++
	}
	return buf.String()
}

// splitVCardBlocks finds all BEGIN:VCARD…END:VCARD sections in the
// unfolded (LF-separated) string. An incomplete trailing block (BEGIN
// without END) is included so the caller can report a truncation error.
func splitVCardBlocks(s string) []string {
	lines := strings.Split(s, "\n")
	var blocks []string
	var current []string
	inCard := false

	for _, line := range lines {
		upper := strings.ToUpper(strings.TrimSpace(line))
		switch upper {
		case "BEGIN:VCARD":
			inCard = true
			current = []string{line}
		case "END:VCARD":
			if inCard {
				current = append(current, line)
				blocks = append(blocks, strings.Join(current, "\n"))
				current = nil
				inCard = false
			}
		default:
			if inCard {
				current = append(current, line)
			}
		}
	}
	if inCard && len(current) > 0 {
		blocks = append(blocks, strings.Join(current, "\n"))
	}
	return blocks
}

// parseVCardBlock parses one BEGIN:VCARD…END:VCARD block.
func parseVCardBlock(s string, internerFn PhotoInternFn) (*Card, error) {
	lines := strings.Split(s, "\n")
	var props []vcardProp
	hasBegin, hasEnd := false, false

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		upper := strings.ToUpper(strings.TrimSpace(line))
		switch upper {
		case "BEGIN:VCARD":
			hasBegin = true
			continue
		case "END:VCARD":
			hasEnd = true
			continue
		}
		p, err := parseContentLine(line)
		if err != nil {
			// A single unparseable property is a card-level error.
			return nil, fmt.Errorf("vcard: property parse error %q: %w", vcardTruncate(line, 40), err)
		}
		if p != nil {
			props = append(props, *p)
		}
	}

	if !hasBegin {
		return nil, errors.New("vcard: missing BEGIN:VCARD")
	}
	if !hasEnd {
		return nil, errors.New("vcard: truncated card: missing END:VCARD")
	}
	return buildCard(props, internerFn)
}

// parseContentLine parses one vCard content line into a vcardProp.
// Returns nil on empty lines.
func parseContentLine(line string) (*vcardProp, error) {
	if strings.TrimSpace(line) == "" {
		return nil, nil
	}

	prop := &vcardProp{
		Params: make(map[string][]string),
	}

	// Find where the name ends (at first ';' or ':').
	nameEnd := -1
	for j := 0; j < len(line); j++ {
		if line[j] == ';' || line[j] == ':' {
			nameEnd = j
			break
		}
	}
	if nameEnd < 0 {
		return nil, errors.New("vcard: no value separator")
	}

	nameStr := strings.ToUpper(line[:nameEnd])
	if dot := strings.LastIndex(nameStr, "."); dot >= 0 {
		prop.Group = nameStr[:dot]
		prop.Name = nameStr[dot+1:]
	} else {
		prop.Name = nameStr
	}
	if prop.Name == "" {
		return nil, errors.New("vcard: empty property name")
	}

	i := nameEnd
	for i < len(line) && line[i] == ';' {
		i++ // skip ';'
		pname, pvals, newI := parseOneParam(line, i)
		i = newI
		if pname != "" {
			pname = strings.ToUpper(pname)
			prop.Params[pname] = append(prop.Params[pname], pvals...)
		}
	}

	if i >= len(line) || line[i] != ':' {
		return nil, fmt.Errorf("vcard: expected ':' at offset %d", i)
	}
	prop.Value = line[i+1:]
	return prop, nil
}

// parseOneParam parses one "name=v1,v2" or "name" param starting at i.
// Returns param name, values slice (may be nil for bare params), and new
// position. Errors are absorbed (lenient parse).
func parseOneParam(line string, i int) (name string, vals []string, newI int) {
	start := i
	for i < len(line) && line[i] != '=' && line[i] != ';' && line[i] != ':' {
		i++
	}
	name = strings.TrimSpace(line[start:i])
	if i >= len(line) || line[i] != '=' {
		return name, nil, i
	}
	i++ // skip '='

	for {
		var val string
		if i < len(line) && line[i] == '"' {
			// Quoted string: scan to closing '"', no unescaping needed.
			i++ // skip '"'
			qstart := i
			for i < len(line) && line[i] != '"' {
				i++
			}
			val = line[qstart:i]
			if i < len(line) {
				i++ // skip '"'
			}
		} else {
			pstart := i
			for i < len(line) && line[i] != ',' && line[i] != ';' && line[i] != ':' {
				i++
			}
			val = line[pstart:i]
		}
		vals = append(vals, val)
		if i >= len(line) || line[i] != ',' {
			break
		}
		i++ // skip ','
	}
	return name, vals, i
}

// buildCard maps a slice of parsed vcardProps to a Card.
func buildCard(props []vcardProp, internerFn PhotoInternFn) (*Card, error) {
	var c Card
	c.Version = "1.0"
	c.RawJSON = make(map[string]json.RawMessage)

	emails := make(map[string]EmailAddress)
	phones := make(map[string]Phone)
	addresses := make(map[string]Address)
	organizations := make(map[string]Organization)
	titles := make(map[string]Title)
	nicknames := make(map[string]map[string]interface{})
	notes := make(map[string]map[string]interface{})
	links := make(map[string]map[string]interface{})
	anniversaries := make(map[string]map[string]interface{})
	media := make(map[string]map[string]interface{})

	var fn string
	var nameObj *Name

	emailIdx, phoneIdx, addrIdx, orgIdx, titleIdx := 0, 0, 0, 0, 0
	nickIdx, noteIdx, urlIdx, photoIdx, annIdx := 0, 0, 0, 0, 0

	for i := range props {
		p := &props[i]
		switch p.Name {
		case "VERSION":
			// Accept any version for lenient parsing; we set 1.0 in JSContact.
		case "UID":
			if c.UID == "" {
				c.UID = unescapeValue(p.Value)
			}
		case "KIND":
			c.Kind = parseKind(p.Value)
		case "FN":
			if fn == "" {
				fn = unescapeValue(p.Value)
			}
		case "N":
			if nameObj == nil {
				nameObj = parseN(p.Value)
			}
		case "EMAIL":
			key := fmt.Sprintf("email/%d", emailIdx)
			emailIdx++
			emails[key] = parseEmail(p)
		case "TEL":
			key := fmt.Sprintf("phone/%d", phoneIdx)
			phoneIdx++
			phones[key] = parseTel(p)
		case "ADR":
			key := fmt.Sprintf("addr/%d", addrIdx)
			addrIdx++
			addresses[key] = parseADR(p)
		case "ORG":
			key := fmt.Sprintf("org/%d", orgIdx)
			orgIdx++
			organizations[key] = parseORG(p)
		case "TITLE":
			key := fmt.Sprintf("title/%d", titleIdx)
			titleIdx++
			titles[key] = Title{Name: unescapeValue(p.Value), Kind: "title"}
		case "ROLE":
			key := fmt.Sprintf("role/%d", titleIdx)
			titleIdx++
			titles[key] = Title{Name: unescapeValue(p.Value), Kind: "role"}
		case "NICKNAME":
			for _, nick := range splitEscaped(p.Value, ',') {
				nick = unescapeValue(nick)
				if nick == "" {
					continue
				}
				key := fmt.Sprintf("nick/%d", nickIdx)
				nickIdx++
				nicknames[key] = map[string]interface{}{
					"@type": "Nickname",
					"name":  nick,
				}
			}
		case "NOTE":
			key := fmt.Sprintf("note/%d", noteIdx)
			noteIdx++
			notes[key] = map[string]interface{}{
				"@type": "Note",
				"note":  unescapeValue(p.Value),
			}
		case "URL":
			key := fmt.Sprintf("url/%d", urlIdx)
			urlIdx++
			entry := map[string]interface{}{
				"@type": "Link",
				"kind":  "uri",
				"uri":   unescapeValue(p.Value),
			}
			if pref := getPref(p); pref > 0 {
				entry["pref"] = pref
			}
			links[key] = entry
		case "BDAY":
			if d := parseVCardDate(p); d != nil {
				anniversaries["bday"] = map[string]interface{}{
					"@type": "Anniversary",
					"kind":  "birth",
					"date":  d,
				}
			}
		case "ANNIVERSARY":
			key := fmt.Sprintf("ann/%d", annIdx)
			annIdx++
			if d := parseVCardDate(p); d != nil {
				anniversaries[key] = map[string]interface{}{
					"@type": "Anniversary",
					"kind":  "wedding",
					"date":  d,
				}
			}
		case "PHOTO":
			entry, err := parsePhoto(p, internerFn)
			if err == nil && entry != nil {
				key := fmt.Sprintf("photo/%d", photoIdx)
				photoIdx++
				media[key] = entry
			}
		default:
			// Preserve X-* properties for round-trip pass-through.
			if strings.HasPrefix(p.Name, "X-") {
				key := "vcard:" + strings.ToLower(p.Name)
				if _, exists := c.RawJSON[key]; !exists {
					raw, _ := json.Marshal(unescapeValue(p.Value))
					c.RawJSON[key] = raw
				}
			}
		}
	}

	// Assemble the Name object.
	if nameObj != nil || fn != "" {
		if nameObj == nil {
			nameObj = &Name{}
		}
		if fn != "" {
			nameObj.Full = fn
		}
		c.Name = nameObj
	}

	if len(emails) > 0 {
		c.Emails = emails
	}
	if len(phones) > 0 {
		c.Phones = phones
	}
	if len(addresses) > 0 {
		c.Addresses = addresses
	}
	if len(organizations) > 0 {
		c.Organizations = organizations
	}
	if len(titles) > 0 {
		c.Titles = titles
	}

	// Store non-typed fields in RawJSON.
	if len(nicknames) > 0 {
		c.RawJSON["nicknames"] = mustMarshal(nicknames)
	}
	if len(notes) > 0 {
		c.RawJSON["notes"] = mustMarshal(notes)
	}
	if len(links) > 0 {
		c.RawJSON["links"] = mustMarshal(links)
	}
	if len(anniversaries) > 0 {
		c.RawJSON["anniversaries"] = mustMarshal(anniversaries)
	}
	if len(media) > 0 {
		c.RawJSON["media"] = mustMarshal(media)
	}

	return &c, nil
}

// parseKind maps a vCard KIND value to a JSContact kind string.
// "org" has no JSContact equivalent; returns "" (kind left unset).
func parseKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "individual", "group", "location", "application", "device":
		return strings.ToLower(strings.TrimSpace(value))
	}
	return ""
}

// parseN maps a vCard N structured value to a JSContact Name.
// N components: surname;given;additional;prefix;suffix
func parseN(value string) *Name {
	kindMap := []string{"surname", "given", "middle", "prefix", "suffix"}
	parts := splitEscaped(value, ';')

	var components []NameComponent
	for i, part := range parts {
		if i >= len(kindMap) {
			break
		}
		for _, v := range splitEscaped(part, ',') {
			v = unescapeValue(v)
			if v != "" {
				components = append(components, NameComponent{
					Kind:  kindMap[i],
					Value: v,
				})
			}
		}
	}
	if len(components) == 0 {
		return nil
	}
	return &Name{Components: components}
}

// parseEmail maps a vCard EMAIL property to a JSContact EmailAddress.
func parseEmail(p *vcardProp) EmailAddress {
	e := EmailAddress{Address: unescapeValue(p.Value)}
	e.Contexts = emailTypesToContexts(getPropTypes(p))
	e.Pref = getPref(p)
	return e
}

// parseTel maps a vCard TEL property to a JSContact Phone.
func parseTel(p *vcardProp) Phone {
	ph := Phone{Number: unescapeValue(p.Value)}
	ph.Contexts, ph.Features = telTypesToContextsFeatures(getPropTypes(p))
	ph.Pref = getPref(p)
	return ph
}

// parseADR maps a vCard ADR structured property to a JSContact Address.
// ADR component order: pobox;ext;street;locality;region;postcode;country
func parseADR(p *vcardProp) Address {
	a := Address{}
	a.Contexts = addrTypesToContexts(getPropTypes(p))
	a.Pref = getPref(p)

	if cc, ok := p.Params["CC"]; ok && len(cc) > 0 {
		a.CountryCode = cc[0]
	}

	adrKinds := []string{
		"postOfficeBox",
		"extension",
		"name",     // street address
		"locality", // city (RFC 9553 JSContact kind)
		"region",   // state/region
		"postcode", // postal code
		"country",  // country name
	}

	var comps []AddressComponent
	for i, part := range splitEscaped(p.Value, ';') {
		if i >= len(adrKinds) {
			break
		}
		v := unescapeValue(part)
		if v != "" {
			comps = append(comps, AddressComponent{
				Kind:  adrKinds[i],
				Value: v,
			})
		}
	}
	if len(comps) > 0 {
		a.Components = comps
	}
	return a
}

// parseORG maps a vCard ORG structured value to a JSContact Organization.
// ORG: name;unit1;unit2;...
func parseORG(p *vcardProp) Organization {
	parts := splitEscaped(p.Value, ';')
	org := Organization{}
	if len(parts) > 0 {
		org.Name = unescapeValue(parts[0])
	}
	for _, part := range parts[1:] {
		u := unescapeValue(part)
		if u != "" {
			org.Units = append(org.Units, u)
		}
	}
	return org
}

// parseVCardDate parses a vCard date value (BDAY/ANNIVERSARY) into a
// JSContact PartialDate map. Returns nil for unrecognised formats.
func parseVCardDate(p *vcardProp) map[string]interface{} {
	s := strings.TrimSpace(p.Value)

	// VALUE=text: free-form text; not representable as PartialDate.
	for _, v := range p.Params["VALUE"] {
		if strings.EqualFold(v, "text") {
			return nil
		}
	}

	// Year-omitted: --MMDD or --MM-DD or --MM
	if strings.HasPrefix(s, "--") {
		rest := strings.ReplaceAll(s[2:], "-", "")
		switch len(rest) {
		case 4:
			mm, e1 := strconv.Atoi(rest[:2])
			dd, e2 := strconv.Atoi(rest[2:])
			if e1 == nil && e2 == nil {
				return map[string]interface{}{"@type": "PartialDate", "month": mm, "day": dd}
			}
		case 2:
			mm, e := strconv.Atoi(rest)
			if e == nil {
				return map[string]interface{}{"@type": "PartialDate", "month": mm}
			}
		}
		return nil
	}

	// Strip time portion.
	clean := s
	if idx := strings.IndexByte(clean, 'T'); idx >= 0 {
		clean = clean[:idx]
	}
	clean = strings.ReplaceAll(clean, "-", "")

	switch len(clean) {
	case 4:
		yyyy, e := strconv.Atoi(clean)
		if e == nil {
			return map[string]interface{}{"@type": "PartialDate", "year": yyyy}
		}
	case 8:
		yyyy, e1 := strconv.Atoi(clean[:4])
		mm, e2 := strconv.Atoi(clean[4:6])
		dd, e3 := strconv.Atoi(clean[6:8])
		if e1 == nil && e2 == nil && e3 == nil {
			return map[string]interface{}{"@type": "PartialDate", "year": yyyy, "month": mm, "day": dd}
		}
	}
	return nil
}

// parsePhoto maps a vCard PHOTO property to a JSContact MediaResource map.
// REQ-CTS-14: inline data is passed to internerFn if non-nil; otherwise
// the data URI is stored directly so no bytes are lost.
func parsePhoto(p *vcardProp, internerFn PhotoInternFn) (map[string]interface{}, error) {
	val := p.Value

	// Data URI (vCard 4.0 preferred inline form).
	if strings.HasPrefix(strings.ToLower(val), "data:") {
		return parseDataURIPhoto(val, internerFn)
	}

	// Legacy ENCODING=b (base64 inline, vCard 3.0 / 2.1).
	for _, enc := range p.Params["ENCODING"] {
		if strings.EqualFold(enc, "b") || strings.EqualFold(enc, "base64") {
			mediaType := "image/jpeg"
			if typeVals := p.Params["TYPE"]; len(typeVals) > 0 {
				mt := strings.ToLower(typeVals[0])
				if !strings.Contains(mt, "/") {
					mt = "image/" + mt
				}
				mediaType = mt
			}
			return parseBase64Photo(val, mediaType, internerFn)
		}
	}

	// Regular URI.
	entry := map[string]interface{}{
		"@type": "MediaResource",
		"kind":  "photo",
		"uri":   val,
	}
	if mt := inferMediaType(val); mt != "" {
		entry["mediaType"] = mt
	}
	return entry, nil
}

func parseDataURIPhoto(dataURI string, internerFn PhotoInternFn) (map[string]interface{}, error) {
	after := dataURI[len("data:"):]
	commaIdx := strings.IndexByte(after, ',')
	if commaIdx < 0 {
		return nil, errors.New("vcard: malformed data URI: no comma")
	}
	meta := after[:commaIdx]
	rawData64 := after[commaIdx+1:]

	metaParts := strings.Split(meta, ";")
	mediaType := metaParts[0]
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	isBase64 := len(metaParts) > 1 && strings.EqualFold(metaParts[len(metaParts)-1], "base64")

	var raw []byte
	var err error
	if isBase64 {
		raw, err = base64.StdEncoding.DecodeString(rawData64)
		if err != nil {
			raw, err = base64.URLEncoding.DecodeString(rawData64)
		}
		if err != nil {
			return nil, fmt.Errorf("vcard: photo base64 decode: %w", err)
		}
	} else {
		raw = []byte(rawData64)
	}

	if internerFn != nil {
		blobId, e := internerFn(mediaType, raw)
		if e == nil && blobId != "" {
			return map[string]interface{}{
				"@type":     "MediaResource",
				"kind":      "photo",
				"blobId":    blobId,
				"mediaType": mediaType,
			}, nil
		}
	}
	return map[string]interface{}{
		"@type":     "MediaResource",
		"kind":      "photo",
		"uri":       dataURI,
		"mediaType": mediaType,
	}, nil
}

func parseBase64Photo(b64, mediaType string, internerFn PhotoInternFn) (map[string]interface{}, error) {
	// Strip whitespace that line-unfolding may have left.
	b64 = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, b64)
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("vcard: photo base64 decode: %w", err)
	}
	if internerFn != nil {
		blobId, e := internerFn(mediaType, raw)
		if e == nil && blobId != "" {
			return map[string]interface{}{
				"@type":     "MediaResource",
				"kind":      "photo",
				"blobId":    blobId,
				"mediaType": mediaType,
			}, nil
		}
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	dataURI := "data:" + mediaType + ";base64," + encoded
	return map[string]interface{}{
		"@type":     "MediaResource",
		"kind":      "photo",
		"uri":       dataURI,
		"mediaType": mediaType,
	}, nil
}

// ===== Generator helpers =====

func generateNicknames(raw json.RawMessage) []string {
	var nicks map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &nicks); err != nil {
		return nil
	}
	var lines []string
	for _, k := range sortedKeys(nicks) {
		name, _ := nicks[k]["name"].(string)
		if name != "" {
			lines = append(lines, "NICKNAME:"+escapeValue(name))
		}
	}
	return lines
}

func generateNotes(raw json.RawMessage) []string {
	var notes map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &notes); err != nil {
		return nil
	}
	var lines []string
	for _, k := range sortedKeys(notes) {
		note, _ := notes[k]["note"].(string)
		if note != "" {
			lines = append(lines, "NOTE:"+escapeValue(note))
		}
	}
	return lines
}

func generateLinks(raw json.RawMessage) []string {
	var links map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &links); err != nil {
		return nil
	}
	var lines []string
	for _, k := range sortedKeys(links) {
		lnk := links[k]
		uri, _ := lnk["uri"].(string)
		if uri == "" {
			continue
		}
		var params []string
		if pref, ok := lnk["pref"].(float64); ok && pref > 0 {
			params = append(params, fmt.Sprintf("PREF=%d", int(pref)))
		}
		lines = append(lines, buildLine("URL", params, uri))
	}
	return lines
}

func generateAnniversaries(raw json.RawMessage) []string {
	var anns map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &anns); err != nil {
		return nil
	}
	var lines []string
	for _, k := range sortedKeys(anns) {
		ann := anns[k]
		kind, _ := ann["kind"].(string)

		var dateMap map[string]interface{}
		switch d := ann["date"].(type) {
		case map[string]interface{}:
			dateMap = d
		}
		if dateMap == nil {
			continue
		}
		dateStr, err := jscontactDateToVCard(dateMap)
		if err != nil {
			continue
		}
		if kind == "birth" {
			lines = append(lines, "BDAY:"+dateStr)
		} else {
			lines = append(lines, "ANNIVERSARY:"+dateStr)
		}
	}
	return lines
}

func generateMedia(raw json.RawMessage, fetchFn PhotoFetchFn) ([]string, error) {
	var meds map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &meds); err != nil {
		return nil, nil //nolint:nilerr // malformed RawJSON is silently skipped
	}
	var lines []string
	for _, k := range sortedKeys(meds) {
		med := meds[k]
		kind, _ := med["kind"].(string)
		if kind != "photo" {
			continue
		}
		line, err := generatePhotoLine(med, fetchFn)
		if err != nil {
			return nil, err
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// generatePhotoLine builds a PHOTO content line from a JSContact media
// entry. REQ-CTS-14: if fetchFn is set and blobId is present, the data
// is fetched and embedded as a data URI; otherwise the stored URI or
// blobId reference is emitted as-is.
func generatePhotoLine(med map[string]interface{}, fetchFn PhotoFetchFn) (string, error) {
	blobId, hasBlobId := med["blobId"].(string)
	uri, hasURI := med["uri"].(string)
	mediaType, _ := med["mediaType"].(string)

	if hasBlobId && blobId != "" {
		if fetchFn != nil {
			mt, data, err := fetchFn(blobId)
			if err != nil {
				return "", fmt.Errorf("vcard: fetch photo blob %q: %w", blobId, err)
			}
			if mt != "" {
				mediaType = mt
			}
			if mediaType == "" {
				mediaType = "image/jpeg"
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			return "PHOTO:data:" + mediaType + ";base64," + encoded, nil
		}
		// No fetchFn: emit blobId as an opaque URI reference.
		return "PHOTO:" + blobId, nil
	}

	if hasURI && uri != "" {
		return "PHOTO:" + uri, nil
	}
	return "", nil
}

// jscontactDateToVCard converts a JSContact PartialDate map to a vCard
// date string.
func jscontactDateToVCard(d map[string]interface{}) (string, error) {
	// JSON numbers unmarshal as float64.
	year, hasYear := d["year"].(float64)
	month, hasMonth := d["month"].(float64)
	day, hasDay := d["day"].(float64)

	switch {
	case hasYear && hasMonth && hasDay:
		return fmt.Sprintf("%04d%02d%02d", int(year), int(month), int(day)), nil
	case !hasYear && hasMonth && hasDay:
		return fmt.Sprintf("--%02d%02d", int(month), int(day)), nil
	case hasYear && hasMonth && !hasDay:
		return fmt.Sprintf("%04d-%02d", int(year), int(month)), nil
	case hasYear && !hasMonth && !hasDay:
		return fmt.Sprintf("%04d", int(year)), nil
	}
	return "", errors.New("vcard: unsupported partial date combination")
}

// generateN builds a vCard N line from a JSContact Name.
// N fields: surname;given;additional;prefix;suffix
func generateN(n *Name) string {
	if n == nil {
		return ""
	}
	var surname, given, middle, prefix, suffix []string
	for _, c := range n.Components {
		v := escapeStructuredComponent(c.Value)
		switch c.Kind {
		case "surname":
			surname = append(surname, v)
		case "given":
			given = append(given, v)
		case "middle":
			middle = append(middle, v)
		case "prefix":
			prefix = append(prefix, v)
		case "suffix", "credential":
			suffix = append(suffix, v)
		}
	}
	if len(surname)+len(given)+len(middle)+len(prefix)+len(suffix) == 0 {
		return ""
	}
	join := func(s []string) string { return strings.Join(s, ",") }
	return "N:" + strings.Join([]string{
		join(surname), join(given), join(middle), join(prefix), join(suffix),
	}, ";")
}

// buildADRValue converts a JSContact Address to a vCard ADR value.
// Output: pobox;ext;street;locality;region;postcode;country
func buildADRValue(a Address) string {
	fields := make([]string, 7)
	assign := func(idx int, v string) {
		if fields[idx] == "" {
			fields[idx] = escapeStructuredComponent(v)
		}
	}
	for _, comp := range a.Components {
		switch comp.Kind {
		case "postOfficeBox":
			assign(0, comp.Value)
		case "extension":
			assign(1, comp.Value)
		case "name", "number", "building", "direction", "block",
			"subdistrict", "landmark":
			assign(2, comp.Value)
		case "locality", "city", "district", "county":
			assign(3, comp.Value)
		case "province", "region", "state":
			assign(4, comp.Value)
		case "postcode":
			assign(5, comp.Value)
		case "country":
			assign(6, comp.Value)
		}
	}
	// Use Full when no individual components mapped.
	if a.Full != "" && allEmpty(fields) {
		fields[2] = escapeStructuredComponent(a.Full)
	}
	return strings.Join(fields, ";")
}

// deriveFullName builds a display string from Name.Components, mirrors
// Card.DisplayName logic.
func deriveFullName(n *Name) string {
	if n == nil {
		return ""
	}
	var given, surname string
	var others []string
	for _, c := range n.Components {
		switch c.Kind {
		case "given":
			given = c.Value
		case "surname":
			surname = c.Value
		default:
			others = append(others, c.Value)
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
	return strings.Join(parts, " ")
}

// jscontactKindToVCard maps a JSContact kind to a vCard KIND value.
// Returns "" for kinds with no vCard equivalent (caller reports as
// UnrepresentableProperty).
func jscontactKindToVCard(kind string) string {
	switch kind {
	case "individual", "group", "location", "application", "device":
		return kind
	}
	return ""
}

// ===== Type mapping helpers =====

func getPropTypes(p *vcardProp) []string {
	var types []string
	for _, v := range p.Params["TYPE"] {
		for _, t := range strings.Split(v, ",") {
			t = strings.ToLower(strings.TrimSpace(t))
			if t != "" {
				types = append(types, t)
			}
		}
	}
	return types
}

func getPref(p *vcardProp) int {
	if vals := p.Params["PREF"]; len(vals) > 0 {
		if v, err := strconv.Atoi(vals[0]); err == nil && v >= 1 && v <= 100 {
			return v
		}
	}
	return 0
}

func emailTypesToContexts(types []string) map[string]bool {
	ctx := make(map[string]bool)
	for _, t := range types {
		switch t {
		case "work":
			ctx["work"] = true
		case "home":
			ctx["private"] = true
		}
	}
	if len(ctx) == 0 {
		return nil
	}
	return ctx
}

func telTypesToContextsFeatures(types []string) (contexts, features map[string]bool) {
	ctx := make(map[string]bool)
	feat := make(map[string]bool)
	for _, t := range types {
		switch t {
		case "work":
			ctx["work"] = true
		case "home":
			ctx["private"] = true
		case "voice":
			feat["voice"] = true
		case "cell", "pcs":
			feat["cell"] = true
		case "fax":
			feat["fax"] = true
		case "text":
			feat["text"] = true
		case "video":
			feat["video"] = true
		case "textphone":
			feat["textphone"] = true
		case "pager":
			feat["pager"] = true
		}
	}
	if len(ctx) == 0 {
		ctx = nil
	}
	if len(feat) == 0 {
		feat = nil
	}
	return ctx, feat
}

func addrTypesToContexts(types []string) map[string]bool {
	ctx := make(map[string]bool)
	for _, t := range types {
		switch t {
		case "work":
			ctx["work"] = true
		case "home":
			ctx["private"] = true
		}
	}
	if len(ctx) == 0 {
		return nil
	}
	return ctx
}

func contextsToEmailTypes(contexts map[string]bool) []string {
	var types []string
	if contexts["work"] {
		types = append(types, "work")
	}
	if contexts["private"] {
		types = append(types, "home")
	}
	return types
}

func featuresToTelTypes(features map[string]bool) []string {
	var types []string
	for _, f := range []string{"voice", "cell", "fax", "text", "video", "textphone", "pager"} {
		if features[f] {
			types = append(types, f)
		}
	}
	return types
}

func contextsToTelTypes(contexts map[string]bool) []string {
	var types []string
	if contexts["work"] {
		types = append(types, "work")
	}
	if contexts["private"] {
		types = append(types, "home")
	}
	return types
}

func contextsToAddrTypes(contexts map[string]bool) []string {
	var types []string
	if contexts["work"] {
		types = append(types, "work")
	}
	if contexts["private"] {
		types = append(types, "home")
	}
	return types
}

// ===== Escaping / value helpers =====

// unescapeValue unescapes vCard property values per RFC 6350 §3.4.
func unescapeValue(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var buf strings.Builder
	buf.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				buf.WriteByte('\n')
			case '\\':
				buf.WriteByte('\\')
			case ',':
				buf.WriteByte(',')
			case ';':
				buf.WriteByte(';')
			default:
				// RFC 6350 sec 3.4 defines only the escapes above; any
				// other "\X" sequence is not conformant. Some exporters
				// (e.g. Gmail) escape characters like ':' anyway
				// (`http\://...`), so treat an unrecognized escape as a
				// literal character and drop the backslash.
				buf.WriteByte(s[i+1])
			}
			i += 2
		} else {
			buf.WriteByte(s[i])
			i++
		}
	}
	return buf.String()
}

// escapeValue escapes a vCard property value. Escapes backslash and
// newlines. Does not escape ',' or ';' (use escapeStructuredComponent
// for fields inside structured values).
func escapeValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// escapeStructuredComponent escapes a single component within a
// structured vCard value, additionally escaping ',' and ';'.
func escapeStructuredComponent(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, ";", `\;`)
	return s
}

// splitEscaped splits s on sep, respecting backslash escaping. The
// returned parts are raw (not unescaped); callers call unescapeValue.
func splitEscaped(s string, sep byte) []string {
	var parts []string
	var buf strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			buf.WriteByte(s[i])
			buf.WriteByte(s[i+1])
			i += 2
			continue
		}
		if s[i] == sep {
			parts = append(parts, buf.String())
			buf.Reset()
			i++
			continue
		}
		buf.WriteByte(s[i])
		i++
	}
	parts = append(parts, buf.String())
	return parts
}

// ===== Output formatting =====

// foldLine applies RFC 6350 §3.2 line folding. Output lines are at most
// 75 octets + CRLF; continuation lines begin with a single space.
// Folds respect UTF-8 character boundaries.
func foldLine(line string) string {
	const maxFirst = 75
	const maxCont = 74

	if len(line) <= maxFirst {
		return line + "\r\n"
	}

	var buf bytes.Buffer
	split := utf8BoundaryAtOrBefore(line, maxFirst)
	buf.WriteString(line[:split])
	buf.WriteString("\r\n")
	line = line[split:]

	for len(line) > 0 {
		buf.WriteByte(' ')
		split = utf8BoundaryAtOrBefore(line, maxCont)
		buf.WriteString(line[:split])
		buf.WriteString("\r\n")
		line = line[split:]
	}
	return buf.String()
}

// utf8BoundaryAtOrBefore returns the largest byte index <= max that
// falls on a UTF-8 character boundary. Ensures foldLine never splits a
// multi-byte sequence across a fold point.
func utf8BoundaryAtOrBefore(s string, max int) int {
	if max >= len(s) {
		return len(s)
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return max
}

// buildLine constructs a content line from a property name, parameter
// list, and value. Parameters are semicolon-separated.
func buildLine(name string, params []string, value string) string {
	if len(params) == 0 {
		return name + ":" + value
	}
	return name + ";" + strings.Join(params, ";") + ":" + value
}

// ===== Miscellaneous =====

func inferMediaType(uri string) string {
	lower := strings.ToLower(uri)
	switch {
	case strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	}
	return ""
}

func allEmpty(fields []string) bool {
	for _, f := range fields {
		if f != "" {
			return false
		}
	}
	return true
}

func vcardTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// isKnownRawJSONKey reports whether key is a well-known JSContact or
// vCard-mapped RawJSON key that GenerateVCard handles explicitly.
func isKnownRawJSONKey(key string) bool {
	switch key {
	case "version", "kind", "uid",
		"name", "emails", "phones", "addresses",
		"organizations", "titles",
		"nicknames", "notes", "links", "anniversaries", "media":
		return true
	}
	return false
}
