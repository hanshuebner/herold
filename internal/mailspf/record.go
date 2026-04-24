package mailspf

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ErrMalformedRecord reports a syntactically invalid SPF record. Callers
// treat it as a permanent error (RFC 7208 §2.6.7).
var ErrMalformedRecord = errors.New("mailspf: malformed record")

// Qualifier is the prefix byte that decides the result of a matched
// mechanism. Missing qualifier implies Pass.
type Qualifier byte

// Qualifier values per RFC 7208 §4.6.2.
const (
	QualifierPass     Qualifier = '+'
	QualifierFail     Qualifier = '-'
	QualifierSoftFail Qualifier = '~'
	QualifierNeutral  Qualifier = '?'
)

// MechanismName identifies one of the SPF mechanisms.
type MechanismName string

// MechanismName values per RFC 7208 §5. "ptr" is intentionally parsed but
// rejected at evaluation time because it is deprecated (RFC 7208 §5.5).
const (
	MechanismAll     MechanismName = "all"
	MechanismInclude MechanismName = "include"
	MechanismA       MechanismName = "a"
	MechanismMX      MechanismName = "mx"
	MechanismPTR     MechanismName = "ptr"
	MechanismIP4     MechanismName = "ip4"
	MechanismIP6     MechanismName = "ip6"
	MechanismExists  MechanismName = "exists"
)

// Mechanism is one directive of an SPF record.
type Mechanism struct {
	Qualifier Qualifier
	Name      MechanismName
	// Domain is the "domain-spec" argument (include / a / mx / ptr /
	// exists). Empty string means "use the current domain".
	Domain string
	// IP is the parsed CIDR for ip4 / ip6.
	IP *net.IPNet
	// Prefix4 and Prefix6 are the optional dual-cidr prefixes on a / mx
	// ("a:host/24//64"). Zero means "use /32" or "/128" respectively.
	Prefix4 int
	Prefix6 int
}

// Modifier is a "name=value" directive — redirect= or exp=.
type Modifier struct {
	Name  string
	Value string
}

// Record is a parsed v=spf1 record.
type Record struct {
	Mechanisms []Mechanism
	Modifiers  []Modifier
}

// ParseRecord parses an SPF record starting with "v=spf1". Whitespace
// between terms is collapsed per RFC 7208 §4.5. Non-v=spf1 strings are
// rejected with ErrMalformedRecord.
func ParseRecord(s string) (*Record, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%w: empty", ErrMalformedRecord)
	}
	parts := strings.Fields(s)
	if len(parts) == 0 || !strings.EqualFold(parts[0], "v=spf1") {
		return nil, fmt.Errorf("%w: missing v=spf1", ErrMalformedRecord)
	}
	rec := &Record{}
	for _, p := range parts[1:] {
		if strings.ContainsAny(p, "=") && !isMechanismWithArg(p) {
			// Modifier: name=value, where name is not a mechanism.
			k, v, ok := strings.Cut(p, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("%w: bad modifier %q", ErrMalformedRecord, p)
			}
			rec.Modifiers = append(rec.Modifiers, Modifier{Name: strings.ToLower(k), Value: v})
			continue
		}
		mech, err := parseMechanism(p)
		if err != nil {
			return nil, err
		}
		rec.Mechanisms = append(rec.Mechanisms, mech)
	}
	return rec, nil
}

// isMechanismWithArg reports whether p looks like a mechanism that
// includes an ":" argument (ip4:/ ip6:/ include:/ a:/ mx:/ exists:). Such
// terms contain "=" only via domain macros, which we treat as mechanism
// arguments rather than modifiers.
func isMechanismWithArg(p string) bool {
	// Strip the qualifier prefix for the scan.
	if len(p) > 0 {
		switch Qualifier(p[0]) {
		case QualifierPass, QualifierFail, QualifierSoftFail, QualifierNeutral:
			p = p[1:]
		}
	}
	name := p
	if i := strings.IndexAny(p, ":/"); i >= 0 {
		name = p[:i]
	}
	switch MechanismName(strings.ToLower(name)) {
	case MechanismAll, MechanismInclude, MechanismA, MechanismMX,
		MechanismPTR, MechanismIP4, MechanismIP6, MechanismExists:
		return true
	}
	return false
}

func parseMechanism(p string) (Mechanism, error) {
	var m Mechanism
	if p == "" {
		return m, fmt.Errorf("%w: empty mechanism", ErrMalformedRecord)
	}
	m.Qualifier = QualifierPass
	if q := Qualifier(p[0]); q == QualifierPass || q == QualifierFail || q == QualifierSoftFail || q == QualifierNeutral {
		m.Qualifier = q
		p = p[1:]
	}
	// Split on the first ':' or '/' — ':' introduces a domain/IP arg,
	// '/' introduces the cidr-length.
	name, rest := p, ""
	if i := strings.IndexAny(p, ":/"); i >= 0 {
		name, rest = p[:i], p[i:]
	}
	m.Name = MechanismName(strings.ToLower(name))
	switch m.Name {
	case MechanismAll:
		if rest != "" {
			return m, fmt.Errorf("%w: all takes no argument", ErrMalformedRecord)
		}
	case MechanismIP4, MechanismIP6:
		if !strings.HasPrefix(rest, ":") {
			return m, fmt.Errorf("%w: %s requires :ip", ErrMalformedRecord, m.Name)
		}
		ipStr := rest[1:]
		if !strings.Contains(ipStr, "/") {
			if m.Name == MechanismIP4 {
				ipStr += "/32"
			} else {
				ipStr += "/128"
			}
		}
		_, n, err := net.ParseCIDR(ipStr)
		if err != nil {
			return m, fmt.Errorf("%w: bad CIDR %q: %v", ErrMalformedRecord, rest[1:], err)
		}
		// Reject family mismatches.
		if m.Name == MechanismIP4 && n.IP.To4() == nil {
			return m, fmt.Errorf("%w: ip4 expects IPv4 %q", ErrMalformedRecord, rest[1:])
		}
		if m.Name == MechanismIP6 && n.IP.To4() != nil {
			return m, fmt.Errorf("%w: ip6 expects IPv6 %q", ErrMalformedRecord, rest[1:])
		}
		m.IP = n
	case MechanismA, MechanismMX:
		// Optional ":domain" then optional "/cidr4" then optional "//cidr6".
		m.Prefix4, m.Prefix6 = 32, 128
		if strings.HasPrefix(rest, ":") {
			rest = rest[1:]
			if i := strings.Index(rest, "/"); i >= 0 {
				m.Domain, rest = rest[:i], rest[i:]
			} else {
				m.Domain, rest = rest, ""
			}
		}
		if strings.HasPrefix(rest, "//") {
			// ip6 prefix only
			p6, err := strconv.Atoi(rest[2:])
			if err != nil || p6 < 0 || p6 > 128 {
				return m, fmt.Errorf("%w: bad ip6 prefix %q", ErrMalformedRecord, rest)
			}
			m.Prefix6 = p6
		} else if strings.HasPrefix(rest, "/") {
			// ip4 prefix, optionally followed by //ip6 prefix.
			tail := rest[1:]
			var ip4Part, ip6Part string
			if i := strings.Index(tail, "//"); i >= 0 {
				ip4Part, ip6Part = tail[:i], tail[i+2:]
			} else {
				ip4Part = tail
			}
			if ip4Part != "" {
				p4, err := strconv.Atoi(ip4Part)
				if err != nil || p4 < 0 || p4 > 32 {
					return m, fmt.Errorf("%w: bad ip4 prefix %q", ErrMalformedRecord, ip4Part)
				}
				m.Prefix4 = p4
			}
			if ip6Part != "" {
				p6, err := strconv.Atoi(ip6Part)
				if err != nil || p6 < 0 || p6 > 128 {
					return m, fmt.Errorf("%w: bad ip6 prefix %q", ErrMalformedRecord, ip6Part)
				}
				m.Prefix6 = p6
			}
		} else if rest != "" {
			return m, fmt.Errorf("%w: trailing junk on %s: %q", ErrMalformedRecord, m.Name, rest)
		}
	case MechanismInclude, MechanismExists, MechanismPTR:
		if strings.HasPrefix(rest, ":") {
			m.Domain = rest[1:]
		} else if rest != "" {
			return m, fmt.Errorf("%w: %s takes only :domain", ErrMalformedRecord, m.Name)
		}
		if m.Name != MechanismPTR && m.Domain == "" {
			return m, fmt.Errorf("%w: %s requires :domain", ErrMalformedRecord, m.Name)
		}
	default:
		return m, fmt.Errorf("%w: unknown mechanism %q", ErrMalformedRecord, name)
	}
	return m, nil
}
