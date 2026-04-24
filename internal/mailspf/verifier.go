package mailspf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
)

// DefaultLookupLimit is RFC 7208 §4.6.4's hard cap on DNS lookups in a
// single SPF evaluation. Exceeding it is a PermError.
const DefaultLookupLimit = 10

// Verifier performs RFC 7208 SPF evaluation. It is safe for concurrent
// use; dependencies are read-only.
type Verifier struct {
	resolver mailauth.Resolver
	clock    clock.Clock
	// limit is the maximum DNS lookup budget per evaluation. Zero means
	// DefaultLookupLimit.
	limit int
}

// Option customises a Verifier.
type Option func(*Verifier)

// WithLookupLimit overrides DefaultLookupLimit for this Verifier. A value
// <= 0 restores the default.
func WithLookupLimit(n int) Option {
	return func(v *Verifier) {
		if n > 0 {
			v.limit = n
		}
	}
}

// New returns a Verifier using resolver for DNS lookups.
func New(resolver mailauth.Resolver, clk clock.Clock, opts ...Option) *Verifier {
	if resolver == nil {
		panic("mailspf: nil resolver")
	}
	if clk == nil {
		panic("mailspf: nil clock")
	}
	v := &Verifier{resolver: resolver, clock: clk, limit: DefaultLookupLimit}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Check evaluates SPF per RFC 7208 against the given mailFrom and helo
// identities. ClientIP is the peer's IP at the time of the SMTP session.
//
// When mailFrom is empty (null reverse-path used for bounces), the HELO
// identity is checked instead — per RFC 7208 §2.4.
//
// A non-nil error indicates an internal failure (e.g. context cancel).
// Ordinary SPF outcomes (pass/fail/softfail/neutral/temperror/permerror)
// are returned in the SPFResult.Status field with a nil error.
func (v *Verifier) Check(ctx context.Context, mailFrom, helo, clientIP string) (mailauth.SPFResult, error) {
	if err := ctx.Err(); err != nil {
		return mailauth.SPFResult{}, err
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return mailauth.SPFResult{
			Status:   mailauth.AuthPermError,
			From:     mailFrom,
			HELO:     helo,
			ClientIP: clientIP,
			Reason:   "client IP not parseable",
		}, nil
	}

	domain := senderDomain(mailFrom)
	from := mailFrom
	if domain == "" {
		// Null return-path: use HELO identity.
		domain = helo
		from = "postmaster@" + helo
	}
	if domain == "" {
		return mailauth.SPFResult{
			Status:   mailauth.AuthNone,
			From:     mailFrom,
			HELO:     helo,
			ClientIP: clientIP,
			Reason:   "no sender domain",
		}, nil
	}

	state := &evalState{
		verifier: v,
		ctx:      ctx,
		ip:       ip,
		sender:   from,
		limit:    v.limit,
	}
	status, reason := state.check(domain)
	return mailauth.SPFResult{
		Status:   status,
		From:     from,
		HELO:     helo,
		ClientIP: clientIP,
		Reason:   reason,
	}, nil
}

// senderDomain returns the domain portion of an RFC 5321 MAIL FROM
// address. An empty input yields an empty string.
func senderDomain(addr string) string {
	addr = strings.Trim(addr, "<>")
	if addr == "" {
		return ""
	}
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

// evalState holds the per-evaluation mutable state: DNS budget, depth
// stack for include/redirect, and the identity being checked.
type evalState struct {
	verifier *Verifier
	ctx      context.Context
	ip       net.IP
	sender   string
	limit    int
	// Track include/redirect recursion to detect loops.
	stack []string
}

// check evaluates domain's SPF record. It returns the terminal status and
// a short reason. Internal recursion shares budget through state.limit.
func (s *evalState) check(domain string) (mailauth.AuthStatus, string) {
	for _, d := range s.stack {
		if strings.EqualFold(d, domain) {
			return mailauth.AuthPermError, "include/redirect loop"
		}
	}
	s.stack = append(s.stack, domain)
	defer func() { s.stack = s.stack[:len(s.stack)-1] }()

	if !s.consumeLookup() {
		return mailauth.AuthPermError, "DNS lookup limit exceeded"
	}
	txts, err := s.verifier.resolver.TXTLookup(s.ctx, domain)
	if err != nil {
		if errors.Is(err, mailauth.ErrNoRecords) {
			return mailauth.AuthNone, "no SPF record"
		}
		if mailauth.IsTemporary(err) {
			return mailauth.AuthTempError, "TXT lookup: " + err.Error()
		}
		return mailauth.AuthPermError, "TXT lookup: " + err.Error()
	}

	var rec *Record
	var count int
	for _, t := range txts {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=spf1") {
			continue
		}
		count++
		r, perr := ParseRecord(t)
		if perr != nil {
			return mailauth.AuthPermError, perr.Error()
		}
		rec = r
	}
	if count == 0 {
		return mailauth.AuthNone, "no v=spf1 record"
	}
	if count > 1 {
		return mailauth.AuthPermError, "multiple v=spf1 records"
	}
	return s.eval(domain, rec)
}

// eval walks the mechanisms in order, then applies redirect= if nothing
// matched.
func (s *evalState) eval(domain string, rec *Record) (mailauth.AuthStatus, string) {
	for _, m := range rec.Mechanisms {
		matched, status, reason := s.matchMechanism(domain, m)
		if matched {
			return status, reason
		}
		// Non-matching mechanisms with internal errors short-circuit.
		if status == mailauth.AuthTempError || status == mailauth.AuthPermError {
			return status, reason
		}
	}
	// No mechanism matched; apply redirect= if present.
	for _, mod := range rec.Modifiers {
		if mod.Name == "redirect" {
			return s.check(expandDomain(mod.Value, domain, s.sender))
		}
	}
	// RFC 7208 §4.7: no mechanism matched and no redirect — result is
	// Neutral.
	return mailauth.AuthNeutral, "no mechanism matched"
}

// matchMechanism evaluates one mechanism and returns (matched, status,
// reason). status is relevant only when matched is true (or when a
// terminal error occurred).
func (s *evalState) matchMechanism(domain string, m Mechanism) (bool, mailauth.AuthStatus, string) {
	switch m.Name {
	case MechanismAll:
		return true, qualifierToStatus(m.Qualifier), "all"
	case MechanismIP4, MechanismIP6:
		if m.IP != nil && m.IP.Contains(s.ip) {
			return true, qualifierToStatus(m.Qualifier), string(m.Name)
		}
		return false, 0, ""
	case MechanismA:
		target := m.Domain
		if target == "" {
			target = domain
		}
		target = expandDomain(target, domain, s.sender)
		if !s.consumeLookup() {
			return true, mailauth.AuthPermError, "DNS lookup limit exceeded"
		}
		ips, err := s.verifier.resolver.IPLookup(s.ctx, target)
		if err != nil {
			if errors.Is(err, mailauth.ErrNoRecords) {
				return false, 0, ""
			}
			if mailauth.IsTemporary(err) {
				return true, mailauth.AuthTempError, "A/AAAA lookup: " + err.Error()
			}
			return false, 0, ""
		}
		if matchIPs(s.ip, ips, m.Prefix4, m.Prefix6) {
			return true, qualifierToStatus(m.Qualifier), "a"
		}
		return false, 0, ""
	case MechanismMX:
		target := m.Domain
		if target == "" {
			target = domain
		}
		target = expandDomain(target, domain, s.sender)
		if !s.consumeLookup() {
			return true, mailauth.AuthPermError, "DNS lookup limit exceeded"
		}
		mxs, err := s.verifier.resolver.MXLookup(s.ctx, target)
		if err != nil {
			if errors.Is(err, mailauth.ErrNoRecords) {
				return false, 0, ""
			}
			if mailauth.IsTemporary(err) {
				return true, mailauth.AuthTempError, "MX lookup: " + err.Error()
			}
			return false, 0, ""
		}
		// RFC 7208 §4.6.4: each MX target counts against the lookup cap.
		for _, mx := range mxs {
			if !s.consumeLookup() {
				return true, mailauth.AuthPermError, "DNS lookup limit exceeded"
			}
			ips, err := s.verifier.resolver.IPLookup(s.ctx, strings.TrimSuffix(mx.Host, "."))
			if err != nil {
				if mailauth.IsTemporary(err) {
					return true, mailauth.AuthTempError, "MX target lookup: " + err.Error()
				}
				continue
			}
			if matchIPs(s.ip, ips, m.Prefix4, m.Prefix6) {
				return true, qualifierToStatus(m.Qualifier), "mx"
			}
		}
		return false, 0, ""
	case MechanismInclude:
		target := expandDomain(m.Domain, domain, s.sender)
		// RFC 7208 §5.2: an include's result depends on the recursive
		// check; pass/fail/softfail/neutral of the include map to the
		// outer result as "match" with the including mechanism's
		// qualifier only when the include result is Pass.
		sub, subReason := s.check(target)
		switch sub {
		case mailauth.AuthPass:
			return true, qualifierToStatus(m.Qualifier), "include:" + target
		case mailauth.AuthFail, mailauth.AuthSoftFail, mailauth.AuthNeutral:
			return false, 0, ""
		case mailauth.AuthTempError:
			return true, mailauth.AuthTempError, "include:" + target + ": " + subReason
		case mailauth.AuthPermError, mailauth.AuthNone:
			return true, mailauth.AuthPermError, "include:" + target + ": " + subReason
		}
		return false, 0, ""
	case MechanismExists:
		target := expandDomain(m.Domain, domain, s.sender)
		if !s.consumeLookup() {
			return true, mailauth.AuthPermError, "DNS lookup limit exceeded"
		}
		_, err := s.verifier.resolver.IPLookup(s.ctx, target)
		if err != nil {
			if errors.Is(err, mailauth.ErrNoRecords) {
				return false, 0, ""
			}
			if mailauth.IsTemporary(err) {
				return true, mailauth.AuthTempError, "exists lookup: " + err.Error()
			}
			return false, 0, ""
		}
		return true, qualifierToStatus(m.Qualifier), "exists:" + target
	case MechanismPTR:
		// RFC 7208 §5.5 deprecates ptr. We treat it as "does not match"
		// (never pass) and do not charge a DNS lookup: RFC 7208 §4.6.4
		// allows implementations to skip ptr evaluation.
		return false, 0, ""
	default:
		return true, mailauth.AuthPermError, "unknown mechanism: " + string(m.Name)
	}
}

// consumeLookup decrements the DNS budget and returns true while the
// budget still has room.
func (s *evalState) consumeLookup() bool {
	if s.limit <= 0 {
		return false
	}
	s.limit--
	return true
}

// matchIPs reports whether ip falls inside any of addrs masked by the
// mechanism's prefix for the relevant family.
func matchIPs(ip net.IP, addrs []net.IP, p4, p6 int) bool {
	ipIs4 := ip.To4() != nil
	for _, a := range addrs {
		aIs4 := a.To4() != nil
		if ipIs4 != aIs4 {
			continue
		}
		bits := p6
		addr := a.To16()
		probe := ip.To16()
		if aIs4 {
			bits = p4
			addr = a.To4()
			probe = ip.To4()
		}
		if bits == 0 {
			return true
		}
		mask := net.CIDRMask(bits, len(addr)*8)
		if addr.Mask(mask).Equal(probe.Mask(mask)) {
			return true
		}
	}
	return false
}

// qualifierToStatus maps a mechanism qualifier to the terminal SPF
// result assigned when the mechanism matches.
func qualifierToStatus(q Qualifier) mailauth.AuthStatus {
	switch q {
	case QualifierPass:
		return mailauth.AuthPass
	case QualifierFail:
		return mailauth.AuthFail
	case QualifierSoftFail:
		return mailauth.AuthSoftFail
	case QualifierNeutral:
		return mailauth.AuthNeutral
	default:
		return mailauth.AuthPass
	}
}

// expandDomain applies the minimal macro substitution required by the
// mechanisms we support: %{d} = current domain; %{s} = sender; %{o} =
// sender domain. Other macros (RFC 7208 §7) are passed through; records
// that rely on exotic macros are rare and will evaluate to a literal
// lookup that returns nothing, yielding a non-match.
func expandDomain(spec, domain, sender string) string {
	if !strings.Contains(spec, "%{") {
		return spec
	}
	var b strings.Builder
	b.Grow(len(spec))
	for i := 0; i < len(spec); i++ {
		if spec[i] == '%' && i+1 < len(spec) {
			switch spec[i+1] {
			case '%':
				b.WriteByte('%')
				i++
				continue
			case '_':
				b.WriteByte(' ')
				i++
				continue
			case '-':
				b.WriteString("%20")
				i++
				continue
			case '{':
				end := strings.Index(spec[i:], "}")
				if end < 0 {
					b.WriteByte(spec[i])
					continue
				}
				tok := spec[i+2 : i+end]
				b.WriteString(macroValue(tok, domain, sender))
				i += end
				continue
			}
		}
		b.WriteByte(spec[i])
	}
	return b.String()
}

func macroValue(tok, domain, sender string) string {
	if tok == "" {
		return ""
	}
	switch tok[0] {
	case 'd':
		return domain
	case 's':
		return sender
	case 'o':
		return senderDomain(sender)
	case 'l':
		if i := strings.LastIndex(sender, "@"); i > 0 {
			return sender[:i]
		}
		return sender
	}
	return fmt.Sprintf("%%{%s}", tok)
}
