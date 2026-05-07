package protomanagesieve

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// Command is the parsed wire-form of a single ManageSieve command. The
// shape varies by verb: Args carries the textual arguments (atom or
// quoted string), and Literals carries any synchronising literals
// consumed inline. Verbs that take a {N} script payload reach for
// Literals[0].
type Command struct {
	Verb     string
	Args     []string
	Literals [][]byte
}

// readCommand consumes one ManageSieve command line plus its
// literals. The grammar is line-oriented (RFC 5804 §1.6): tokens
// separated by spaces, atoms unquoted, strings double-quoted with
// backslash escaping, literals as "{N}" or "{N+}" placeholders that
// the server replaces with the literal payload.
func (ses *session) readCommand() (*Command, error) {
	line, err := ses.readLineRaw()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(line) == "" {
		return &Command{}, nil
	}
	cmd := &Command{}
	for {
		tokens, finished, lit, err := tokeniseLine(line)
		if err != nil {
			return nil, err
		}
		if cmd.Verb == "" && len(tokens) > 0 {
			cmd.Verb = strings.ToUpper(tokens[0])
			cmd.Args = append(cmd.Args, tokens[1:]...)
		} else {
			cmd.Args = append(cmd.Args, tokens...)
		}
		if lit == nil {
			if finished {
				return cmd, nil
			}
			break
		}
		buf, lerr := ses.readScriptLiteral(lit.size, lit.sync)
		if lerr != nil {
			return nil, lerr
		}
		cmd.Literals = append(cmd.Literals, buf)
		// After the literal there may be more arguments on the
		// following line.
		next, nerr := ses.readLineRaw()
		if nerr != nil {
			return nil, nerr
		}
		if next == "" {
			return cmd, nil
		}
		line = next
	}
	return cmd, nil
}

// literalSpec is a small helper carrying parsed {N}/{N+} parameters.
type literalSpec struct {
	size int64
	sync bool // true for {N}, false for {N+}
}

// tokeniseLine splits a single physical line into tokens, returning
// "finished == true" when the line ends without a trailing literal.
// When the line ends with a "{N}" / "{N+}" placeholder, lit carries
// the parsed parameters and the caller must read the literal bytes
// before continuing.
func tokeniseLine(line string) (tokens []string, finished bool, lit *literalSpec, err error) {
	i := 0
	for i < len(line) {
		// skip whitespace
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		switch line[i] {
		case '"':
			// Quoted string.
			j := i + 1
			var sb strings.Builder
			for j < len(line) {
				c := line[j]
				if c == '\\' && j+1 < len(line) {
					sb.WriteByte(line[j+1])
					j += 2
					continue
				}
				if c == '"' {
					j++
					break
				}
				sb.WriteByte(c)
				j++
			}
			tokens = append(tokens, sb.String())
			i = j
		case '{':
			// Literal placeholder; must be the last thing on the
			// line.
			closeIdx := strings.IndexByte(line[i:], '}')
			if closeIdx < 0 {
				return nil, false, nil, fmt.Errorf("protomanagesieve: unterminated literal spec")
			}
			spec := line[i+1 : i+closeIdx]
			ns := false
			if strings.HasSuffix(spec, "+") {
				ns = true
				spec = strings.TrimSuffix(spec, "+")
			}
			n, perr := strconv.ParseInt(spec, 10, 64)
			if perr != nil || n < 0 {
				return nil, false, nil, fmt.Errorf("protomanagesieve: bad literal spec %q", spec)
			}
			rest := strings.TrimSpace(line[i+closeIdx+1:])
			if rest != "" {
				return nil, false, nil, fmt.Errorf("protomanagesieve: trailing data after literal spec")
			}
			return tokens, false, &literalSpec{size: n, sync: !ns}, nil
		default:
			// Atom: read until whitespace.
			j := i
			for j < len(line) && line[j] != ' ' && line[j] != '\t' {
				j++
			}
			tokens = append(tokens, line[i:j])
			i = j
		}
	}
	return tokens, true, nil, nil
}

// -----------------------------------------------------------------------------
// CAPABILITY
// -----------------------------------------------------------------------------

func (ses *session) handleCAPABILITY(c *Command) error {
	ses.logger.Debug("CAPABILITY", "activity", observe.ActivityAccess)
	for _, line := range ses.capabilityLines() {
		if err := ses.writeLine(line); err != nil {
			return err
		}
	}
	return ses.writeOK("Capability completed")
}

// -----------------------------------------------------------------------------
// STARTTLS
// -----------------------------------------------------------------------------

func (ses *session) handleSTARTTLS(ctx context.Context, c *Command) error {
	ses.logger.Debug("STARTTLS", "activity", observe.ActivityAccess)
	if ses.tlsActive {
		return ses.writeNO("", "TLS already active")
	}
	if ses.s.tlsStore == nil {
		return ses.writeNO("", "TLS not available")
	}
	if err := ses.writeOK("Begin TLS negotiation now"); err != nil {
		return err
	}
	tlsConn, leaf, err := ses.s.upgradeTLS(ctx, ses.conn)
	if err != nil {
		ses.logger.Debug("STARTTLS handshake error", "err", err, "activity", observe.ActivityAccess)
		return err
	}
	ses.conn = tlsConn
	ses.br = bufio.NewReaderSize(tlsConn, 16*1024)
	ses.bw = bufio.NewWriter(tlsConn)
	ses.tlsActive = true
	if leaf != nil {
		if cb, err := sasl.TLSServerEndpoint(leaf); err == nil {
			ses.serverEndpoint = cb
		}
	}
	// RFC 5804 §2.2: server MUST re-emit the capability list followed
	// by OK so the client sees the post-TLS SASL set as a complete
	// response.
	for _, line := range ses.capabilityLines() {
		if err := ses.writeLine(line); err != nil {
			return err
		}
	}
	return ses.writeOK("TLS negotiation successful")
}

// Compile-time guard so the unused import linter does not complain
// when STARTTLS is the only consumer of crypto/tls in this file.
var _ = tls.VersionTLS12

// -----------------------------------------------------------------------------
// AUTHENTICATE
// -----------------------------------------------------------------------------

func (ses *session) handleAUTHENTICATE(ctx context.Context, c *Command) error {
	if ses.state == stateAuthed {
		return ses.writeNO("", "Already authenticated")
	}
	// RFC 5804 §1.5: AUTHENTICATE requires TLS unless the operator
	// turned off TLS entirely.
	if err := ses.requireTLS(); err != nil {
		return err
	}
	if len(c.Args) == 0 {
		return ses.writeNO("", "AUTHENTICATE missing mechanism")
	}
	mechName := c.Args[0]
	mech, err := ses.makeMechanism(mechName)
	if err != nil {
		return ses.writeNO("", err.Error())
	}
	if ses.tlsActive {
		ctx = sasl.WithTLS(ctx, true)
		if len(ses.serverEndpoint) > 0 {
			ctx = sasl.WithTLSServerEndpoint(ctx, ses.serverEndpoint)
		}
	}
	// Optional initial response: RFC 5804 §2.1 wraps SASL data in
	// modified base64. Quoted string and literal forms both carry
	// the base64 encoding; we decode to the raw bytes the SASL
	// state machine expects.
	var initial []byte
	if len(c.Args) >= 2 {
		decoded, derr := decodeSASLPayload(c.Args[1])
		if derr != nil {
			ses.logger.Warn("AUTHENTICATE bad initial response",
				"activity", observe.ActivityAudit,
				"mechanism", mechName)
			return ses.writeNO("", "bad SASL initial response")
		}
		initial = decoded
	} else if len(c.Literals) > 0 {
		decoded, derr := decodeSASLPayload(string(c.Literals[0]))
		if derr != nil {
			ses.logger.Warn("AUTHENTICATE bad initial response",
				"activity", observe.ActivityAudit,
				"mechanism", mechName)
			return ses.writeNO("", "bad SASL initial response")
		}
		initial = decoded
	}
	challenge, done, err := mech.Start(ctx, initial)
	if err != nil {
		ses.logger.Warn("AUTHENTICATE failed",
			"activity", observe.ActivityAudit,
			"mechanism", mechName,
			"err", err)
		return ses.writeNO("", "Authentication failed")
	}
	for !done {
		// Server challenges are sent as base64-encoded quoted strings.
		encoded := base64.StdEncoding.EncodeToString(challenge)
		if err := ses.writeLine(quoteString(encoded)); err != nil {
			return err
		}
		line, rerr := ses.readLineRaw()
		if rerr != nil {
			return rerr
		}
		if line == `"*"` || line == "*" {
			ses.logger.Warn("AUTHENTICATE aborted by client",
				"activity", observe.ActivityAudit,
				"mechanism", mechName)
			return ses.writeNO("", "client aborted SASL")
		}
		decoded, derr := decodeSASLLine(line)
		if derr != nil {
			ses.logger.Warn("AUTHENTICATE bad SASL response",
				"activity", observe.ActivityAudit,
				"mechanism", mechName)
			return ses.writeNO("", "bad SASL response")
		}
		challenge, done, err = mech.Next(ctx, decoded)
		if err != nil {
			ses.logger.Warn("AUTHENTICATE failed",
				"activity", observe.ActivityAudit,
				"mechanism", mechName,
				"err", err)
			return ses.writeNO("", "Authentication failed")
		}
	}
	pid, perr := mech.Principal()
	if perr != nil {
		ses.logger.Warn("AUTHENTICATE principal lookup failed",
			"activity", observe.ActivityAudit,
			"mechanism", mechName,
			"err", perr)
		return ses.writeNO("", "Authentication failed")
	}
	ses.pid = pid
	ses.state = stateAuthed
	// Enrich the session logger with the authenticated principal (REQ-OPS-83).
	ses.logger = ses.logger.With("principal_id", slog.AnyValue(pid))
	ses.logger.Info("AUTHENTICATE success",
		"activity", observe.ActivityAudit,
		"mechanism", mechName,
		"principal_id", pid)
	return ses.writeOK("Authenticated")
}

// decodeSASLLine unwraps the quoted/literal form on the wire and
// base64-decodes the payload per RFC 5804 §2.1. Returns the raw bytes
// the SASL state machine expects.
func decodeSASLLine(line string) ([]byte, error) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "\"") && strings.HasSuffix(line, "\"") && len(line) >= 2 {
		line = line[1 : len(line)-1]
	}
	return decodeSASLPayload(line)
}

// decodeSASLPayload base64-decodes a SASL payload. Empty / "=" inputs
// map to the zero-length byte slice (RFC 4422 sentinel).
func decodeSASLPayload(s string) ([]byte, error) {
	if s == "" || s == "=" {
		return []byte{}, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

// -----------------------------------------------------------------------------
// HAVESPACE
// -----------------------------------------------------------------------------

func (ses *session) handleHAVESPACE(ctx context.Context, c *Command) error {
	if err := ses.requireAuth(); err != nil {
		return nil
	}
	if err := ses.requireTLS(); err != nil {
		return nil
	}
	if len(c.Args) < 2 {
		return ses.writeNO("", "HAVESPACE requires <name> <size>")
	}
	size, perr := strconv.ParseInt(c.Args[1], 10, 64)
	if perr != nil || size < 0 {
		return ses.writeNO("", "bad size")
	}
	if size > ses.s.opts.MaxScriptBytes {
		return ses.writeNO("QUOTA/MAXSIZE", "script too large")
	}
	// Quota check against the principal's overall quota. The store
	// does not yet expose per-Sieve quota, so we approximate by
	// rejecting only oversize scripts; a per-script quota lands when
	// multi-script support arrives.
	ses.logger.Debug("HAVESPACE", "activity", observe.ActivityAccess, "size", size)
	return ses.writeOK("HAVESPACE completed")
}

// -----------------------------------------------------------------------------
// PUTSCRIPT / CHECKSCRIPT
// -----------------------------------------------------------------------------

func (ses *session) handlePUTSCRIPT(ctx context.Context, c *Command) error {
	if err := ses.requireAuth(); err != nil {
		return nil
	}
	if err := ses.requireTLS(); err != nil {
		return nil
	}
	if len(c.Args) < 1 || len(c.Literals) < 1 {
		return ses.writeNO("", "PUTSCRIPT requires <name> <script>")
	}
	name := c.Args[0]
	body := c.Literals[0]
	if name == "" {
		return ses.writeNO("", "PUTSCRIPT requires non-empty script name")
	}
	// RFC 5804 §2.6 / REQ-PROTO-51: parse + validate using the
	// runtime interpreter's own grammar.
	script, perr := sieve.Parse(body)
	if perr != nil {
		return ses.writeNOQuotedScriptName(name, perr.Error())
	}
	if verr := sieve.Validate(script); verr != nil {
		return ses.writeNOQuotedScriptName(name, verr.Error())
	}
	if err := ses.s.store.Meta().PutSieveScriptByName(ctx, ses.pid, name, string(body)); err != nil {
		return ses.writeNO("", "store write failed")
	}
	ses.logger.Info("PUTSCRIPT",
		"activity", observe.ActivityUser,
		"script_name", name,
		"principal_id", ses.pid,
		"size_bytes", len(body))
	return ses.writeOK("PUTSCRIPT completed")
}

func (ses *session) handleCHECKSCRIPT(ctx context.Context, c *Command) error {
	if err := ses.requireAuth(); err != nil {
		return nil
	}
	if err := ses.requireTLS(); err != nil {
		return nil
	}
	if len(c.Literals) < 1 {
		return ses.writeNO("", "CHECKSCRIPT requires <script>")
	}
	body := c.Literals[0]
	script, perr := sieve.Parse(body)
	if perr != nil {
		return ses.writeNO("", perr.Error())
	}
	if verr := sieve.Validate(script); verr != nil {
		return ses.writeNO("", verr.Error())
	}
	ses.logger.Info("CHECKSCRIPT",
		"activity", observe.ActivityUser,
		"principal_id", ses.pid,
		"size_bytes", len(body))
	return ses.writeOK("CHECKSCRIPT completed")
}

// -----------------------------------------------------------------------------
// GETSCRIPT / DELETESCRIPT / LISTSCRIPTS / SETACTIVE / RENAMESCRIPT
// -----------------------------------------------------------------------------

func (ses *session) handleGETSCRIPT(ctx context.Context, c *Command) error {
	if err := ses.requireAuth(); err != nil {
		return nil
	}
	if err := ses.requireTLS(); err != nil {
		return nil
	}
	if len(c.Args) < 1 {
		return ses.writeNO("", "GETSCRIPT requires <name>")
	}
	name := c.Args[0]
	ses.logger.Debug("GETSCRIPT", "activity", observe.ActivityAccess, "script_name", name)
	body, err := ses.s.store.Meta().GetSieveScriptByName(ctx, ses.pid, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ses.writeNO("NONEXISTENT", "no script for that name")
		}
		return ses.writeNO("", "GETSCRIPT failed")
	}
	// Server response for a successful GETSCRIPT is the script
	// literal followed by the OK terminator (RFC 5804 §2.5).
	if err := ses.writeLine(fmt.Sprintf("{%d}", len(body))); err != nil {
		return err
	}
	if _, err := ses.bw.WriteString(body); err != nil {
		return err
	}
	if _, err := ses.bw.WriteString("\r\n"); err != nil {
		return err
	}
	if err := ses.bw.Flush(); err != nil {
		return err
	}
	return ses.writeOK("GETSCRIPT completed")
}

func (ses *session) handleDELETESCRIPT(ctx context.Context, c *Command) error {
	if err := ses.requireAuth(); err != nil {
		return nil
	}
	if err := ses.requireTLS(); err != nil {
		return nil
	}
	if len(c.Args) < 1 {
		return ses.writeNO("", "DELETESCRIPT requires <name>")
	}
	name := c.Args[0]
	if err := ses.s.store.Meta().DeleteSieveScriptByName(ctx, ses.pid, name); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return ses.writeNO("NONEXISTENT", "no such script")
		case errors.Is(err, store.ErrConflict):
			// RFC 5804 §2.10: deleting the active script must fail.
			return ses.writeNO("ACTIVE", "cannot delete active script")
		}
		return ses.writeNO("", "DELETESCRIPT failed")
	}
	ses.logger.Info("DELETESCRIPT",
		"activity", observe.ActivityUser,
		"script_name", name,
		"principal_id", ses.pid)
	return ses.writeOK("DELETESCRIPT completed")
}

func (ses *session) handleLISTSCRIPTS(ctx context.Context, c *Command) error {
	if err := ses.requireAuth(); err != nil {
		return nil
	}
	if err := ses.requireTLS(); err != nil {
		return nil
	}
	ses.logger.Debug("LISTSCRIPTS", "activity", observe.ActivityAccess)
	scripts, err := ses.s.store.Meta().ListSieveScripts(ctx, ses.pid)
	if err != nil {
		return ses.writeNO("", "LISTSCRIPTS failed")
	}
	for _, s := range scripts {
		var line string
		if s.IsActive {
			line = fmt.Sprintf("%s ACTIVE", quoteString(s.Name))
		} else {
			line = quoteString(s.Name)
		}
		if err := ses.writeLine(line); err != nil {
			return err
		}
	}
	return ses.writeOK("LISTSCRIPTS completed")
}

// handleSETACTIVE flips the active script per RFC 5804 §2.8.
// SETACTIVE "" deactivates whichever script is currently active.
func (ses *session) handleSETACTIVE(ctx context.Context, c *Command) error {
	if err := ses.requireAuth(); err != nil {
		return nil
	}
	if err := ses.requireTLS(); err != nil {
		return nil
	}
	if len(c.Args) < 1 {
		return ses.writeNO("", "SETACTIVE requires <name>")
	}
	name := c.Args[0]
	if err := ses.s.store.Meta().SetActiveSieveScript(ctx, ses.pid, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ses.writeNO("NONEXISTENT", "no such script")
		}
		return ses.writeNO("", "SETACTIVE failed")
	}
	ses.logger.Info("SETACTIVE",
		"activity", observe.ActivityUser,
		"script_name", name,
		"principal_id", ses.pid)
	return ses.writeOK("SETACTIVE completed")
}

func (ses *session) handleRENAMESCRIPT(ctx context.Context, c *Command) error {
	if err := ses.requireAuth(); err != nil {
		return nil
	}
	if err := ses.requireTLS(); err != nil {
		return nil
	}
	if len(c.Args) < 2 {
		return ses.writeNO("", "RENAMESCRIPT requires <old> <new>")
	}
	oldName, newName := c.Args[0], c.Args[1]
	if newName == "" {
		return ses.writeNO("", "RENAMESCRIPT requires non-empty new name")
	}
	if err := ses.s.store.Meta().RenameSieveScript(ctx, ses.pid, oldName, newName); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return ses.writeNO("NONEXISTENT", "no such script")
		case errors.Is(err, store.ErrConflict):
			return ses.writeNO("ALREADYEXISTS", "script with new name already exists")
		}
		return ses.writeNO("", "RENAMESCRIPT failed")
	}
	ses.logger.Info("RENAMESCRIPT",
		"activity", observe.ActivityUser,
		"old_name", oldName,
		"new_name", newName,
		"principal_id", ses.pid)
	return ses.writeOK("RENAMESCRIPT completed")
}

// Compile-time anchors so the unused-import detector does not flag
// these packages on build paths that don't exercise them.
var (
	_ = bytes.NewReader
	_ = store.PrincipalID(0)
)
