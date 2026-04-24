package protoimap

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"
)

// parseStore handles "set [(UNCHANGEDSINCE n)] op (flags)" where op is
// FLAGS / +FLAGS / -FLAGS (optionally .SILENT).
func parseStore(p *parser, cmd *Command) error {
	p.skipSP()
	set, err := parseNumSet(p, cmd.IsUID)
	if err != nil {
		return err
	}
	cmd.StoreSet = set
	p.skipSP()
	// Optional "(UNCHANGEDSINCE n)" block.
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
			p.skipSP()
			if strings.EqualFold(a, "UNCHANGEDSINCE") {
				n, err := strconv.ParseUint(mustReadNum(p), 10, 64)
				if err != nil {
					return fmt.Errorf("protoimap: bad UNCHANGEDSINCE: %w", err)
				}
				cmd.StoreOptions.UnchangedSince = n
			}
		}
		p.skipSP()
	}
	op, err := p.readAtom()
	if err != nil {
		return err
	}
	opU := strings.ToUpper(op)
	silent := strings.HasSuffix(opU, ".SILENT")
	if silent {
		opU = strings.TrimSuffix(opU, ".SILENT")
	}
	switch opU {
	case "FLAGS":
		cmd.StoreFlags.Op = imap.StoreFlagsSet
	case "+FLAGS":
		cmd.StoreFlags.Op = imap.StoreFlagsAdd
	case "-FLAGS":
		cmd.StoreFlags.Op = imap.StoreFlagsDel
	default:
		return fmt.Errorf("protoimap: unknown STORE op %q", op)
	}
	cmd.StoreFlags.Silent = silent
	p.skipSP()
	flagNames, err := parseFlagList(p)
	if err != nil {
		return err
	}
	for _, f := range flagNames {
		cmd.StoreFlags.Flags = append(cmd.StoreFlags.Flags, imap.Flag(f))
	}
	return nil
}

func mustReadNum(p *parser) string {
	start := p.pos
	for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
		p.pos++
	}
	return string(p.src[start:p.pos])
}

// parseSearch parses the SEARCH / UID SEARCH command criteria.
func parseSearch(p *parser, cmd *Command) error {
	p.skipSP()
	opts := &imap.SearchOptions{}
	// Optional RETURN (...) prefix per RFC 4731.
	if p.peek() == 'R' || p.peek() == 'r' {
		save := p.pos
		word, _ := p.readAtom()
		if strings.EqualFold(word, "RETURN") {
			p.skipSP()
			if err := p.expect('('); err != nil {
				return err
			}
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
				case "MIN":
					opts.ReturnMin = true
				case "MAX":
					opts.ReturnMax = true
				case "ALL":
					opts.ReturnAll = true
				case "COUNT":
					opts.ReturnCount = true
				case "SAVE":
					opts.ReturnSave = true
				}
			}
			p.skipSP()
		} else {
			p.pos = save
		}
	}
	// Optional CHARSET "utf-8" prefix.
	if p.peek() == 'C' || p.peek() == 'c' {
		save := p.pos
		word, _ := p.readAtom()
		if strings.EqualFold(word, "CHARSET") {
			p.skipSP()
			cs, err := p.readAstring()
			if err != nil {
				return err
			}
			cmd.SearchCharset = cs
			p.skipSP()
		} else {
			p.pos = save
		}
	}
	crit, err := parseSearchCriteriaList(p)
	if err != nil {
		return err
	}
	cmd.SearchCriteria = crit
	cmd.SearchOptions = opts
	return nil
}

// parseSearchCriteriaList reads a sequence of top-level criteria tokens
// and ANDs them together. Supports parenthesised groups, NOT, OR, and the
// Phase 1 predicate subset listed in the package doc.
func parseSearchCriteriaList(p *parser) (*imap.SearchCriteria, error) {
	out := &imap.SearchCriteria{}
	for {
		p.skipSP()
		if p.eof() {
			return out, nil
		}
		one, err := parseSearchTerm(p)
		if err != nil {
			return nil, err
		}
		out.And(one)
	}
}

func parseSearchTerm(p *parser) (*imap.SearchCriteria, error) {
	if p.peek() == '(' {
		p.pos++
		out := &imap.SearchCriteria{}
		for {
			p.skipSP()
			if p.peek() == ')' {
				p.pos++
				return out, nil
			}
			if p.eof() {
				return nil, fmt.Errorf("protoimap: unterminated criteria group")
			}
			inner, err := parseSearchTerm(p)
			if err != nil {
				return nil, err
			}
			out.And(inner)
		}
	}
	tok, err := p.readAtom()
	if err != nil {
		return nil, err
	}
	upper := strings.ToUpper(tok)
	crit := &imap.SearchCriteria{}

	switch upper {
	case "ALL":
		return crit, nil
	case "ANSWERED":
		crit.Flag = []imap.Flag{imap.FlagAnswered}
	case "DELETED":
		crit.Flag = []imap.Flag{imap.FlagDeleted}
	case "FLAGGED":
		crit.Flag = []imap.Flag{imap.FlagFlagged}
	case "DRAFT":
		crit.Flag = []imap.Flag{imap.FlagDraft}
	case "SEEN":
		crit.Flag = []imap.Flag{imap.FlagSeen}
	case "UNANSWERED":
		crit.NotFlag = []imap.Flag{imap.FlagAnswered}
	case "UNDELETED":
		crit.NotFlag = []imap.Flag{imap.FlagDeleted}
	case "UNFLAGGED":
		crit.NotFlag = []imap.Flag{imap.FlagFlagged}
	case "UNDRAFT":
		crit.NotFlag = []imap.Flag{imap.FlagDraft}
	case "UNSEEN":
		crit.NotFlag = []imap.Flag{imap.FlagSeen}
	case "NEW":
		crit.Flag = []imap.Flag{"\\Recent"}
		crit.NotFlag = []imap.Flag{imap.FlagSeen}
	case "OLD":
		crit.NotFlag = []imap.Flag{"\\Recent"}
	case "RECENT":
		crit.Flag = []imap.Flag{"\\Recent"}

	case "KEYWORD":
		p.skipSP()
		a, err := p.readAstring()
		if err != nil {
			return nil, err
		}
		crit.Flag = []imap.Flag{imap.Flag(a)}
	case "UNKEYWORD":
		p.skipSP()
		a, err := p.readAstring()
		if err != nil {
			return nil, err
		}
		crit.NotFlag = []imap.Flag{imap.Flag(a)}

	case "BCC":
		return parseSearchHeader(p, "Bcc")
	case "CC":
		return parseSearchHeader(p, "Cc")
	case "FROM":
		return parseSearchHeader(p, "From")
	case "TO":
		return parseSearchHeader(p, "To")
	case "SUBJECT":
		p.skipSP()
		s, err := p.readAstring()
		if err != nil {
			return nil, err
		}
		crit.Header = []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: s}}
	case "HEADER":
		p.skipSP()
		k, err := p.readAstring()
		if err != nil {
			return nil, err
		}
		p.skipSP()
		v, err := p.readAstring()
		if err != nil {
			return nil, err
		}
		crit.Header = []imap.SearchCriteriaHeaderField{{Key: k, Value: v}}
	case "BODY":
		p.skipSP()
		s, err := p.readAstring()
		if err != nil {
			return nil, err
		}
		crit.Body = []string{s}
	case "TEXT":
		p.skipSP()
		s, err := p.readAstring()
		if err != nil {
			return nil, err
		}
		crit.Text = []string{s}

	case "LARGER":
		p.skipSP()
		n, err := strconv.ParseInt(mustReadNum(p), 10, 64)
		if err != nil {
			return nil, err
		}
		crit.Larger = n
	case "SMALLER":
		p.skipSP()
		n, err := strconv.ParseInt(mustReadNum(p), 10, 64)
		if err != nil {
			return nil, err
		}
		crit.Smaller = n

	case "SINCE":
		p.skipSP()
		t, err := readSearchDate(p)
		if err != nil {
			return nil, err
		}
		crit.Since = t
	case "BEFORE":
		p.skipSP()
		t, err := readSearchDate(p)
		if err != nil {
			return nil, err
		}
		crit.Before = t
	case "ON":
		p.skipSP()
		t, err := readSearchDate(p)
		if err != nil {
			return nil, err
		}
		crit.Since = t
		crit.Before = t.Add(24 * time.Hour)
	case "SENTSINCE":
		p.skipSP()
		t, err := readSearchDate(p)
		if err != nil {
			return nil, err
		}
		crit.SentSince = t
	case "SENTBEFORE":
		p.skipSP()
		t, err := readSearchDate(p)
		if err != nil {
			return nil, err
		}
		crit.SentBefore = t

	case "UID":
		p.skipSP()
		set, err := parseNumSet(p, true)
		if err != nil {
			return nil, err
		}
		if u, ok := set.(imap.UIDSet); ok {
			crit.UID = []imap.UIDSet{u}
		}

	case "NOT":
		p.skipSP()
		inner, err := parseSearchTerm(p)
		if err != nil {
			return nil, err
		}
		crit.Not = []imap.SearchCriteria{*inner}
	case "OR":
		p.skipSP()
		a, err := parseSearchTerm(p)
		if err != nil {
			return nil, err
		}
		p.skipSP()
		b, err := parseSearchTerm(p)
		if err != nil {
			return nil, err
		}
		crit.Or = [][2]imap.SearchCriteria{{*a, *b}}

	default:
		// Try as number set (seq-set without explicit keyword).
		if set, err := parseNumSetString(tok, false); err == nil {
			if s, ok := set.(imap.SeqSet); ok {
				crit.SeqNum = []imap.SeqSet{s}
				return crit, nil
			}
		}
		return nil, fmt.Errorf("protoimap: unknown SEARCH key %q", tok)
	}
	return crit, nil
}

func parseSearchHeader(p *parser, key string) (*imap.SearchCriteria, error) {
	p.skipSP()
	v, err := p.readAstring()
	if err != nil {
		return nil, err
	}
	return &imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: v}},
	}, nil
}

func readSearchDate(p *parser) (time.Time, error) {
	var raw string
	if p.peek() == '"' {
		s, err := p.readQuoted()
		if err != nil {
			return time.Time{}, err
		}
		raw = s
	} else {
		raw, _ = p.readAtom()
	}
	for _, layout := range []string{"2-Jan-2006", "02-Jan-2006"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("protoimap: bad SEARCH date %q", raw)
}
