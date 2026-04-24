package protoimap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"
)

// maxLineLength is the largest non-literal command line we accept
// (RFC 9051 gives no explicit bound but 64 KiB covers all realistic
// commands; literals carry their own byte count).
const maxLineLength = 64 * 1024

// maxAppendLiteral caps the literal size accepted by APPEND and similar
// commands. 50 MiB is the Phase 1 ceiling; larger values are rejected with
// NO [TOOBIG].
const maxAppendLiteral = 50 * 1024 * 1024

// ErrTooBig is returned by literal readers when the declared size exceeds
// maxAppendLiteral. The session maps this to NO [TOOBIG].
var ErrTooBig = errors.New("protoimap: literal size exceeds limit")

// literalReader is injected by the session so the parser can request a
// literal mid-line: the session writes the continuation request (if
// nonSync is false) and returns the literal bytes from the underlying
// reader.
type literalReader func(size int64, nonSync bool) ([]byte, error)

// Command is the parsed representation of a client command line plus any
// attached literals. Fields are populated per Op; a command that fails to
// parse produces a non-nil error, never a partial Command.
type Command struct {
	Tag string
	Op  string // UPPERCASE verb (e.g. "LOGIN", "FETCH"); "UID " prefix stripped
	Raw string

	LoginUser string
	LoginPass string

	AuthMechanism string
	AuthInitial   []byte

	Mailbox string

	RenameOldName string
	RenameNewName string

	ListReference string
	ListMailbox   string

	StatusItems []string

	AppendFlags    []string
	AppendInternal time.Time
	AppendData     []byte

	IDParams map[string]string

	FetchSet     imap.NumSet
	FetchOptions *imap.FetchOptions

	StoreSet     imap.NumSet
	StoreFlags   imap.StoreFlags
	StoreOptions imap.StoreOptions

	SearchCriteria *imap.SearchCriteria
	SearchOptions  *imap.SearchOptions
	SearchCharset  string

	ExpungeSet imap.NumSet

	// IsUID flags the UID-prefixed variants.
	IsUID bool
}

// parser walks a flattened command string, expanding literal placeholders
// on demand. Literal bytes are substituted into the token stream at the
// point the caller requested them.
type parser struct {
	src  []byte
	pos  int
	lits [][]byte // FIFO of literal payloads (oldest first)
}

// readCommand consumes one complete tagged command line (and its literals)
// from br, invoking readLit to materialise each literal chunk.
//
// Returns a fully-populated Command or an error. A zero-length command
// (caller sent only CRLF) returns (&Command{}, nil).
func readCommand(br *bufio.Reader, readLit literalReader) (*Command, error) {
	var sb strings.Builder
	var lits [][]byte
	first, err := readLine(br)
	if err != nil {
		return nil, err
	}
	if len(first) == 0 {
		return &Command{}, nil
	}
	sb.WriteString(first)
	for {
		line := sb.String()
		idx, size, nonSync, ok := lastLiteral(line)
		if !ok {
			break
		}
		data, lerr := readLit(size, nonSync)
		if lerr != nil {
			return nil, lerr
		}
		lits = append(lits, data)
		// Replace "{N}"/"{N+}" with a single NUL marker so the tokeniser
		// recognises the literal slot without re-scanning for braces.
		sb.Reset()
		sb.WriteString(line[:idx])
		sb.WriteByte(0)
		cont, cerr := readLine(br)
		if cerr != nil {
			return nil, cerr
		}
		sb.WriteString(cont)
	}
	p := &parser{src: []byte(sb.String()), lits: lits}
	cmd := &Command{Raw: sb.String()}
	if err := parseCommand(p, cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

// lastLiteral finds the trailing "{N}" or "{N+}" marker on a line. Returns
// the index in line, the size, the nonSync flag, and ok == true when a
// valid marker was found. It intentionally matches only at end-of-line so
// mid-line text like "{foo}" does not mis-trigger.
func lastLiteral(line string) (idx int, size int64, nonSync bool, ok bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, 0, false, false
	}
	openIdx := strings.LastIndex(line, "{")
	if openIdx < 0 {
		return 0, 0, false, false
	}
	spec := line[openIdx+1 : len(line)-1]
	ns := false
	if strings.HasSuffix(spec, "+") {
		ns = true
		spec = spec[:len(spec)-1]
	}
	n, err := strconv.ParseInt(spec, 10, 64)
	if err != nil || n < 0 {
		return 0, 0, false, false
	}
	if n > maxAppendLiteral {
		return 0, 0, false, false
	}
	return openIdx, n, ns, true
}

// readLine returns a single CRLF-terminated line (without the CRLF) from
// br. Returns io.ErrUnexpectedEOF when the peer hung up without sending
// the CRLF. Enforces maxLineLength.
func readLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for sb.Len() < maxLineLength {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF && sb.Len() > 0 {
				return "", io.ErrUnexpectedEOF
			}
			return "", err
		}
		if b == '\r' {
			next, err := br.ReadByte()
			if err != nil {
				return "", err
			}
			if next == '\n' {
				return sb.String(), nil
			}
			// Tolerate bare CR by keeping both bytes.
			sb.WriteByte('\r')
			sb.WriteByte(next)
			continue
		}
		if b == '\n' {
			return sb.String(), nil
		}
		sb.WriteByte(b)
	}
	return "", fmt.Errorf("protoimap: line exceeds %d bytes", maxLineLength)
}

// -----------------------------------------------------------------------------
// tokeniser
// -----------------------------------------------------------------------------

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) skipSP() {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
}

func (p *parser) expect(b byte) error {
	if p.eof() || p.src[p.pos] != b {
		return fmt.Errorf("protoimap: expected %q at pos %d", b, p.pos)
	}
	p.pos++
	return nil
}

// readAtom reads an IMAP atom (no SP, no parens, no quoted/literal syntax).
func (p *parser) readAtom() (string, error) {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ' ' || c == '(' || c == ')' || c == '[' || c == ']' || c == '"' || c == 0 {
			break
		}
		p.pos++
	}
	if start == p.pos {
		return "", fmt.Errorf("protoimap: expected atom at pos %d", start)
	}
	return string(p.src[start:p.pos]), nil
}

// readAstring reads an astring: atom, quoted string, or literal.
func (p *parser) readAstring() (string, error) {
	if p.eof() {
		return "", fmt.Errorf("protoimap: unexpected EOL")
	}
	switch p.src[p.pos] {
	case '"':
		return p.readQuoted()
	case 0:
		return p.takeLiteral()
	default:
		return p.readAtom()
	}
}

func (p *parser) readQuoted() (string, error) {
	if err := p.expect('"'); err != nil {
		return "", err
	}
	var sb strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\\' && p.pos+1 < len(p.src) {
			sb.WriteByte(p.src[p.pos+1])
			p.pos += 2
			continue
		}
		if c == '"' {
			p.pos++
			return sb.String(), nil
		}
		sb.WriteByte(c)
		p.pos++
	}
	return "", fmt.Errorf("protoimap: unterminated quoted string")
}

func (p *parser) takeLiteral() (string, error) {
	if err := p.expect(0); err != nil {
		return "", err
	}
	if len(p.lits) == 0 {
		return "", fmt.Errorf("protoimap: literal slot with no data")
	}
	data := p.lits[0]
	p.lits = p.lits[1:]
	return string(data), nil
}

// -----------------------------------------------------------------------------
// command dispatch
// -----------------------------------------------------------------------------

func parseCommand(p *parser, cmd *Command) error {
	tag, err := p.readAtom()
	if err != nil {
		return err
	}
	cmd.Tag = tag
	p.skipSP()
	verb, err := p.readAtom()
	if err != nil {
		return err
	}
	verbUpper := strings.ToUpper(verb)

	if verbUpper == "UID" {
		cmd.IsUID = true
		p.skipSP()
		inner, err := p.readAtom()
		if err != nil {
			return err
		}
		verbUpper = strings.ToUpper(inner)
	}
	cmd.Op = verbUpper

	switch verbUpper {
	case "CAPABILITY", "NOOP", "LOGOUT", "STARTTLS", "CHECK", "CLOSE", "IDLE", "DONE", "UNSELECT":
		return nil
	case "LOGIN":
		p.skipSP()
		user, err := p.readAstring()
		if err != nil {
			return err
		}
		p.skipSP()
		pass, err := p.readAstring()
		if err != nil {
			return err
		}
		cmd.LoginUser = user
		cmd.LoginPass = pass
		return nil
	case "AUTHENTICATE":
		p.skipSP()
		mech, err := p.readAtom()
		if err != nil {
			return err
		}
		cmd.AuthMechanism = strings.ToUpper(mech)
		// RFC 4959 SASL-IR: optional initial response.
		if p.peek() == ' ' {
			p.skipSP()
			if p.peek() == '=' {
				// Zero-length initial response sentinel.
				p.pos++
				cmd.AuthInitial = []byte{}
			} else {
				ir, err := p.readAstring()
				if err != nil {
					return err
				}
				cmd.AuthInitial = []byte(ir)
			}
		}
		return nil
	case "SELECT", "EXAMINE", "CREATE", "DELETE", "SUBSCRIBE", "UNSUBSCRIBE":
		p.skipSP()
		name, err := p.readAstring()
		if err != nil {
			return err
		}
		cmd.Mailbox = name
		return nil
	case "RENAME":
		p.skipSP()
		oldName, err := p.readAstring()
		if err != nil {
			return err
		}
		p.skipSP()
		newName, err := p.readAstring()
		if err != nil {
			return err
		}
		cmd.RenameOldName = oldName
		cmd.RenameNewName = newName
		return nil
	case "LIST", "LSUB":
		p.skipSP()
		ref, err := p.readAstring()
		if err != nil {
			return err
		}
		p.skipSP()
		mb, err := p.readAstring()
		if err != nil {
			return err
		}
		cmd.ListReference = ref
		cmd.ListMailbox = mb
		return nil
	case "STATUS":
		p.skipSP()
		name, err := p.readAstring()
		if err != nil {
			return err
		}
		cmd.Mailbox = name
		p.skipSP()
		if err := p.expect('('); err != nil {
			return err
		}
		var items []string
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
			items = append(items, strings.ToUpper(a))
		}
		cmd.StatusItems = items
		return nil
	case "APPEND":
		return parseAppend(p, cmd)
	case "ID":
		return parseID(p, cmd)
	case "FETCH":
		return parseFetch(p, cmd)
	case "STORE":
		return parseStore(p, cmd)
	case "SEARCH":
		return parseSearch(p, cmd)
	case "EXPUNGE":
		// EXPUNGE takes no args; UID EXPUNGE takes a UID set.
		if cmd.IsUID {
			p.skipSP()
			set, err := parseNumSet(p, true)
			if err != nil {
				return err
			}
			cmd.ExpungeSet = set
		}
		return nil
	case "ENABLE":
		// Accept but ignore contents for Phase 1.
		return nil
	case "NAMESPACE":
		return nil
	default:
		return fmt.Errorf("protoimap: unknown command %q", verbUpper)
	}
}

// parseAppend reads "mailbox [(flags)] [internaldate] literal".
func parseAppend(p *parser, cmd *Command) error {
	p.skipSP()
	mb, err := p.readAstring()
	if err != nil {
		return err
	}
	cmd.Mailbox = mb
	p.skipSP()
	if p.peek() == '(' {
		flags, err := parseFlagList(p)
		if err != nil {
			return err
		}
		cmd.AppendFlags = flags
		p.skipSP()
	}
	if p.peek() == '"' {
		ds, err := p.readQuoted()
		if err != nil {
			return err
		}
		t, terr := time.Parse(`2-Jan-2006 15:04:05 -0700`, ds)
		if terr != nil {
			t, terr = time.Parse(`_2-Jan-2006 15:04:05 -0700`, ds)
		}
		if terr == nil {
			cmd.AppendInternal = t
		}
		p.skipSP()
	}
	// Literal payload: must be at current position as NUL marker.
	if p.peek() != 0 {
		return fmt.Errorf("protoimap: APPEND requires literal payload")
	}
	data, err := p.takeLiteral()
	if err != nil {
		return err
	}
	cmd.AppendData = []byte(data)
	return nil
}

func parseID(p *parser, cmd *Command) error {
	p.skipSP()
	if p.eof() {
		return nil
	}
	if p.peek() == 'N' || p.peek() == 'n' {
		// "NIL"
		_, _ = p.readAtom()
		return nil
	}
	if err := p.expect('('); err != nil {
		return err
	}
	params := map[string]string{}
	for {
		p.skipSP()
		if p.peek() == ')' {
			p.pos++
			break
		}
		k, err := p.readAstring()
		if err != nil {
			return err
		}
		p.skipSP()
		// Value may be NIL atom or string.
		var v string
		if p.peek() == '"' || p.peek() == 0 {
			vs, verr := p.readAstring()
			if verr != nil {
				return verr
			}
			v = vs
		} else {
			a, aerr := p.readAtom()
			if aerr != nil {
				return aerr
			}
			if strings.EqualFold(a, "NIL") {
				v = ""
			} else {
				v = a
			}
		}
		params[k] = v
	}
	cmd.IDParams = params
	return nil
}

func parseFlagList(p *parser) ([]string, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	var out []string
	for {
		p.skipSP()
		if p.peek() == ')' {
			p.pos++
			break
		}
		a, err := p.readAtom()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// parseNumSet reads "1:5,7,10:*" as a SeqSet or UIDSet depending on uid.
func parseNumSet(p *parser, uid bool) (imap.NumSet, error) {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ' ' || c == '(' || c == ')' || c == '[' || c == ']' {
			break
		}
		p.pos++
	}
	if start == p.pos {
		return nil, fmt.Errorf("protoimap: expected number set")
	}
	s := string(p.src[start:p.pos])
	return parseNumSetString(s, uid)
}

func parseNumSetString(s string, uid bool) (imap.NumSet, error) {
	if uid {
		var out imap.UIDSet
		for _, part := range strings.Split(s, ",") {
			r, err := parseUIDRange(part)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	}
	var out imap.SeqSet
	for _, part := range strings.Split(s, ",") {
		r, err := parseSeqRange(part)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func parseSeqRange(s string) (imap.SeqRange, error) {
	lo, hi, err := parseRangeLoHi(s)
	if err != nil {
		return imap.SeqRange{}, err
	}
	return imap.SeqRange{Start: uint32(lo), Stop: uint32(hi)}, nil
}

func parseUIDRange(s string) (imap.UIDRange, error) {
	lo, hi, err := parseRangeLoHi(s)
	if err != nil {
		return imap.UIDRange{}, err
	}
	return imap.UIDRange{Start: imap.UID(lo), Stop: imap.UID(hi)}, nil
}

func parseRangeLoHi(s string) (uint64, uint64, error) {
	parts := strings.SplitN(s, ":", 2)
	lo, err := parseRangeTerm(parts[0])
	if err != nil {
		return 0, 0, err
	}
	if len(parts) == 1 {
		return lo, lo, nil
	}
	hi, err := parseRangeTerm(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return lo, hi, nil
}

func parseRangeTerm(s string) (uint64, error) {
	if s == "*" {
		// Represent "*" as uint32 max. The session resolves it against the
		// current mailbox HighestUID/HighestSeq when applying the set.
		return 0xFFFFFFFF, nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("protoimap: bad number %q: %w", s, err)
	}
	return n, nil
}
