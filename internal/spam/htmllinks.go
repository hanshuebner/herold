package spam

import (
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// linkSentinel delimits an anchor-host marker while the excerpt is being
// assembled: stripHTMLTags writes linkSentinel host linkSentinel, and
// renderExcerpt turns it into "[link: host]" once the message-derived
// text around it has been decoded. Every message-derived string has the
// sentinel removed, so only stripHTMLTags can produce one.
const linkSentinel = "\x01"

// maxLinkMarkers bounds how many distinct hosts get a "[link: host]"
// marker in one HTML body (each host is marked once), so a message made of thousands of anchors cannot expand
// the text beyond what the excerpt cap then truncates.
const maxLinkMarkers = 20

// maxLinkHostLen is the longest host a marker names (the DNS limit);
// longer values are cut so one anchor cannot spend the excerpt budget.
const maxLinkHostLen = 253

// linkMarkerSpelling matches the opening of a marker, tolerating
// whitespace and case, in decoded and joined text.
var linkMarkerSpelling = regexp.MustCompile(`(?i)\[\s*link\s*:`)

// hostIDNA converts a host to its ASCII form the way a browser's URL
// parser does (UTS 46 mapping, no STD3 restriction on underscores).
var hostIDNA = idna.New(idna.MapForLookup(), idna.StrictDomainName(false))

// stripLinkSentinel removes linkSentinel from message-derived text.
func stripLinkSentinel(s string) string {
	if !strings.Contains(s, linkSentinel) {
		return s
	}
	return strings.ReplaceAll(s, linkSentinel, "")
}

// renderLinkMarkers turns the sentinel-delimited hosts stripHTMLTags
// wrote into "[link: host]" markers.
func renderLinkMarkers(s string) string {
	if !strings.Contains(s, linkSentinel) {
		return s
	}
	segs := strings.Split(s, linkSentinel)
	var b strings.Builder
	b.Grow(len(s) + 8*len(segs))
	for i, seg := range segs {
		if i%2 == 1 && i < len(segs)-1 {
			b.WriteString("[link: " + seg + "]")
			continue
		}
		b.WriteString(seg)
	}
	return b.String()
}

// stripHTMLTags converts HTML to text for the classifier excerpt. It
// tokenizes with golang.org/x/net/html, so quoted attribute values
// (including a ">" inside one), upper-case tags, unclosed anchors and
// comments are handled the way a browser handles them. Text tokens are
// kept in their raw (entity-encoded) form; renderExcerpt decodes
// entities once afterwards.
//
// The destination of a call-to-action button is evidence the classifier
// needs (a "download the file" button leading somewhere unrelated to the
// sender is the signature of a document lure), so each <a> start tag
// whose href resolves, as a browser resolves it, to an http(s) host
// emits a marker ahead of the anchor text. Only the host is kept so
// tracking-heavy URLs do not consume the excerpt budget. Because the
// marker is written from the start tag it does not depend on a closing
// </a>. The marker is written in sentinel form; renderExcerpt renders it
// as "[link: host]" and rewrites any marker spelling that message
// content produced.
func stripHTMLTags(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	base := documentBase(in)
	seenHosts := map[string]bool{}
	sc := linkScanner{z: xhtml.NewTokenizer(strings.NewReader(in))}
	for {
		tt := sc.z.Next()
		switch tt {
		case xhtml.ErrorToken:
			return b.String()
		case xhtml.TextToken:
			b.WriteString(stripLinkSentinel(string(sc.z.Raw())))
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken, xhtml.EndTagToken:
			href, ok := sc.href(tt)
			if !ok || len(seenHosts) >= maxLinkMarkers {
				continue
			}
			if host := resolveLinkHost(href, base); host != "" && !seenHosts[host] {
				seenHosts[host] = true
				b.WriteString(" " + linkSentinel + host + linkSentinel + " ")
			}
		}
	}
}

// linkScanner walks an HTML token stream and reports the href of each
// element a browser follows as a link: <a href> and <area href> in HTML,
// and <a href> or <a xlink:href> inside <svg>. <noscript> content is
// parsed as markup, as a mail client without scripting renders it.
type linkScanner struct {
	z   *xhtml.Tokenizer
	svg int
}

// href inspects the tag token just returned by z.Next and returns its
// link target, if it has one.
func (s *linkScanner) href(tt xhtml.TokenType) (string, bool) {
	name, hasAttr := s.z.TagName()
	switch tt {
	case xhtml.EndTagToken:
		if string(name) == "svg" && s.svg > 0 {
			s.svg--
		}
		return "", false
	case xhtml.StartTagToken:
		switch string(name) {
		case "noscript":
			s.z.NextIsNotRawText()
		case "svg":
			s.svg++
		}
	}
	if !hasAttr {
		return "", false
	}
	switch string(name) {
	case "a":
		if s.svg > 0 {
			return tagAttr(s.z, "href", "xlink:href")
		}
		return tagAttr(s.z, "href")
	case "area":
		if s.svg == 0 {
			return tagAttr(s.z, "href")
		}
	}
	return "", false
}

// tagAttr returns the value of the first of keys (in the order given)
// present on the tag the tokenizer is positioned on; browsers ignore
// later duplicates of an attribute.
func tagAttr(z *xhtml.Tokenizer, keys ...string) (string, bool) {
	found := make([]string, len(keys))
	have := make([]bool, len(keys))
	for {
		k, v, more := z.TagAttr()
		for i, key := range keys {
			if string(k) == key && !have[i] {
				found[i], have[i] = string(v), true
			}
		}
		if !more {
			break
		}
	}
	for i := range keys {
		if have[i] {
			return found[i], true
		}
	}
	return "", false
}

// linkBase is the document base URL a <base href> establishes. web is
// true when it is an http(s) URL, the only kind relative web links can
// resolve against.
type linkBase struct {
	scheme string
	host   string
	web    bool
}

// documentBase returns the base URL of an HTML document: the first
// <base> element carrying an href decides, wherever it appears. Nil when
// there is none or it does not parse to a usable URL.
func documentBase(in string) *linkBase {
	if !strings.Contains(strings.ToLower(in), "<base") {
		return nil
	}
	z := xhtml.NewTokenizer(strings.NewReader(in))
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return nil
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if string(name) == "noscript" {
				z.NextIsNotRawText()
			}
			if string(name) != "base" || !hasAttr {
				continue
			}
			if href, ok := tagAttr(z, "href"); ok {
				return parseBase(href)
			}
		}
	}
}

// parseBase interprets a <base href> value. Non-web schemes yield a base
// no web link resolves against; an unusable value yields nil.
func parseBase(val string) *linkBase {
	s := cleanHref(val)
	scheme, rest, ok := splitScheme(s)
	if ok {
		if scheme != "http" && scheme != "https" {
			return &linkBase{scheme: scheme}
		}
		host := authorityHost(rest)
		if host == "" {
			return nil
		}
		return &linkBase{scheme: scheme, host: host, web: true}
	}
	if isSlash(s, 0) && isSlash(s, 1) {
		if host := authorityHost(s); host != "" {
			return &linkBase{host: host, web: true}
		}
	}
	return nil
}

// anchorLinkHosts calls visit with the destination host of every link
// element whose href resolves to an http(s) host, until visit returns
// false or limit links have been seen.
func anchorLinkHosts(in string, limit int, visit func(host string) bool) {
	base := documentBase(in)
	sc := linkScanner{z: xhtml.NewTokenizer(strings.NewReader(in))}
	seen := 0
	for seen < limit {
		tt := sc.z.Next()
		switch tt {
		case xhtml.ErrorToken:
			return
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken, xhtml.EndTagToken:
			href, ok := sc.href(tt)
			if !ok {
				continue
			}
			host := resolveLinkHost(href, base)
			if host == "" {
				continue
			}
			seen++
			if !visit(host) {
				return
			}
		}
	}
}

// cleanHref applies the URL parser's input preprocessing: leading and
// trailing C0 controls and spaces are trimmed, and tabs and newlines are
// removed wherever they occur.
func cleanHref(val string) string {
	val = strings.TrimFunc(val, func(r rune) bool { return r <= 0x20 })
	if !strings.ContainsAny(val, "\t\n\r") {
		return val
	}
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, val)
}

// splitScheme splits a leading "scheme:" off s (lower-cased scheme).
func splitScheme(s string) (scheme, rest string, ok bool) {
	if s == "" || !isASCIILetter(s[0]) {
		return "", s, false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case isASCIILetter(c) || (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.':
		case c == ':':
			return strings.ToLower(s[:i]), s[i+1:], true
		default:
			return "", s, false
		}
	}
	return "", s, false
}

func isASCIILetter(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }

// isSlash reports whether s[i] exists and is a slash or backslash;
// browsers treat both as path separators in http(s) URLs.
func isSlash(s string, i int) bool {
	return i < len(s) && (s[i] == '/' || s[i] == '\\')
}

// resolveLinkHost returns the lower-cased host an href leads to when a
// browser follows it, or "" when it does not lead to a web host (mailto:,
// javascript:, data:, a relative reference with no web base, or an
// unparseable value). base is the document's <base href>, or nil.
func resolveLinkHost(val string, base *linkBase) string {
	s := cleanHref(val)
	if scheme, rest, ok := splitScheme(s); ok {
		if scheme != "http" && scheme != "https" {
			return ""
		}
		// A same-scheme reference to a base without a "//" authority is
		// relative to that base ("https:x", "https:/x").
		if base != nil && base.web && base.scheme == scheme && !(isSlash(rest, 0) && isSlash(rest, 1)) {
			return base.host
		}
		return authorityHost(rest)
	}
	if isSlash(s, 0) && isSlash(s, 1) {
		if base != nil && !base.web {
			return ""
		}
		return authorityHost(s)
	}
	if base != nil && base.web {
		return base.host
	}
	return ""
}

// authorityHost extracts the host from the text following "scheme:" of a
// special-scheme URL: leading slashes and backslashes are skipped, the
// authority ends at the next slash, backslash, "?" or "#", the userinfo
// before the last "@" and the port are dropped, and the host is
// percent-decoded and converted to its ASCII form. "" when the host is
// empty or contains a character a browser rejects.
func authorityHost(rest string) string {
	i := 0
	for isSlash(rest, i) {
		i++
	}
	rest = rest[i:]
	end := strings.IndexAny(rest, "/\\?#")
	if end < 0 {
		end = len(rest)
	}
	auth := rest[:end]
	if at := strings.LastIndexByte(auth, '@'); at >= 0 {
		auth = auth[at+1:]
	}
	var host string
	if strings.HasPrefix(auth, "[") {
		rb := strings.IndexByte(auth, ']')
		if rb < 0 {
			return ""
		}
		host = strings.ToLower(auth[:rb+1])
		if net.ParseIP(host[1:rb]) == nil {
			return ""
		}
		return host
	}
	if colon := strings.IndexByte(auth, ':'); colon >= 0 {
		auth = auth[:colon]
	}
	dec, err := url.PathUnescape(auth)
	if err != nil {
		return ""
	}
	host, err = hostIDNA.ToASCII(dec)
	if err != nil {
		return ""
	}
	host = strings.TrimRight(strings.ToLower(host), ".")
	if host == "" || strings.ContainsAny(host, " #%/:<>?@[\\]^|\x7f") {
		return ""
	}
	for j := 0; j < len(host); j++ {
		if host[j] <= 0x20 {
			return ""
		}
	}
	if len(host) > maxLinkHostLen {
		host = host[:maxLinkHostLen]
	}
	return host
}

// LinkHostMismatch reports whether any web link in the message's HTML
// parts leads to a host unrelated to fromDomain (registrable-domain
// comparison). False when the From domain is unknown or the message has
// no web links. It is a fact for the classifier's judgement; legitimate
// mail routinely links to hosts of other organisations.
func LinkHostMismatch(root mailparse.Part, fromDomain string) bool {
	from := normaliseDomain(fromDomain)
	if from == "" {
		return false
	}
	mismatch := false
	var walk func(p mailparse.Part)
	walk = func(p mailparse.Part) {
		if mismatch {
			return
		}
		if strings.HasPrefix(strings.ToLower(p.ContentType), "text/html") {
			anchorLinkHosts(p.Text, math.MaxInt, func(host string) bool {
				if !domainsRelated(host, from) {
					mismatch = true
					return false
				}
				return true
			})
		}
		for _, c := range p.Children {
			walk(c)
		}
	}
	walk(root)
	return mismatch
}

// normaliseDomain lower-cases d and drops a trailing dot.
func normaliseDomain(d string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(d)), ".")
}

// domainsRelated reports whether host and from share a registrable
// domain (eTLD+1). IP-literal hosts are related only to themselves.
func domainsRelated(host, from string) bool {
	if host == from {
		return true
	}
	if strings.HasPrefix(host, "[") || net.ParseIP(host) != nil {
		return false
	}
	h, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return false
	}
	f, err := publicsuffix.EffectiveTLDPlusOne(from)
	if err != nil {
		return false
	}
	return h == f
}
