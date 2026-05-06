package sieve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
)

// ActionKind enumerates the delivery actions a Sieve script can emit.
type ActionKind int

// Action kinds. A single invocation can produce multiple actions (for
// example: fileinto + keep, or redirect + keep via :copy). The delivery
// path interprets them in order.
const (
	ActionKeep ActionKind = iota
	ActionDiscard
	ActionFileInto
	ActionRedirect
	ActionReject
	ActionVacation
	ActionAddFlag
	ActionSetFlag
	ActionRemoveFlag
	ActionNotify
	ActionAddHeader
	ActionDeleteHeader
)

// Action is one decision recorded by the interpreter.
type Action struct {
	Kind ActionKind
	// Mailbox is the target folder for FileInto or MailboxID override.
	Mailbox string
	// MailboxID is the IMAP MAILBOXID (RFC 9042) value if the script
	// used fileinto :mailboxid. Set in addition to Mailbox when present.
	MailboxID string
	// Copy indicates the action had a :copy modifier (keep a copy in
	// addition to the regular disposition).
	Copy bool
	// Address is the target of Redirect / Notify (mailto:).
	Address string
	// Reason carries the Reject reason or Vacation body.
	Reason string
	// Subject is set on Vacation / Notify actions.
	Subject string
	// From is set on Vacation when :from is used.
	From string
	// Handle is the vacation :handle or duplicate :handle value.
	Handle string
	// Days is the vacation repetition window.
	Days int
	// Addresses is the vacation :addresses list.
	Addresses []string
	// HeaderName is the editheader target.
	HeaderName string
	// HeaderValue is the editheader value.
	HeaderValue string
	// Flag is the imap4flags target flag.
	Flag string
}

// Outcome is the interpreter's return value: the ordered list of actions
// and the implicit-keep flag. The delivery path decides how to execute
// each action (file into mailbox, mark flags, enqueue redirect to the
// outbound queue, record vacation response, etc.). This package does no
// I/O of its own.
type Outcome struct {
	Actions []Action
	// ImplicitKeep is true when no explicit keep, discard, fileinto, or
	// redirect-without-:copy was taken. The delivery path treats
	// ImplicitKeep as "file into INBOX" per RFC 5228 §2.10.6.
	ImplicitKeep bool
	// Stop is set when the script executed `stop`.
	Stop bool
}

// VacationStore is a Phase 1 seam for vacation dedup. Implementations
// persist (handle, sender, last-sent) tuples so a recipient doesn't
// receive two vacation replies inside the window. The delivery path
// wires a real store; tests use the in-memory helper.
type VacationStore interface {
	// ShouldSend reports whether a vacation reply to recipient (from
	// the script owner) should be sent now given a repetition window of
	// days days. It must be idempotent: the interpreter calls
	// ShouldSend first, and only records Send on actual dispatch.
	ShouldSend(ctx context.Context, handle, sender string, days int, now time.Time) (bool, error)
	// Record stores that a reply was sent. The interpreter calls this
	// before returning the outcome; the delivery layer is responsible
	// for actually dispatching the message.
	Record(ctx context.Context, handle, sender string, now time.Time) error
}

// DuplicateStore seams RFC 7352 duplicate tracking the same way as
// VacationStore.
type DuplicateStore interface {
	// SeenAndMark reports whether (handle, value) was seen inside window
	// and atomically records the new observation.
	SeenAndMark(ctx context.Context, handle, value string, window time.Duration, now time.Time) (bool, error)
}

// Environment is the runtime context passed to the interpreter.
type Environment struct {
	// Recipient is the envelope recipient the script is running for.
	Recipient string
	// OriginalRecipient preserves the original envelope recipient when a
	// forwarding/alias expansion has occurred.
	OriginalRecipient string
	// Sender is the envelope MAIL FROM.
	Sender string
	// Auth is the mail authentication result (SPF/DKIM/DMARC/ARC).
	// A nil value is treated as "no authentication data" — the
	// spamtest mapping folds to the unauthenticated path.
	Auth *mailauth.AuthResults
	// SpamScore is the normalised classifier score in the [0,10] range
	// used by RFC 5235 §2. A value <0 means unclassified.
	SpamScore float64
	// SpamVerdict is one of "ham" / "suspect" / "spam" / "unknown".
	SpamVerdict string
	// SpamReason carries the human-readable classifier reason (may be
	// empty). Exposed via ${spam.reason}.
	SpamReason string
	// Vacation is the dedup seam; required only when a script uses
	// vacation, else the interpreter returns an error mentioning the
	// missing dependency.
	Vacation VacationStore
	// Duplicate is the RFC 7352 seam. Required when a script uses the
	// duplicate test; else the interpreter reports missing.
	Duplicate DuplicateStore
	// Clock is the time source; nil falls back to clock.NewReal.
	Clock clock.Clock
	// Limits overrides SandboxLimits. The zero value is replaced with
	// DefaultSandboxLimits by the interpreter.
	Limits SandboxLimits
	// Now overrides the "current" time tests see; preferred to pre-
	// advancing Clock in simple tests.
	Now time.Time
	// Logger is the structured logger for the interpreter. When set it
	// must already carry subsystem=sieve, principal_id, and script_id via
	// log.With(...) at the call site (SMTP delivery path). A nil Logger
	// silences interpreter log output — tests that do not exercise the
	// logging path may leave this unset.
	Logger *slog.Logger
	// ScriptID identifies the script being evaluated; used in log records
	// when Logger is non-nil (REQ-OPS-83).
	ScriptID string
	// PrincipalID identifies the script owner; used in log records when
	// Logger is non-nil.
	PrincipalID string
}

// Interpreter evaluates a pre-parsed, pre-validated script against a
// message + Environment. Interpreters are stateless and safe to reuse.
type Interpreter struct{}

// NewInterpreter returns a new Interpreter.
func NewInterpreter() *Interpreter { return &Interpreter{} }

// Evaluate runs the script against msg and env. It never performs I/O;
// the returned Outcome describes what the caller should do.
//
// When env.Logger is non-nil the interpreter emits activity-tagged log
// records for each action taken and for runtime errors (REQ-OPS-86).
// The caller is responsible for pre-scoping the logger with
// subsystem=sieve, principal_id, and script_id before passing it in.
func (in *Interpreter) Evaluate(ctx context.Context, script *Script, msg mailparse.Message, env Environment) (Outcome, error) {
	if script == nil {
		return Outcome{}, errors.New("sieve: nil script")
	}
	if env.Clock == nil {
		env.Clock = clock.NewReal()
	}
	if env.Now.IsZero() {
		env.Now = env.Clock.Now()
	}
	if env.Limits == (SandboxLimits{}) {
		env.Limits = DefaultSandboxLimits()
	}
	// Scope the logger for this evaluation. The caller may have already
	// added subsystem/principal_id/script_id via log.With; we add them
	// here only when they are not already present (safe to re-add, slog
	// does not deduplicate but the pre-scoped pattern means the attrs are
	// set once at entry). When env.Logger is nil we fall back to a discard
	// logger so the rest of the code can log unconditionally.
	log := env.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	} else {
		log = log.With(
			"subsystem", "sieve",
			"principal_id", env.PrincipalID,
			"script_id", env.ScriptID,
		)
	}
	st := &state{
		ctx:      ctx,
		env:      env,
		msg:      msg,
		requires: map[string]bool{},
		sandbox:  newSandbox(env.Limits),
		vars:     map[string]string{},
		flags:    map[string]struct{}{},
		outcome:  Outcome{ImplicitKeep: true},
		log:      log,
	}
	for _, r := range script.Requires {
		st.requires[r] = true
	}
	if err := st.runBlock(script.Commands); err != nil {
		log.Warn("sieve runtime error",
			"activity", observe.ActivityInternal,
			"err", err)
		return st.outcome, err
	}
	return st.outcome, nil
}

// state is the per-evaluation mutable bag.
type state struct {
	ctx      context.Context
	env      Environment
	msg      mailparse.Message
	requires map[string]bool

	sandbox *sandbox
	vars    map[string]string
	flags   map[string]struct{}

	// currentPart is the MIME part the interpreter is "on" inside a
	// foreverypart loop iteration. nil at top level. Tests with the
	// :mime tag (RFC 5703 §4.2) consult this part's headers; outside
	// foreverypart they fall through to msg-level headers per the
	// RFC's "or with the message MIME header" branch.
	currentPart *mailparse.Part

	// breakLoop is set by the `break` command (RFC 5703 §4.1). The
	// surrounding foreverypart catches it on the next iteration check
	// and clears it. Named-break is not yet supported; the field is a
	// boolean rather than a name string for v1.5.
	breakLoop bool

	outcome Outcome
	log     *slog.Logger
}

func (s *state) runBlock(cmds []Command) error {
	// Sieve has chained if/elsif/else sequences. We walk the block and
	// track the previous if's condition to route elsif/else.
	var lastIfTaken bool
	var inChain bool
	for _, c := range cmds {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if err := s.sandbox.tick(); err != nil {
			return err
		}
		// `break` inside a foreverypart sets s.breakLoop; bail out of
		// the current block immediately so the surrounding loop driver
		// (runForeveryPart) can observe and clear the flag.
		if s.breakLoop {
			return nil
		}
		switch c.Name {
		case "if":
			taken, err := s.ifCommand(c)
			if err != nil {
				return err
			}
			lastIfTaken = taken
			inChain = true
			if s.outcome.Stop {
				return nil
			}
		case "elsif":
			if !inChain {
				return &ValidationError{Line: c.Line, Column: c.Column, Message: "elsif without matching if"}
			}
			if lastIfTaken {
				continue
			}
			taken, err := s.ifCommand(c)
			if err != nil {
				return err
			}
			lastIfTaken = taken
			if s.outcome.Stop {
				return nil
			}
		case "else":
			if !inChain {
				return &ValidationError{Line: c.Line, Column: c.Column, Message: "else without matching if"}
			}
			if !lastIfTaken {
				if err := s.runBlock(c.Block); err != nil {
					return err
				}
			}
			inChain = false
			if s.outcome.Stop {
				return nil
			}
		default:
			inChain = false
			if err := s.runSimple(c); err != nil {
				return err
			}
			if s.outcome.Stop {
				return nil
			}
		}
	}
	return nil
}

func (s *state) ifCommand(c Command) (bool, error) {
	if c.Test == nil {
		return false, &ValidationError{Line: c.Line, Column: c.Column, Message: "if without test"}
	}
	ok, err := s.evalTest(*c.Test)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := s.runBlock(c.Block); err != nil {
		return true, err
	}
	return true, nil
}

func (s *state) runSimple(c Command) error {
	switch c.Name {
	case "require":
		// require is top-level; if we see it here, validation let it
		// through. Treat as no-op.
		return nil
	case "stop":
		s.outcome.Stop = true
		return nil
	case "keep":
		return s.appendAction(Action{Kind: ActionKeep})
	case "discard":
		s.outcome.ImplicitKeep = false
		return s.appendAction(Action{Kind: ActionDiscard})
	case "fileinto":
		return s.fileInto(c)
	case "redirect":
		return s.redirect(c)
	case "reject", "ereject":
		return s.reject(c)
	case "vacation":
		return s.vacation(c)
	case "setflag", "addflag", "removeflag":
		return s.flagCmd(c)
	case "set":
		return s.setVar(c)
	case "include":
		// Inline include only: the production path resolves includes out
		// of band before handing us the AST. Treat as no-op with a note.
		return nil
	case "return":
		s.outcome.Stop = true
		return nil
	case "addheader":
		return s.addHeader(c)
	case "deleteheader":
		return s.deleteHeader(c)
	case "notify":
		return s.notify(c)
	case "foreverypart":
		return s.runForeveryPart(c)
	case "break":
		s.breakLoop = true
		return nil
	case "extracttext":
		return s.extractText(c)
	case "replace", "enclose":
		// Body-mutation actions (RFC 5703 §4.3, §4.4) require a
		// delivery-side rewrite path that does not yet exist. Recognise
		// the commands so scripts parse, but record no action; a
		// follow-up wave will surface ActionReplacePart / ActionEnclose
		// alongside the existing addheader/deleteheader actions.
		return nil
	default:
		return &ValidationError{Line: c.Line, Column: c.Column, Message: fmt.Sprintf("command %q not implemented", c.Name)}
	}
}

// runForeveryPart implements RFC 5703 §3 iteration. It walks every leaf
// MIME part (text and binary, in source order) and runs the inner
// block once per part, with state.currentPart pointing at the
// iteration's part. The iteration terminates when the block executes
// `break`, when the script's stop bit is set, or when every leaf has
// been visited.
//
// The RFC defines iteration scope as "the current MIME part's direct
// children", which would require the script to nest foreverypart loops
// to descend. This implementation flattens the tree to all leaves so a
// single foreverypart visits every part the way most scripts intend
// (including over attachments inside multipart/alternative or
// multipart/mixed). Named-break (foreverypart :name x { ... break :name x })
// is not implemented; v1 scripts use one loop and bare break.
func (s *state) runForeveryPart(c Command) error {
	if !s.requires["foreverypart"] {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "foreverypart requires \"foreverypart\""}
	}
	leaves := collectLeafParts(s.msg.Body)
	prev := s.currentPart
	defer func() { s.currentPart = prev }()
	for i := range leaves {
		s.currentPart = &leaves[i]
		if err := s.runBlock(c.Block); err != nil {
			return err
		}
		if s.breakLoop {
			s.breakLoop = false
			return nil
		}
		if s.outcome.Stop {
			return nil
		}
	}
	return nil
}

// collectLeafParts flattens p into a slice of every leaf part in walk
// order. Multipart container nodes are descended; non-multipart leaves
// (text/* or any application/* / image/* part) become an entry. This
// matches the "every part in the message" semantics most foreverypart
// scripts assume.
func collectLeafParts(p mailparse.Part) []mailparse.Part {
	if len(p.Children) == 0 {
		return []mailparse.Part{p}
	}
	var out []mailparse.Part
	for _, c := range p.Children {
		out = append(out, collectLeafParts(c)...)
	}
	return out
}

// extractText implements RFC 5703 §4.5. Inside a foreverypart loop it
// stores the current part's decoded text (Part.Text for text/*, or the
// stringified Part.Bytes otherwise) in the named variable. Outside a
// loop it stores the message's primary text body. The :first tag caps
// the stored length in bytes.
func (s *state) extractText(c Command) error {
	if !s.requires["mime"] {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "extracttext requires \"mime\""}
	}
	var first int64 = -1
	var varname string
	for i := 0; i < len(c.Args); i++ {
		a := c.Args[i]
		switch a.Kind {
		case ArgTag:
			if strings.EqualFold(a.Tag, ":first") {
				if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgNumber {
					first = c.Args[i+1].Num
					i++
				}
			}
			// Other modifier tags (RFC 5229 :lower / :upper / etc.)
			// are deliberately ignored in v1.5; the variable receives
			// the raw text.
		case ArgString:
			if varname == "" {
				varname = a.Str
			}
		}
	}
	if varname == "" {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "extracttext requires varname"}
	}
	text := s.partOrMessageText()
	if first > 0 && int64(len(text)) > first {
		text = text[:first]
	}
	if len(text) > s.env.Limits.MaxVariableBytes {
		text = text[:s.env.Limits.MaxVariableBytes]
	}
	existing, ok := s.vars[varname]
	if !ok {
		if len(s.vars) >= s.env.Limits.MaxVariables {
			return ErrVariableBudget
		}
		if err := s.sandbox.checkVarStorage(len(text)); err != nil {
			return err
		}
	} else {
		s.sandbox.totalBytes -= len(existing)
		if err := s.sandbox.checkVarStorage(len(text)); err != nil {
			return err
		}
	}
	s.vars[varname] = text
	return nil
}

// partOrMessageText returns the text content of the current part if
// inside a foreverypart loop, otherwise the message's primary text
// body. Used by extracttext.
func (s *state) partOrMessageText() string {
	if s.currentPart != nil {
		if s.currentPart.IsText() && s.currentPart.Text != "" {
			return s.currentPart.Text
		}
		return string(s.currentPart.Bytes)
	}
	return mailparse.PrimaryTextBody(s.msg)
}

func (s *state) appendAction(a Action) error {
	if err := s.sandbox.recordAction(); err != nil {
		return err
	}
	// fileinto / redirect-without-copy / discard clear the implicit keep.
	switch a.Kind {
	case ActionFileInto, ActionDiscard, ActionReject:
		s.outcome.ImplicitKeep = false
	case ActionRedirect:
		if !a.Copy {
			s.outcome.ImplicitKeep = false
		}
	case ActionKeep:
		s.outcome.ImplicitKeep = false
	}
	s.outcome.Actions = append(s.outcome.Actions, a)
	// Emit system/debug for every action so the SMTP delivery flow log
	// shows what Sieve decided (REQ-OPS-86 activity guide).
	s.logAction(a)
	return nil
}

// logAction emits a system/debug record describing the action. Vacation
// is handled separately in the vacation() method to use system/info
// (REQ-OPS-86 activity guide: "vacation-response sent -> system/info").
func (s *state) logAction(a Action) {
	switch a.Kind {
	case ActionKeep:
		s.log.Debug("sieve action: keep", "activity", observe.ActivitySystem)
	case ActionDiscard:
		s.log.Debug("sieve action: discard", "activity", observe.ActivitySystem)
	case ActionFileInto:
		s.log.Debug("sieve action: fileinto", "activity", observe.ActivitySystem,
			"mailbox", a.Mailbox)
	case ActionRedirect:
		s.log.Debug("sieve action: redirect", "activity", observe.ActivitySystem,
			"address", a.Address, "copy", a.Copy)
	case ActionReject:
		s.log.Debug("sieve action: reject", "activity", observe.ActivitySystem)
	case ActionVacation:
		// vacation-response sent is system/info per the activity guide.
		s.log.Info("vacation response sent", "activity", observe.ActivitySystem,
			"sender", s.env.Sender, "handle", a.Handle)
	case ActionAddFlag, ActionSetFlag, ActionRemoveFlag:
		s.log.Debug("sieve action: flag", "activity", observe.ActivitySystem,
			"kind", a.Kind, "flag", a.Flag)
	case ActionNotify:
		s.log.Debug("sieve action: notify", "activity", observe.ActivitySystem,
			"address", a.Address)
	case ActionAddHeader, ActionDeleteHeader:
		s.log.Debug("sieve action: editheader", "activity", observe.ActivitySystem,
			"kind", a.Kind, "header", a.HeaderName)
	default:
		s.log.Debug("sieve action", "activity", observe.ActivitySystem, "kind", a.Kind)
	}
}

// --- action implementations -------------------------------------------------

func (s *state) fileInto(c Command) error {
	a := Action{Kind: ActionFileInto}
	var folder string
	for _, arg := range c.Args {
		switch arg.Kind {
		case ArgTag:
			switch strings.ToLower(arg.Tag) {
			case ":copy":
				a.Copy = true
			case ":mailboxid":
			case ":flags":
				// handled below as the next arg
			}
		case ArgString:
			folder = s.expandVars(arg.Str)
		case ArgStringList:
			if len(arg.StrList) > 0 {
				folder = s.expandVars(arg.StrList[0])
			}
		}
	}
	// Scan for :mailboxid <string> and :flags <string-or-list>.
	for i := 0; i < len(c.Args); i++ {
		if c.Args[i].Kind != ArgTag {
			continue
		}
		switch strings.ToLower(c.Args[i].Tag) {
		case ":mailboxid":
			if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgString {
				a.MailboxID = s.expandVars(c.Args[i+1].Str)
			}
		case ":flags":
			if i+1 < len(c.Args) {
				switch c.Args[i+1].Kind {
				case ArgString:
					a.Flag = s.expandVars(c.Args[i+1].Str)
				case ArgStringList:
					if len(c.Args[i+1].StrList) > 0 {
						a.Flag = s.expandVars(c.Args[i+1].StrList[0])
					}
				}
			}
		}
	}
	if folder == "" && a.MailboxID == "" {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "fileinto requires a mailbox argument"}
	}
	a.Mailbox = folder
	return s.appendAction(a)
}

func (s *state) redirect(c Command) error {
	if err := s.sandbox.recordRedirect(); err != nil {
		return err
	}
	a := Action{Kind: ActionRedirect}
	for _, arg := range c.Args {
		switch arg.Kind {
		case ArgTag:
			if strings.EqualFold(arg.Tag, ":copy") {
				a.Copy = true
			}
		case ArgString:
			a.Address = s.expandVars(arg.Str)
		}
	}
	if a.Address == "" {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "redirect requires an address"}
	}
	return s.appendAction(a)
}

func (s *state) reject(c Command) error {
	a := Action{Kind: ActionReject}
	for _, arg := range c.Args {
		if arg.Kind == ArgString {
			a.Reason = s.expandVars(arg.Str)
		}
	}
	return s.appendAction(a)
}

func (s *state) vacation(c Command) error {
	if s.env.Vacation == nil {
		return errors.New("sieve: vacation used but no VacationStore provided")
	}
	a := Action{Kind: ActionVacation, Days: 7}
	var body, subject, handle, fromAddr string
	var seconds int
	var addresses []string
	for i := 0; i < len(c.Args); i++ {
		arg := c.Args[i]
		switch arg.Kind {
		case ArgTag:
			switch strings.ToLower(arg.Tag) {
			case ":days":
				if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgNumber {
					a.Days = int(c.Args[i+1].Num)
					i++
				}
			case ":seconds":
				if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgNumber {
					seconds = int(c.Args[i+1].Num)
					i++
				}
			case ":subject":
				if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgString {
					subject = s.expandVars(c.Args[i+1].Str)
					i++
				}
			case ":from":
				if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgString {
					fromAddr = s.expandVars(c.Args[i+1].Str)
					i++
				}
			case ":addresses":
				if i+1 < len(c.Args) {
					switch c.Args[i+1].Kind {
					case ArgStringList:
						for _, v := range c.Args[i+1].StrList {
							addresses = append(addresses, s.expandVars(v))
						}
					case ArgString:
						addresses = append(addresses, s.expandVars(c.Args[i+1].Str))
					}
					i++
				}
			case ":handle":
				if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgString {
					handle = s.expandVars(c.Args[i+1].Str)
					i++
				}
			case ":mime":
				// Treat body as pre-formed MIME; Phase 1 records the
				// raw body and lets the delivery layer decide whether
				// to wrap it.
			}
		case ArgString:
			body = s.expandVars(arg.Str)
		}
	}
	if seconds > 0 {
		a.Days = seconds / 86400
		if a.Days < 1 {
			a.Days = 1
		}
	}
	a.Reason = body
	a.Subject = subject
	a.From = fromAddr
	a.Handle = handle
	a.Addresses = addresses
	// Dedup via VacationStore before recording the action so the
	// delivery layer never sees redundant intents.
	ok, err := s.env.Vacation.ShouldSend(s.ctx, handle, s.env.Sender, a.Days, s.env.Now)
	if err != nil {
		return fmt.Errorf("sieve: vacation store: %w", err)
	}
	if !ok {
		return nil
	}
	if err := s.env.Vacation.Record(s.ctx, handle, s.env.Sender, s.env.Now); err != nil {
		return fmt.Errorf("sieve: vacation record: %w", err)
	}
	return s.appendAction(a)
}

func (s *state) flagCmd(c Command) error {
	var kind ActionKind
	switch c.Name {
	case "setflag":
		kind = ActionSetFlag
	case "addflag":
		kind = ActionAddFlag
	case "removeflag":
		kind = ActionRemoveFlag
	}
	for _, arg := range c.Args {
		switch arg.Kind {
		case ArgString:
			if err := s.appendAction(Action{Kind: kind, Flag: s.expandVars(arg.Str)}); err != nil {
				return err
			}
		case ArgStringList:
			for _, v := range arg.StrList {
				if err := s.appendAction(Action{Kind: kind, Flag: s.expandVars(v)}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *state) setVar(c Command) error {
	if !s.requires["variables"] {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "set requires \"variables\""}
	}
	// Variables: set [MODIFIER*] <name: string> <value: string>.
	var modifiers []string
	var name, value string
	var seenName bool
	for _, arg := range c.Args {
		switch arg.Kind {
		case ArgTag:
			modifiers = append(modifiers, strings.ToLower(arg.Tag))
		case ArgString:
			if !seenName {
				name = arg.Str
				seenName = true
			} else {
				value = s.expandVars(arg.Str)
			}
		}
	}
	if name == "" {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "set requires a variable name"}
	}
	for _, m := range modifiers {
		value = applyVarModifier(m, value)
	}
	if len(value) > s.env.Limits.MaxVariableBytes {
		return ErrVariableSize
	}
	existing, ok := s.vars[name]
	if !ok {
		if len(s.vars) >= s.env.Limits.MaxVariables {
			return ErrVariableBudget
		}
		if err := s.sandbox.checkVarStorage(len(value)); err != nil {
			return err
		}
	} else {
		// replace storage accounting.
		s.sandbox.totalBytes -= len(existing)
		if err := s.sandbox.checkVarStorage(len(value)); err != nil {
			return err
		}
	}
	s.vars[name] = value
	return nil
}

func applyVarModifier(tag, value string) string {
	switch tag {
	case ":lower":
		return strings.ToLower(value)
	case ":upper":
		return strings.ToUpper(value)
	case ":lowerfirst":
		if len(value) == 0 {
			return value
		}
		return strings.ToLower(value[:1]) + value[1:]
	case ":upperfirst":
		if len(value) == 0 {
			return value
		}
		return strings.ToUpper(value[:1]) + value[1:]
	case ":quotewildcard":
		var b strings.Builder
		b.Grow(len(value))
		for _, r := range value {
			switch r {
			case '*', '?', '\\':
				b.WriteRune('\\')
			}
			b.WriteRune(r)
		}
		return b.String()
	case ":length":
		return strconv.Itoa(len(value))
	default:
		return value
	}
}

func (s *state) addHeader(c Command) error {
	if !s.requires["editheader"] {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "addheader requires \"editheader\""}
	}
	a := Action{Kind: ActionAddHeader}
	var args []string
	for _, arg := range c.Args {
		if arg.Kind == ArgString {
			args = append(args, s.expandVars(arg.Str))
		}
	}
	if len(args) < 2 {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "addheader requires name and value"}
	}
	a.HeaderName = args[0]
	a.HeaderValue = args[1]
	return s.appendAction(a)
}

func (s *state) deleteHeader(c Command) error {
	if !s.requires["editheader"] {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "deleteheader requires \"editheader\""}
	}
	a := Action{Kind: ActionDeleteHeader}
	for _, arg := range c.Args {
		if arg.Kind == ArgString {
			a.HeaderName = s.expandVars(arg.Str)
			break
		}
	}
	if a.HeaderName == "" {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "deleteheader requires a name"}
	}
	return s.appendAction(a)
}

func (s *state) notify(c Command) error {
	if !s.requires["enotify"] {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "notify requires \"enotify\""}
	}
	if err := s.sandbox.recordNotify(); err != nil {
		return err
	}
	a := Action{Kind: ActionNotify}
	for i := 0; i < len(c.Args); i++ {
		arg := c.Args[i]
		switch arg.Kind {
		case ArgTag:
			switch strings.ToLower(arg.Tag) {
			case ":message":
				if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgString {
					a.Reason = s.expandVars(c.Args[i+1].Str)
					i++
				}
			case ":from":
				if i+1 < len(c.Args) && c.Args[i+1].Kind == ArgString {
					a.From = s.expandVars(c.Args[i+1].Str)
					i++
				}
			case ":options", ":importance", ":method":
				if i+1 < len(c.Args) {
					i++
				}
			}
		case ArgString:
			uri := s.expandVars(arg.Str)
			if !strings.HasPrefix(strings.ToLower(uri), "mailto:") {
				return &ValidationError{Line: c.Line, Column: c.Column, Message: "notify URI must be mailto:"}
			}
			a.Address = uri
		}
	}
	if a.Address == "" {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: "notify requires a URI"}
	}
	return s.appendAction(a)
}

// --- tests ------------------------------------------------------------------

func (s *state) evalTest(t Test) (bool, error) {
	if err := s.ctx.Err(); err != nil {
		return false, err
	}
	if err := s.sandbox.tick(); err != nil {
		return false, err
	}
	switch t.Name {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "not":
		if len(t.Children) != 1 {
			return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "not takes one test"}
		}
		v, err := s.evalTest(t.Children[0])
		if err != nil {
			return false, err
		}
		return !v, nil
	case "allof":
		for _, c := range t.Children {
			v, err := s.evalTest(c)
			if err != nil {
				return false, err
			}
			if !v {
				return false, nil
			}
		}
		return true, nil
	case "anyof":
		for _, c := range t.Children {
			v, err := s.evalTest(c)
			if err != nil {
				return false, err
			}
			if v {
				return true, nil
			}
		}
		return false, nil
	case "exists":
		return s.testExists(t), nil
	case "size":
		return s.testSize(t)
	case "header":
		return s.testHeader(t)
	case "address":
		return s.testAddress(t)
	case "envelope":
		return s.testEnvelope(t)
	case "body":
		return s.testBody(t)
	case "hasflag":
		return s.testHasFlag(t), nil
	case "date", "currentdate":
		return s.testDate(t)
	case "string":
		return s.testString(t)
	case "mailboxexists", "mailboxidexists", "metadata", "metadataexists", "servermetadata":
		// These require a store lookup seam; Phase 1 returns false and
		// records the test name for observability. Conservative: a
		// script that asks whether a mailbox exists gets "no", which
		// keeps default delivery untouched.
		return false, nil
	case "spamtest":
		return s.testSpam(t)
	case "duplicate":
		return s.testDuplicate(t)
	case "valid_notify_method", "notify_method_capability":
		// We only speak mailto: in Phase 1.
		return s.testNotifyMethod(t), nil
	case "environment":
		// environment values we know: "name" (server), "domain", "host".
		return s.testEnvironmentExt(t), nil
	default:
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: fmt.Sprintf("test %q not implemented", t.Name)}
	}
}

// matcher encapsulates the comparator + match-type + relational suffix
// selected by a test's tags.
type matcher struct {
	comparator   string
	matchType    string // :is / :contains / :matches / :regex
	relation     string // :count or :value operator suffix
	addressPart  string // :all / :localpart / :domain / :user / :detail
	bodyType     string // :raw / :text / :content — for body test
	contentTypes []string
	// mime is true when the test carried the :mime tag (RFC 5703 §4.2).
	// Inside a foreverypart loop the test reads from the current part's
	// MIME headers; outside, it falls back to the message-level headers.
	mime bool
	// anychild is true when the test carried :anychild. Reserved for
	// child-walking semantics; recognised but not yet honoured beyond
	// the existing message-level read.
	anychild bool
}

func parseMatcher(args []Argument) (matcher, []Argument) {
	m := matcher{
		comparator:  "i;ascii-casemap",
		matchType:   ":is",
		addressPart: ":all",
		bodyType:    ":text",
	}
	var rest []Argument
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.Kind != ArgTag {
			rest = append(rest, a)
			continue
		}
		tag := strings.ToLower(a.Tag)
		switch tag {
		case ":is", ":contains", ":matches", ":regex":
			m.matchType = tag
		case ":comparator":
			if i+1 < len(args) && args[i+1].Kind == ArgString {
				m.comparator = args[i+1].Str
				i++
			}
		case ":count", ":value":
			if i+1 < len(args) && args[i+1].Kind == ArgString {
				m.relation = tag + " " + args[i+1].Str
				i++
			}
		case ":all", ":localpart", ":domain", ":user", ":detail":
			m.addressPart = tag
		case ":raw", ":text", ":content":
			m.bodyType = tag
			if tag == ":content" && i+1 < len(args) {
				if args[i+1].Kind == ArgStringList {
					m.contentTypes = append(m.contentTypes, args[i+1].StrList...)
					i++
				} else if args[i+1].Kind == ArgString {
					m.contentTypes = append(m.contentTypes, args[i+1].Str)
					i++
				}
			}
		case ":index":
			if i+1 < len(args) {
				i++
			}
		case ":last":
			// positional flag for :index direction
		case ":mime":
			m.mime = true
		case ":anychild":
			m.anychild = true
		default:
			// Unknown tag is carried along (defensive, shouldn't happen
			// after validation).
			rest = append(rest, a)
		}
	}
	return m, rest
}

func (s *state) testExists(t Test) bool {
	mime := false
	for _, a := range t.Args {
		if a.Kind == ArgTag && strings.EqualFold(a.Tag, ":mime") {
			mime = true
		}
	}
	headers := s.msg.Headers
	if mime && s.currentPart != nil {
		headers = s.currentPart.Headers
	}
	for _, a := range t.Args {
		switch a.Kind {
		case ArgString:
			if headers.Get(a.Str) == "" {
				return false
			}
		case ArgStringList:
			for _, v := range a.StrList {
				if headers.Get(v) == "" {
					return false
				}
			}
		}
	}
	return true
}

func (s *state) testSize(t Test) (bool, error) {
	var mode string
	var n int64
	for _, a := range t.Args {
		switch a.Kind {
		case ArgTag:
			if strings.EqualFold(a.Tag, ":over") {
				mode = "over"
			} else if strings.EqualFold(a.Tag, ":under") {
				mode = "under"
			}
		case ArgNumber:
			n = a.Num
		}
	}
	size := s.msg.Size
	if size == 0 {
		size = int64(len(s.msg.Raw))
	}
	switch mode {
	case "over":
		return size > n, nil
	case "under":
		return size < n, nil
	default:
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "size requires :over or :under"}
	}
}

func (s *state) testHeader(t Test) (bool, error) {
	m, rest := parseMatcher(t.Args)
	if len(rest) < 2 {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "header requires headers + keys"}
	}
	names := asStrings(rest[0])
	keys := asStrings(rest[1])
	headers := s.headersFor(m)
	if m.relation != "" {
		return s.countMatch(m, headersToValues(headers, names), keys)
	}
	for _, n := range names {
		for _, v := range headers.GetAll(n) {
			for _, k := range keys {
				ok, err := compare(m, strings.TrimSpace(v), s.expandVars(k))
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// headersFor returns the headers a header/exists/address test should
// consult given the matcher's :mime flag and the current foreverypart
// scope. RFC 5703 §4.2: ":mime" reads MIME headers of the current
// part, "or with the message MIME header if the test is not used
// inside a foreverypart loop."
func (s *state) headersFor(m matcher) mailparse.Headers {
	if m.mime && s.currentPart != nil {
		return s.currentPart.Headers
	}
	return s.msg.Headers
}

func (s *state) testAddress(t Test) (bool, error) {
	m, rest := parseMatcher(t.Args)
	if len(rest) < 2 {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "address requires headers + keys"}
	}
	names := asStrings(rest[0])
	keys := asStrings(rest[1])
	headers := s.headersFor(m)
	var vals []string
	for _, n := range names {
		for _, v := range headers.GetAll(n) {
			vs := extractAddressParts(v, m.addressPart)
			vals = append(vals, vs...)
		}
	}
	if m.relation != "" {
		return s.countMatch(m, vals, keys)
	}
	for _, v := range vals {
		for _, k := range keys {
			ok, err := compare(m, v, s.expandVars(k))
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *state) testEnvelope(t Test) (bool, error) {
	if !s.requires["envelope"] {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "envelope requires \"envelope\""}
	}
	m, rest := parseMatcher(t.Args)
	if len(rest) < 2 {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "envelope requires parts + keys"}
	}
	names := asStrings(rest[0])
	keys := asStrings(rest[1])
	var vals []string
	for _, n := range names {
		switch strings.ToLower(n) {
		case "from":
			vals = append(vals, extractAddressParts(s.env.Sender, m.addressPart)...)
		case "to":
			vals = append(vals, extractAddressParts(s.env.Recipient, m.addressPart)...)
		case "orig_to":
			vals = append(vals, extractAddressParts(s.env.OriginalRecipient, m.addressPart)...)
		}
	}
	if m.relation != "" {
		return s.countMatch(m, vals, keys)
	}
	for _, v := range vals {
		for _, k := range keys {
			ok, err := compare(m, v, s.expandVars(k))
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *state) testBody(t Test) (bool, error) {
	if !s.requires["body"] {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "body requires \"body\""}
	}
	m, rest := parseMatcher(t.Args)
	if len(rest) < 1 {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "body requires keys"}
	}
	keys := asStrings(rest[0])
	body := collectBody(s.msg.Body, m.bodyType, m.contentTypes)
	for _, k := range keys {
		ok, err := compare(m, body, s.expandVars(k))
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *state) testHasFlag(t Test) bool {
	if !s.requires["imap4flags"] {
		return false
	}
	// hasflag takes an optional variable name then a string-list of keys.
	var keys []string
	for _, a := range t.Args {
		switch a.Kind {
		case ArgString:
			keys = append(keys, a.Str)
		case ArgStringList:
			keys = append(keys, a.StrList...)
		}
	}
	for _, k := range keys {
		if _, ok := s.flags[k]; ok {
			return true
		}
	}
	return false
}

func (s *state) testDate(t Test) (bool, error) {
	if !s.requires["date"] {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "date requires \"date\""}
	}
	m, rest := parseMatcher(t.Args)
	if t.Name == "currentdate" {
		// currentdate <:zone | :originalzone>? <date-part: string> <key-list>
		if len(rest) < 2 {
			return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "currentdate requires date-part and keys"}
		}
		part := asString(rest[0])
		keys := asStrings(rest[1])
		val := datePart(s.env.Now.UTC(), part)
		for _, k := range keys {
			ok, err := compare(m, val, s.expandVars(k))
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	// date <header: string> <date-part: string> <key-list>.
	if len(rest) < 3 {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "date requires header, date-part, keys"}
	}
	hdr := asString(rest[0])
	part := asString(rest[1])
	keys := asStrings(rest[2])
	raw := s.msg.Headers.Get(hdr)
	ts, err := mail.ParseDate(raw)
	if err != nil {
		return false, nil
	}
	val := datePart(ts.UTC(), part)
	for _, k := range keys {
		ok, err := compare(m, val, s.expandVars(k))
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *state) testString(t Test) (bool, error) {
	if !s.requires["variables"] {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "string requires \"variables\""}
	}
	m, rest := parseMatcher(t.Args)
	if len(rest) < 2 {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "string requires sources + keys"}
	}
	sources := asStrings(rest[0])
	keys := asStrings(rest[1])
	for _, src := range sources {
		v := s.expandVars(src)
		for _, k := range keys {
			ok, err := compare(m, v, s.expandVars(k))
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *state) testSpam(t Test) (bool, error) {
	if !s.requires["spamtest"] && !s.requires["spamtestplus"] {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "spamtest requires \"spamtest\" or \"spamtestplus\""}
	}
	m, rest := parseMatcher(t.Args)
	if len(rest) < 1 {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "spamtest requires keys"}
	}
	keys := asStrings(rest[0])
	score := mapSpamScore(s.env.SpamScore, s.env.Auth, s.requires["spamtestplus"])
	if m.relation == "" {
		m.relation = ":value eq"
	}
	scoreStr := strconv.Itoa(score)
	for _, k := range keys {
		ok, err := compareRelational(scoreStr, m.relation, s.expandVars(k))
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *state) testDuplicate(t Test) (bool, error) {
	if !s.requires["duplicate"] {
		return false, &ValidationError{Line: t.Line, Column: t.Column, Message: "duplicate requires \"duplicate\""}
	}
	if s.env.Duplicate == nil {
		return false, errors.New("sieve: duplicate test used but no DuplicateStore provided")
	}
	var handle, header, uniqueID string
	var seconds int = 7 * 86400
	for i := 0; i < len(t.Args); i++ {
		a := t.Args[i]
		if a.Kind != ArgTag {
			continue
		}
		switch strings.ToLower(a.Tag) {
		case ":handle":
			if i+1 < len(t.Args) && t.Args[i+1].Kind == ArgString {
				handle = t.Args[i+1].Str
				i++
			}
		case ":header":
			if i+1 < len(t.Args) && t.Args[i+1].Kind == ArgString {
				header = t.Args[i+1].Str
				i++
			}
		case ":uniqueid":
			if i+1 < len(t.Args) && t.Args[i+1].Kind == ArgString {
				uniqueID = t.Args[i+1].Str
				i++
			}
		case ":seconds":
			if i+1 < len(t.Args) && t.Args[i+1].Kind == ArgNumber {
				seconds = int(t.Args[i+1].Num)
				i++
			}
		}
	}
	value := uniqueID
	if value == "" && header != "" {
		value = s.msg.Headers.Get(header)
	}
	if value == "" {
		value = s.msg.Envelope.MessageID
	}
	seen, err := s.env.Duplicate.SeenAndMark(s.ctx, handle, value, time.Duration(seconds)*time.Second, s.env.Now)
	if err != nil {
		return false, fmt.Errorf("sieve: duplicate store: %w", err)
	}
	return seen, nil
}

func (s *state) testNotifyMethod(t Test) bool {
	for _, a := range t.Args {
		switch a.Kind {
		case ArgString:
			if !strings.HasPrefix(strings.ToLower(a.Str), "mailto:") {
				return false
			}
		case ArgStringList:
			for _, v := range a.StrList {
				if !strings.HasPrefix(strings.ToLower(v), "mailto:") {
					return false
				}
			}
		}
	}
	return true
}

func (s *state) testEnvironmentExt(_ Test) bool { return false }

// --- comparators + match types ---------------------------------------------

func compare(m matcher, value, key string) (bool, error) {
	comp := strings.ToLower(m.comparator)
	var v, k string
	switch comp {
	case "i;ascii-casemap", "":
		v = strings.ToLower(value)
		k = strings.ToLower(key)
	case "i;octet":
		v = value
		k = key
	case "i;ascii-numeric":
		return compareAsciiNumeric(value, key, m.matchType)
	default:
		// Unknown comparator — treat as octet.
		v = value
		k = key
	}
	switch m.matchType {
	case ":is":
		return v == k, nil
	case ":contains":
		return strings.Contains(v, k), nil
	case ":matches":
		return globMatch(k, v), nil
	case ":regex":
		re, err := regexp.Compile("(?s)" + k)
		if err != nil {
			if _, ok := err.(*syntax.Error); ok {
				return false, fmt.Errorf("invalid :regex pattern %q: %w", key, err)
			}
			return false, err
		}
		return re.MatchString(v), nil
	default:
		return false, fmt.Errorf("unknown match-type %q", m.matchType)
	}
}

func compareAsciiNumeric(value, key, matchType string) (bool, error) {
	// RFC 4790 i;ascii-numeric: truncate at first non-digit; if no
	// leading digit, value is +∞ for comparisons.
	vn, vInf := parseLeadingNumber(value)
	kn, kInf := parseLeadingNumber(key)
	switch matchType {
	case ":is":
		if vInf && kInf {
			return true, nil
		}
		if vInf || kInf {
			return false, nil
		}
		return vn == kn, nil
	case ":contains", ":matches", ":regex":
		// Not defined for i;ascii-numeric.
		return false, fmt.Errorf("i;ascii-numeric does not support match-type %s", matchType)
	default:
		return false, fmt.Errorf("unknown match-type %q", matchType)
	}
}

func parseLeadingNumber(s string) (int64, bool) {
	if s == "" {
		return 0, true
	}
	var n int64
	hasDigit := false
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		hasDigit = true
		n = n*10 + int64(r-'0')
	}
	if !hasDigit {
		return 0, true
	}
	return n, false
}

// globMatch implements the Sieve :matches glob: '*' and '?' wildcards, no
// character classes.
func globMatch(pattern, s string) bool {
	// Iterative NFA.
	var pi, si int
	star := -1
	match := 0
	for si < len(s) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			match = si
			pi++
			continue
		}
		if star != -1 {
			pi = star + 1
			match++
			si = match
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

func (s *state) countMatch(m matcher, vals, keys []string) (bool, error) {
	if strings.HasPrefix(m.relation, ":count") {
		parts := strings.Fields(m.relation)
		if len(parts) != 2 {
			return false, fmt.Errorf("bad :count relation %q", m.relation)
		}
		op := parts[1]
		n := strconv.Itoa(len(vals))
		for _, k := range keys {
			ok, err := compareRelational(n, ":value "+op, s.expandVars(k))
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	// :value — fall through to normal compare, one-by-one.
	parts := strings.Fields(m.relation)
	if len(parts) != 2 {
		return false, fmt.Errorf("bad :value relation %q", m.relation)
	}
	op := parts[1]
	for _, v := range vals {
		for _, k := range keys {
			ok, err := compareRelational(v, ":value "+op, s.expandVars(k))
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}

func compareRelational(v, relation, k string) (bool, error) {
	parts := strings.Fields(relation)
	if len(parts) != 2 {
		return false, fmt.Errorf("bad relational %q", relation)
	}
	op := parts[1]
	vn, vErr := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	kn, kErr := strconv.ParseInt(strings.TrimSpace(k), 10, 64)
	if vErr == nil && kErr == nil {
		switch op {
		case "eq":
			return vn == kn, nil
		case "ne":
			return vn != kn, nil
		case "gt":
			return vn > kn, nil
		case "ge":
			return vn >= kn, nil
		case "lt":
			return vn < kn, nil
		case "le":
			return vn <= kn, nil
		}
	}
	switch op {
	case "eq":
		return v == k, nil
	case "ne":
		return v != k, nil
	case "gt":
		return v > k, nil
	case "ge":
		return v >= k, nil
	case "lt":
		return v < k, nil
	case "le":
		return v <= k, nil
	}
	return false, fmt.Errorf("unknown relational op %q", op)
}

// --- helpers ----------------------------------------------------------------

func asStrings(a Argument) []string {
	switch a.Kind {
	case ArgString:
		return []string{a.Str}
	case ArgStringList:
		return append([]string(nil), a.StrList...)
	}
	return nil
}

func asString(a Argument) string {
	if a.Kind == ArgString {
		return a.Str
	}
	return ""
}

func headersToValues(h mailparse.Headers, names []string) []string {
	var out []string
	for _, n := range names {
		out = append(out, h.GetAll(n)...)
	}
	return out
}

// extractAddressParts pulls from a raw address header value the pieces
// selected by the :all / :localpart / :domain / :user / :detail tag.
func extractAddressParts(raw, part string) []string {
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		if a, err2 := mail.ParseAddress(raw); err2 == nil {
			addrs = []*mail.Address{a}
		} else if raw != "" {
			addrs = []*mail.Address{{Address: raw}}
		}
	}
	var out []string
	for _, a := range addrs {
		addr := a.Address
		switch strings.ToLower(part) {
		case ":all", "":
			out = append(out, addr)
		case ":localpart":
			at := strings.IndexByte(addr, '@')
			if at >= 0 {
				out = append(out, addr[:at])
			} else {
				out = append(out, addr)
			}
		case ":domain":
			at := strings.IndexByte(addr, '@')
			if at >= 0 {
				out = append(out, addr[at+1:])
			}
		case ":user", ":detail":
			at := strings.IndexByte(addr, '@')
			local := addr
			if at >= 0 {
				local = addr[:at]
			}
			plus := strings.IndexByte(local, '+')
			if part == ":user" {
				if plus >= 0 {
					out = append(out, local[:plus])
				} else {
					out = append(out, local)
				}
			} else { // :detail
				if plus >= 0 {
					out = append(out, local[plus+1:])
				} else {
					out = append(out, "")
				}
			}
		}
	}
	return out
}

// collectBody materialises the body content the body test needs. :text
// concatenates decoded text/* parts; :raw returns the full raw message
// bytes; :content filters parts whose major/minor content-type matches
// one of the supplied prefixes.
func collectBody(p mailparse.Part, mode string, filters []string) string {
	switch mode {
	case ":raw":
		if len(p.Bytes) > 0 {
			return string(p.Bytes)
		}
		return p.Text
	case ":content":
		var parts []string
		collectContent(p, filters, &parts)
		return strings.Join(parts, "\n")
	default: // :text
		var parts []string
		collectText(p, &parts)
		return strings.Join(parts, "\n")
	}
}

func collectText(p mailparse.Part, out *[]string) {
	if p.IsText() && p.Text != "" {
		*out = append(*out, p.Text)
	}
	for _, c := range p.Children {
		collectText(c, out)
	}
}

func collectContent(p mailparse.Part, filters []string, out *[]string) {
	for _, f := range filters {
		if contentTypePrefixMatch(p.ContentType, f) {
			if p.Text != "" {
				*out = append(*out, p.Text)
			} else if len(p.Bytes) > 0 {
				*out = append(*out, string(p.Bytes))
			}
			break
		}
	}
	for _, c := range p.Children {
		collectContent(c, filters, out)
	}
}

func contentTypePrefixMatch(ct, prefix string) bool {
	ct = strings.ToLower(ct)
	prefix = strings.ToLower(prefix)
	if prefix == "" {
		return true
	}
	if !strings.Contains(prefix, "/") {
		return strings.HasPrefix(ct, prefix+"/")
	}
	return strings.HasPrefix(ct, prefix)
}

// datePart returns the textual representation of t's component named by
// part (per RFC 5260 §4): year, month, day, date, weekday, etc.
func datePart(t time.Time, part string) string {
	switch strings.ToLower(part) {
	case "year":
		return fmt.Sprintf("%04d", t.Year())
	case "month":
		return fmt.Sprintf("%02d", int(t.Month()))
	case "day":
		return fmt.Sprintf("%02d", t.Day())
	case "date":
		return t.Format("2006-01-02")
	case "julian":
		return strconv.Itoa(julian(t))
	case "hour":
		return fmt.Sprintf("%02d", t.Hour())
	case "minute":
		return fmt.Sprintf("%02d", t.Minute())
	case "second":
		return fmt.Sprintf("%02d", t.Second())
	case "time":
		return t.Format("15:04:05")
	case "iso8601":
		return t.Format(time.RFC3339)
	case "std11":
		return t.Format(time.RFC1123Z)
	case "zone":
		_, offset := t.Zone()
		sign := "+"
		if offset < 0 {
			sign = "-"
			offset = -offset
		}
		return fmt.Sprintf("%s%02d%02d", sign, offset/3600, (offset%3600)/60)
	case "weekday":
		return strconv.Itoa(int(t.Weekday()))
	default:
		return ""
	}
}

func julian(t time.Time) int {
	// Modified Julian Day per the :julian Sieve date-part.
	y, mo, d := t.Date()
	year, m := y, int(mo)
	if m <= 2 {
		year--
		m += 12
	}
	a := year / 100
	b := 2 - a + a/4
	mjd := int(365.25*float64(year+4716)) + int(30.6001*float64(m+1)) + d + b - 1524 - 2400000
	return mjd
}

// expandVars performs the RFC 5229 variable substitution. Only recognises
// simple ${name} references; ${unicode:...} and ${hex:...} are already
// expanded at parse time by expandEncodedChars. Unknown names expand to
// the empty string per RFC 5229 §3.
func (s *state) expandVars(in string) string {
	if !s.requires["variables"] {
		return in
	}
	if !strings.Contains(in, "${") {
		return in
	}
	var b strings.Builder
	b.Grow(len(in))
	for i := 0; i < len(in); {
		if i+1 < len(in) && in[i] == '$' && in[i+1] == '{' {
			end := strings.IndexByte(in[i+2:], '}')
			if end < 0 {
				b.WriteString(in[i:])
				break
			}
			name := in[i+2 : i+2+end]
			b.WriteString(s.lookupVar(name))
			i = i + 2 + end + 1
			continue
		}
		b.WriteByte(in[i])
		i++
	}
	return b.String()
}

func (s *state) lookupVar(name string) string {
	// Built-in namespaces: spam.*, env.*, numbered match captures.
	if v, ok := s.vars[name]; ok {
		return v
	}
	switch strings.ToLower(name) {
	case "spam.verdict":
		return s.env.SpamVerdict
	case "spam.confidence":
		if s.env.SpamScore < 0 {
			return ""
		}
		return strconv.FormatFloat(s.env.SpamScore, 'f', 2, 64)
	case "spam.reason":
		return s.env.SpamReason
	}
	return ""
}

// mapSpamScore normalises the [0,1] classifier score into the [0,10]
// spamtest range per RFC 5235 §2. A negative score (unclassified) maps
// to "0" (undefined). spamtestplus promises the value is an integer in
// [0,10]; we rely on that to keep the comparator simple.
func mapSpamScore(score float64, _ *mailauth.AuthResults, _ bool) int {
	if score < 0 {
		return 0
	}
	if score > 1 {
		score = 1
	}
	v := int(score*9.0) + 1
	if v < 1 {
		v = 1
	}
	if v > 10 {
		v = 10
	}
	return v
}
