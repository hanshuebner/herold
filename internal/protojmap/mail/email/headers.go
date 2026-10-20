package email

import (
	"bytes"
	"encoding/json"
	"net/mail"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// rawHeaderField is one header field of a message as it appears on the
// wire: the name before the colon and the raw value, every octet after
// the colon up to the field's terminating CRLF, folding included
// (RFC 8621 section 4.1.2.1, the Raw form).
type rawHeaderField struct {
	Name  string
	Value string
}

// rawHeaderFields splits the header section of raw into its fields in
// message order. Bare LF line ends count as CRLF; a continuation line
// (one starting with a space or a tab) joins the field before it, with
// its line break kept in the value as CRLF. Lines without a colon that
// are not continuations are skipped.
func rawHeaderFields(raw []byte) []rawHeaderField {
	var out []rawHeaderField
	for len(raw) > 0 {
		var line []byte
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			line, raw = raw[:i], raw[i+1:]
		} else {
			line, raw = raw, nil
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			break // end of the header section
		}
		if line[0] == ' ' || line[0] == '\t' {
			if len(out) > 0 {
				out[len(out)-1].Value += "\r\n" + string(line)
			}
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		out = append(out, rawHeaderField{
			Name:  strings.TrimRight(string(line[:colon]), " \t"),
			Value: string(line[colon+1:]),
		})
	}
	return out
}

// unfoldHeader removes the line breaks of a folded header value
// (RFC 5322 section 2.2.3).
func unfoldHeader(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "")
	return strings.ReplaceAll(v, "\n", "")
}

// headerPropertyValue renders the dynamic Email property
// "header:{name}[:{form}][:all]" (RFC 8621 section 4.1.3) from the
// message's header fields. Without ":all" the value is that of the last
// field with the name, or null when there is none; with ":all" it is an
// array holding one value per field in message order, empty when there
// is none.
func headerPropertyValue(fields []rawHeaderField, prop string) json.RawMessage {
	parts := strings.Split(strings.TrimPrefix(prop, "header:"), ":")
	name := parts[0]
	parts = parts[1:]
	all := false
	if len(parts) > 0 && parts[len(parts)-1] == "all" {
		all = true
		parts = parts[:len(parts)-1]
	}
	form := "asRaw"
	if len(parts) == 1 {
		form = parts[0]
	} else if len(parts) > 1 {
		return jsonNull()
	}
	var values []any
	for _, f := range fields {
		if strings.EqualFold(f.Name, name) {
			values = append(values, headerForm(f.Value, form))
		}
	}
	var v any
	switch {
	case all:
		if values == nil {
			values = []any{}
		}
		v = values
	case len(values) > 0:
		v = values[len(values)-1]
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return jsonNull()
	}
	return encoded
}

// headerForm parses one raw header value into the named form
// (RFC 8621 section 4.1.2). A value that does not parse in the form is
// null.
func headerForm(raw, form string) any {
	switch strings.ToLower(form) {
	case "asraw":
		return raw
	case "astext":
		return mailparse.DecodeHeader(strings.TrimSpace(unfoldHeader(raw)))
	case "asaddresses":
		var out []jmapAddress
		for _, g := range groupedAddresses(raw) {
			out = append(out, g.Addresses...)
		}
		if out == nil {
			out = []jmapAddress{}
		}
		return out
	case "asgroupedaddresses":
		return groupedAddresses(raw)
	case "asmessageids":
		ids := angleBracketed(unfoldHeader(raw))
		if len(ids) == 0 {
			return nil
		}
		return ids
	case "asdate":
		t, err := mail.ParseDate(strings.TrimSpace(unfoldHeader(raw)))
		if err != nil {
			return nil
		}
		return rfc3339UTC(t)
	case "asurls":
		urls := angleBracketed(unfoldHeader(raw))
		if len(urls) == 0 {
			return nil
		}
		return urls
	}
	return nil
}

// angleBracketed returns the contents of every <...> in v, in order,
// with whitespace removed from each (RFC 5322 msg-id, RFC 2369 URL
// lists). Brackets inside quoted strings and comments do not count.
func angleBracketed(v string) []string {
	var out []string
	inQuote, depth, start := false, 0, -1
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && (inQuote || depth > 0):
			i++
		case inQuote:
			if c == '"' {
				inQuote = false
			}
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case depth > 0:
		case c == '"' && start < 0:
			inQuote = true
		case c == '<' && start < 0:
			start = i + 1
		case c == '>' && start >= 0:
			if s := strings.Join(strings.Fields(v[start:i]), ""); s != "" {
				out = append(out, s)
			}
			start = -1
		}
	}
	return out
}

// jmapAddressGroup is an EmailAddressGroup (RFC 8621 section 4.1.2.4).
type jmapAddressGroup struct {
	Name      *string       `json:"name"`
	Addresses []jmapAddress `json:"addresses"`
}

// groupedAddresses parses an address-list header value into address
// groups: each RFC 5322 group becomes a named group, and each run of
// consecutive addresses outside a group becomes a group with a null
// name.
func groupedAddresses(raw string) []jmapAddressGroup {
	v := unfoldHeader(raw)
	out := []jmapAddressGroup{}
	addUngrouped := func(seg string) {
		addrs := parseAddressList(seg)
		if len(addrs) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Name == nil {
			out[n-1].Addresses = append(out[n-1].Addresses, addrs...)
			return
		}
		out = append(out, jmapAddressGroup{Addresses: addrs})
	}
	inQuote, depth, angle := false, 0, false
	segStart := 0 // start of the text not yet assigned to a group
	groupAt := -1 // start of the current group's address list, or -1
	groupName := ""
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && (inQuote || depth > 0):
			i++
		case inQuote:
			if c == '"' {
				inQuote = false
			}
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case depth > 0:
		case c == '"':
			inQuote = true
		case c == '<':
			angle = true
		case c == '>':
			angle = false
		case angle:
		case c == ':' && groupAt < 0:
			// display-name ":" starts a group; the text since the last
			// top-level comma before it is the group's name.
			nameStart := segStart
			if j := lastTopLevelComma(v[segStart:i]); j >= 0 {
				addUngrouped(v[segStart : segStart+j])
				nameStart = segStart + j + 1
			}
			groupName = mailparse.DecodeHeader(strings.Trim(strings.TrimSpace(v[nameStart:i]), `"`))
			groupAt = i + 1
		case c == ';' && groupAt >= 0:
			name := groupName
			addrs := parseAddressList(v[groupAt:i])
			if addrs == nil {
				addrs = []jmapAddress{}
			}
			out = append(out, jmapAddressGroup{Name: &name, Addresses: addrs})
			groupAt = -1
			segStart = i + 1
		}
	}
	if groupAt >= 0 {
		// An unterminated group runs to the end of the value.
		name := groupName
		addrs := parseAddressList(v[groupAt:])
		if addrs == nil {
			addrs = []jmapAddress{}
		}
		out = append(out, jmapAddressGroup{Name: &name, Addresses: addrs})
		return out
	}
	addUngrouped(strings.TrimLeft(v[segStart:], ", \t"))
	return out
}

// lastTopLevelComma returns the index of the last comma in s that is
// outside quoted strings, comments and angle brackets, or -1.
func lastTopLevelComma(s string) int {
	last := -1
	inQuote, depth, angle := false, 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && (inQuote || depth > 0):
			i++
		case inQuote:
			if c == '"' {
				inQuote = false
			}
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case depth > 0:
		case c == '"':
			inQuote = true
		case c == '<':
			angle = true
		case c == '>':
			angle = false
		case c == ',' && !angle:
			last = i
		}
	}
	return last
}

// emailHeaders renders the Email "headers" property: every header
// field of the message in order, with its raw value.
func emailHeaders(fields []rawHeaderField) []bodyPartHeader {
	out := make([]bodyPartHeader, 0, len(fields))
	for _, f := range fields {
		out = append(out, bodyPartHeader(f))
	}
	return out
}

// withoutStoredXHeroldRecipient drops the X-Herold-Recipient fields a
// stored message carries from fields whose first field is the one
// herold injected at render time (REQ-FLOW-34), so the header reports
// the envelope recipient herold recorded and nothing a sender wrote.
func withoutStoredXHeroldRecipient(fields []rawHeaderField) []rawHeaderField {
	if len(fields) == 0 {
		return fields
	}
	out := fields[:1:1]
	for _, f := range fields[1:] {
		if !strings.EqualFold(f.Name, mailparse.XHeroldRecipientHeader) {
			out = append(out, f)
		}
	}
	return out
}
