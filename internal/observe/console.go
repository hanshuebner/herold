package observe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// ANSI SGR colour codes. Only used when the writer is a TTY and NO_COLOR is
// not set.
const (
	ansiReset   = "\x1b[0m"
	ansiGray    = "\x1b[2m"
	ansiBold    = "\x1b[1m"
	ansiCyan    = "\x1b[36m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiRed     = "\x1b[31m"
	ansiMagenta = "\x1b[35m" // used for [suite]/[admin] source markers
)

// isTerminal is the TTY detection hook. Replaced in tests via the
// forceColor seam.
var isTerminal = func(fd int) bool {
	return isTerminalOS(fd)
}

// ConsoleHandler is an slog.Handler that produces human-readable single-line
// output with optional ANSI colour (REQ-OPS-81a). See NewConsoleHandler.
type ConsoleHandler struct {
	w          io.Writer
	opts       *slog.HandlerOptions
	clk        clock.Clock
	forceColor *bool // non-nil overrides TTY detection (test seam)
	useColor   bool  // resolved once at construction
	mu         sync.Mutex
	preAttrs   []slog.Attr
	group      []string // nested group stack
}

// NewConsoleHandler returns a ConsoleHandler writing to w. opts.Level gates
// records (default LevelInfo). Color is auto-detected from w if w is an
// *os.File; otherwise it is off. Use NewConsoleHandlerWithClock for test
// injection.
func NewConsoleHandler(w io.Writer, opts *slog.HandlerOptions) *ConsoleHandler {
	return newConsoleHandler(w, opts, nil, nil)
}

// NewConsoleHandlerWithClock is the test-seam constructor. clk provides
// deterministic timestamps; forceColor, when non-nil, bypasses TTY detection.
func NewConsoleHandlerWithClock(w io.Writer, opts *slog.HandlerOptions, clk clock.Clock, forceColor *bool) *ConsoleHandler {
	return newConsoleHandler(w, opts, clk, forceColor)
}

func newConsoleHandler(w io.Writer, opts *slog.HandlerOptions, clk clock.Clock, forceColor *bool) *ConsoleHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	color := resolveColor(w, forceColor)
	return &ConsoleHandler{
		w:          w,
		opts:       opts,
		clk:        clk,
		forceColor: forceColor,
		useColor:   color,
	}
}

// resolveColor determines whether ANSI colour should be used.
func resolveColor(w io.Writer, forceColor *bool) bool {
	if forceColor != nil {
		return *forceColor
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if f, ok := w.(*os.File); ok {
		return isTerminal(int(f.Fd()))
	}
	return false
}

// Enabled reports whether the handler handles records at lvl.
func (h *ConsoleHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	min := slog.LevelInfo
	if h.opts.Level != nil {
		min = h.opts.Level.Level()
	}
	return lvl >= min
}

// Handle formats r and writes it to the underlying writer.
func (h *ConsoleHandler) Handle(_ context.Context, r slog.Record) error {
	min := slog.LevelInfo
	if h.opts.Level != nil {
		min = h.opts.Level.Level()
	}
	if r.Level < min {
		return nil
	}

	var buf bytes.Buffer
	h.formatRecord(&buf, r)

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf.Bytes())
	return err
}

// WithAttrs returns a new handler with additional pre-scoped attributes.
func (h *ConsoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := h.clone()
	nh.preAttrs = append(nh.preAttrs, attrs...)
	return nh
}

// WithGroup returns a new handler with an additional group nesting level.
func (h *ConsoleHandler) WithGroup(name string) slog.Handler {
	nh := h.clone()
	nh.group = append(nh.group, name)
	return nh
}

func (h *ConsoleHandler) clone() *ConsoleHandler {
	preAttrs := make([]slog.Attr, len(h.preAttrs))
	copy(preAttrs, h.preAttrs)
	group := make([]string, len(h.group))
	copy(group, h.group)
	return &ConsoleHandler{
		w:          h.w,
		opts:       h.opts,
		clk:        h.clk,
		forceColor: h.forceColor,
		useColor:   h.useColor,
		preAttrs:   preAttrs,
		group:      group,
	}
}

// deprioritizedKeys are correlation IDs and similar opaque identifiers that
// crowd the meaningful content; they are rendered after every other attr.
var deprioritizedKeys = map[string]struct{}{
	"request_id":     {},
	"session_id":     {},
	"principal_id":   {},
	"account_id":     {},
	"client_call_id": {},
	"remote_addr":    {},
}

// formatRecord writes the full rendered line to buf.
//
// Output order:
//  1. Timestamp (HH:MM:SS.mmm local)
//  2. Level (4-char: INFO, WARN, ERRO, DEBG, TRCE)
//  3. [subsystem] tag (always shown when subsystem or module is set; module
//     is the fallback when subsystem is unset)
//  4. Message
//  5. Domain-meaningful attrs in original record order
//  6. Correlation IDs (deprioritizedKeys) in lex order, last
//
// "activity", "subsystem", "module" are NEVER rendered as trailing attrs:
// they shape the record but are not display content (the operator already
// chose which sink they're reading; the JSON sink and filter dimension still
// see them).
func (h *ConsoleHandler) formatRecord(buf *bytes.Buffer, r slog.Record) {
	// Collect attrs preserving insertion order (pre-scoped first, then record).
	type kv struct {
		key string
		val string
	}
	ordered := make([]kv, 0, len(h.preAttrs)+r.NumAttrs())
	seen := make(map[string]int, len(h.preAttrs)+r.NumAttrs())
	addAttr := func(a slog.Attr) {
		if a.Key == "" {
			return
		}
		v := renderAttrValue(a.Value)
		if i, ok := seen[a.Key]; ok {
			ordered[i].val = v
			return
		}
		seen[a.Key] = len(ordered)
		ordered = append(ordered, kv{key: a.Key, val: v})
	}
	for _, a := range h.preAttrs {
		addAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		addAttr(a)
		return true
	})

	subsystem := ""
	module := ""
	sourceClient := false
	clientApp := ""
	clientStack := ""
	for _, p := range ordered {
		switch p.key {
		case "subsystem":
			subsystem = p.val
		case "module":
			module = p.val
		case "source":
			if p.val == "client" {
				sourceClient = true
			}
		case "app":
			// Collect regardless; used only when sourceClient is true.
			clientApp = p.val
		case "stack":
			clientStack = p.val
		}
	}

	// 1. Timestamp.
	now := h.clk.Now()
	ts := now.Format("15:04:05.000")
	if h.useColor {
		buf.WriteString(ansiGray)
	}
	buf.WriteString(ts)
	if h.useColor {
		buf.WriteString(ansiReset)
	}
	buf.WriteByte(' ')

	// 2. Level.
	lvlStr, lvlColor := levelStr(r.Level)
	if h.useColor && lvlColor != "" {
		buf.WriteString(lvlColor)
		buf.WriteString(ansiBold)
	}
	buf.WriteString(lvlStr)
	if h.useColor && lvlColor != "" {
		buf.WriteString(ansiReset)
	}
	buf.WriteByte(' ')

	// 3. Source marker and/or subsystem tag.
	//
	// For source=client records, render a [suite] or [admin] marker in
	// magenta (REQ-OPS-81a) so operators can distinguish browser events from
	// server events at a glance. The colour is only emitted on TTY targets;
	// plain-ASCII output uses the text marker only.
	if sourceClient {
		marker := clientSourceMarker(clientApp)
		if h.useColor {
			buf.WriteString(ansiMagenta)
		}
		buf.WriteString(marker)
		if h.useColor {
			buf.WriteString(ansiReset)
		}
		buf.WriteByte(' ')
	} else {
		tag := subsystem
		if tag == "" {
			tag = module
		}
		if tag != "" {
			if h.useColor {
				buf.WriteString(ansiCyan)
			}
			buf.WriteByte('[')
			buf.WriteString(tag)
			buf.WriteByte(']')
			if h.useColor {
				buf.WriteString(ansiReset)
			}
			buf.WriteByte(' ')
		}
	}

	// 4. Message.
	if h.useColor {
		buf.WriteString(ansiBold)
	}
	buf.WriteString(r.Message)
	if h.useColor {
		buf.WriteString(ansiReset)
	}

	// 5/6. Trailing attrs.
	//
	// For source=client records, "source", "app", and "stack" are excluded
	// from the inline key=value list: "source" is implicit from the marker,
	// "app" is implicit from the marker, and "stack" is rendered as indented
	// continuation lines after the main line (see step 7 below).
	primary := make([]kv, 0, len(ordered))
	deferred := make([]kv, 0, 4)
	for _, p := range ordered {
		switch p.key {
		case "subsystem", "module", "activity":
			continue
		}
		if sourceClient {
			switch p.key {
			case "source", "app", "stack":
				continue
			}
		}
		if _, late := deprioritizedKeys[p.key]; late {
			deferred = append(deferred, p)
			continue
		}
		primary = append(primary, p)
	}
	sort.Slice(deferred, func(i, j int) bool { return deferred[i].key < deferred[j].key })

	writePair := func(p kv) {
		buf.WriteByte(' ')
		if h.useColor {
			buf.WriteString(ansiGray)
		}
		buf.WriteString(p.key)
		if h.useColor {
			buf.WriteString(ansiReset)
		}
		buf.WriteByte('=')
		lines := strings.Split(p.val, "\n")
		if needsQuote(lines[0]) {
			fmt.Fprintf(buf, "%q", lines[0])
		} else {
			buf.WriteString(lines[0])
		}
		for _, extra := range lines[1:] {
			buf.WriteString("\n  | ")
			buf.WriteString(extra)
		}
	}
	for _, p := range primary {
		writePair(p)
	}
	for _, p := range deferred {
		writePair(p)
	}
	buf.WriteByte('\n')

	// 7. For source=client records, render the stack trace as indented lines
	// under the parent line (REQ-OPS-81a, architecture §Console rendering).
	// Only emitted when the stack field is non-empty.
	if sourceClient && clientStack != "" {
		stackLines := strings.Split(clientStack, "\n")
		for _, sl := range stackLines {
			buf.WriteString("  | ")
			buf.WriteString(sl)
			buf.WriteByte('\n')
		}
	}
}

// clientSourceMarker returns the [suite] or [admin] marker text for a given
// app value.
func clientSourceMarker(app string) string {
	switch strings.ToLower(app) {
	case "admin":
		return "[admin]"
	default:
		return "[suite]"
	}
}

// levelStr returns a 4-char level abbreviation and ANSI colour for it.
func levelStr(lvl slog.Level) (string, string) {
	switch {
	case lvl <= LevelTrace:
		return "TRCE", ""
	case lvl <= slog.LevelDebug:
		return "DEBG", ansiGray
	case lvl <= slog.LevelInfo:
		return "INFO", ansiGreen
	case lvl <= slog.LevelWarn:
		return "WARN", ansiYellow
	default:
		return "ERRO", ansiRed
	}
}

// renderAttrValue converts a slog.Value to a string for display.
func renderAttrValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindGroup:
		parts := v.Group()
		pairs := make([]string, 0, len(parts))
		for _, a := range parts {
			pairs = append(pairs, a.Key+"="+renderAttrValue(a.Value))
		}
		return "{" + strings.Join(pairs, " ") + "}"
	default:
		return v.String()
	}
}

// needsQuote reports whether a value string should be double-quoted in output:
// when it contains a space, '=', or is empty.
func needsQuote(s string) bool {
	return s == "" || strings.ContainsAny(s, " =\t\r\n\"")
}
