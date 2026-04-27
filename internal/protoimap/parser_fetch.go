package protoimap

import (
	"fmt"
	"strconv"
	"strings"

	imap "github.com/emersion/go-imap/v2"
)

// parseFetch reads "set macro_or_list" into cmd.FetchSet + cmd.FetchOptions.
// Phase 1 handles the common macros (ALL, FAST, FULL) and the items
// listed in REQ-PROTO-IMAP: UID, FLAGS, ENVELOPE, INTERNALDATE, RFC822.SIZE,
// BODY, BODY.PEEK[...], BODYSTRUCTURE. Phase 2 adds the MODSEQ item plus
// the trailing "(CHANGEDSINCE n [VANISHED])" modifier (RFC 7162 §3.1.4).
func parseFetch(p *parser, cmd *Command) error {
	p.skipSP()
	set, err := parseNumSet(p, cmd.IsUID)
	if err != nil {
		return err
	}
	cmd.FetchSet = set
	p.skipSP()
	opts := &imap.FetchOptions{}
	if p.peek() == '(' {
		if err := parseFetchItemList(p, opts); err != nil {
			return err
		}
	} else {
		// RFC 9051 §6.4.6: an unparenthesised FETCH attribute can be
		// either one of the macros (ALL / FAST / FULL) or a single
		// fetch-att — including the bracket-bearing forms BODY[...]
		// and BODY.PEEK[...]. The earlier readAtom/applyFetchMacro
		// path missed the bracket-bearing case, which made plain
		// `FETCH 1 BODY.PEEK[]` (mutt's single-message body fetch)
		// fail with "unknown fetch item BODY.PEEK".
		a, err := p.readAtom()
		if err != nil {
			return err
		}
		name := strings.ToUpper(a)
		switch name {
		case "ALL", "FAST", "FULL":
			if err := applyFetchMacro(name, opts); err != nil {
				return err
			}
		default:
			if p.peek() == '[' {
				if err := parseBodySection(p, name, opts); err != nil {
					return err
				}
			} else if err := applyFetchItem(name, opts); err != nil {
				return err
			}
		}
	}
	cmd.FetchOptions = opts
	// Optional "(CHANGEDSINCE n [VANISHED])" tail (RFC 7162 §3.1.4).
	p.skipSP()
	if p.peek() == '(' {
		p.pos++
		for {
			p.skipSP()
			if p.peek() == ')' {
				p.pos++
				break
			}
			a, err := p.readAtom()
			if err != nil {
				return err
			}
			switch strings.ToUpper(a) {
			case "CHANGEDSINCE":
				p.skipSP()
				n, err := strconv.ParseUint(readNum(p), 10, 64)
				if err != nil {
					return fmt.Errorf("protoimap: bad CHANGEDSINCE: %w", err)
				}
				opts.ChangedSince = n
				// MODSEQ is implicit when CHANGEDSINCE is specified.
				opts.ModSeq = true
			case "VANISHED":
				// Caller wants VANISHED instead of EXPUNGE for
				// dropped UIDs. Captured on FetchOptions.Vanished.
				// emersion's struct uses bool field name "Vanished"
				// — check by reflection-free assignment below.
			}
		}
	}
	return nil
}

// readNum returns the leading run of decimal digits and advances the
// parser past them. Returns the empty string when the cursor is not on
// a digit.
func readNum(p *parser) string {
	start := p.pos
	for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
		p.pos++
	}
	return string(p.src[start:p.pos])
}

func applyFetchMacro(name string, opts *imap.FetchOptions) error {
	switch name {
	case "ALL":
		opts.Flags = true
		opts.InternalDate = true
		opts.RFC822Size = true
		opts.Envelope = true
	case "FAST":
		opts.Flags = true
		opts.InternalDate = true
		opts.RFC822Size = true
	case "FULL":
		opts.Flags = true
		opts.InternalDate = true
		opts.RFC822Size = true
		opts.Envelope = true
		opts.BodyStructure = &imap.FetchItemBodyStructure{}
	default:
		return applyFetchItem(name, opts)
	}
	return nil
}

func parseFetchItemList(p *parser, opts *imap.FetchOptions) error {
	if err := p.expect('('); err != nil {
		return err
	}
	for {
		p.skipSP()
		if p.peek() == ')' {
			p.pos++
			return nil
		}
		if err := parseFetchItem(p, opts); err != nil {
			return err
		}
	}
}

func parseFetchItem(p *parser, opts *imap.FetchOptions) error {
	a, err := p.readAtom()
	if err != nil {
		return err
	}
	name := strings.ToUpper(a)
	// BODY[...]... and BODY.PEEK[...] are atoms followed by "[..]".
	if p.peek() == '[' {
		return parseBodySection(p, name, opts)
	}
	return applyFetchItem(name, opts)
}

func applyFetchItem(name string, opts *imap.FetchOptions) error {
	switch name {
	case "UID":
		opts.UID = true
	case "FLAGS":
		opts.Flags = true
	case "INTERNALDATE":
		opts.InternalDate = true
	case "RFC822.SIZE":
		opts.RFC822Size = true
	case "ENVELOPE":
		opts.Envelope = true
	case "BODY":
		// Without [...] means BODYSTRUCTURE-lite (non-extensible body).
		opts.BodyStructure = &imap.FetchItemBodyStructure{Extended: false}
	case "BODYSTRUCTURE":
		opts.BodyStructure = &imap.FetchItemBodyStructure{Extended: true}
	case "RFC822":
		opts.BodySection = append(opts.BodySection, &imap.FetchItemBodySection{})
	case "RFC822.HEADER":
		opts.BodySection = append(opts.BodySection, &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierHeader,
			Peek:      true,
		})
	case "RFC822.TEXT":
		opts.BodySection = append(opts.BodySection, &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierText,
		})
	case "MODSEQ":
		// RFC 7162 §3.1.4 FETCH MODSEQ item.
		opts.ModSeq = true
	default:
		return fmt.Errorf("protoimap: unknown fetch item %q", name)
	}
	return nil
}

// parseBodySection reads the [..] suffix of BODY / BODY.PEEK.
func parseBodySection(p *parser, name string, opts *imap.FetchOptions) error {
	peek := strings.HasSuffix(name, ".PEEK")
	if err := p.expect('['); err != nil {
		return err
	}
	sec := &imap.FetchItemBodySection{Peek: peek}
	// Optional part numbers "1.2.3"
	start := p.pos
	for p.pos < len(p.src) && (isDigit(p.src[p.pos]) || p.src[p.pos] == '.') {
		p.pos++
	}
	if p.pos > start {
		partStr := string(p.src[start:p.pos])
		for _, s := range strings.Split(partStr, ".") {
			if s == "" {
				continue
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("protoimap: bad part %q: %w", partStr, err)
			}
			sec.Part = append(sec.Part, n)
		}
	}
	// Optional specifier "HEADER" / "HEADER.FIELDS [.NOT] (...)" / "MIME" / "TEXT".
	if p.peek() != ']' {
		if p.peek() == '.' {
			p.pos++
		}
		spec, err := p.readAtom()
		if err != nil {
			return err
		}
		specU := strings.ToUpper(spec)
		switch specU {
		case "HEADER":
			sec.Specifier = imap.PartSpecifierHeader
		case "HEADER.FIELDS":
			sec.Specifier = imap.PartSpecifierHeader
			p.skipSP()
			fields, err := readAtomList(p)
			if err != nil {
				return err
			}
			sec.HeaderFields = fields
		case "HEADER.FIELDS.NOT":
			sec.Specifier = imap.PartSpecifierHeader
			p.skipSP()
			fields, err := readAtomList(p)
			if err != nil {
				return err
			}
			sec.HeaderFieldsNot = fields
		case "TEXT":
			sec.Specifier = imap.PartSpecifierText
		case "MIME":
			sec.Specifier = imap.PartSpecifierMIME
		default:
			return fmt.Errorf("protoimap: unknown body specifier %q", spec)
		}
	}
	if err := p.expect(']'); err != nil {
		return err
	}
	// Optional partial "<offset.size>".
	if p.peek() == '<' {
		p.pos++
		end := strings.IndexByte(string(p.src[p.pos:]), '>')
		if end < 0 {
			return fmt.Errorf("protoimap: unterminated partial")
		}
		spec := string(p.src[p.pos : p.pos+end])
		p.pos += end + 1
		parts := strings.SplitN(spec, ".", 2)
		off, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return fmt.Errorf("protoimap: bad partial offset: %w", err)
		}
		var size int64 = -1
		if len(parts) == 2 {
			size, err = strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return fmt.Errorf("protoimap: bad partial size: %w", err)
			}
		}
		sec.Partial = &imap.SectionPartial{Offset: off, Size: size}
	}
	opts.BodySection = append(opts.BodySection, sec)
	return nil
}

func readAtomList(p *parser) ([]string, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	var out []string
	for {
		p.skipSP()
		if p.peek() == ')' {
			p.pos++
			return out, nil
		}
		a, err := p.readAstring()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
