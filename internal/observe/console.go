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
	ansiReset  = "\x1b[0m"
	ansiGray   = "\x1b[2m"
	ansiBold   = "\x1b[1m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
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

// formatRecord writes the full rendered line to buf.
//
// Output order (REQ-OPS-83):
//  1. Timestamp (HH:MM:SS.mmm local)
//  2. Level (4-char: INFO, WARN, ERRO, DEBG, TRCE)
//  3. subsystem|module (if present in pre-scoped or record attrs)
//  4. Message
//  5. activity (if present)
//  6. request_id, session_id, principal_id (if present)
//  7. All other attrs, lexicographically sorted, key=value aligned
func (h *ConsoleHandler) formatRecord(buf *bytes.Buffer, r slog.Record) {
	// Collect all attrs (pre-scoped first, then record attrs).
	all := make([]slog.Attr, 0, len(h.preAttrs)+r.NumAttrs())
	all = append(all, h.preAttrs...)
	r.Attrs(func(a slog.Attr) bool {
		all = append(all, a)
		return true
	})

	// Index attrs by key for ordered output.
	attrMap := make(map[string]string, len(all))
	var extraKeys []string
	for _, a := range all {
		k := a.Key
		v := renderAttrValue(a.Value)
		attrMap[k] = v
		switch k {
		case "subsystem", "module", "activity",
			"request_id", "session_id", "principal_id":
			// handled in the ordered section
		default:
			extraKeys = append(extraKeys, k)
		}
	}
	sort.Strings(extraKeys)

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

	// 3. subsystem|module.
	subsystem := attrMap["subsystem"]
	module := attrMap["module"]
	if subsystem != "" || module != "" {
		if h.useColor {
			buf.WriteString(ansiCyan)
		}
		if subsystem != "" && module != "" {
			buf.WriteString(subsystem)
			buf.WriteByte('|')
			buf.WriteString(module)
		} else {
			buf.WriteString(subsystem + module)
		}
		if h.useColor {
			buf.WriteString(ansiReset)
		}
		buf.WriteByte(' ')
	}

	// 4. Message.
	if h.useColor {
		buf.WriteString(ansiBold)
	}
	buf.WriteString(r.Message)
	if h.useColor {
		buf.WriteString(ansiReset)
	}

	// 5..7. Ordered important attrs + extras. Compute max key width for
	// alignment within this record.
	orderedKeys := []string{}
	for _, k := range []string{"activity", "request_id", "session_id", "principal_id"} {
		if _, ok := attrMap[k]; ok {
			orderedKeys = append(orderedKeys, k)
		}
	}
	orderedKeys = append(orderedKeys, extraKeys...)
	// Also skip subsystem/module from the trailing attrs since they were
	// already rendered in position 3.
	finalKeys := make([]string, 0, len(orderedKeys))
	for _, k := range orderedKeys {
		if k == "subsystem" || k == "module" {
			continue
		}
		finalKeys = append(finalKeys, k)
	}

	if len(finalKeys) == 0 {
		buf.WriteByte('\n')
		return
	}

	// Compute max key width for alignment.
	maxW := 0
	for _, k := range finalKeys {
		if len(k) > maxW {
			maxW = len(k)
		}
	}

	for _, k := range finalKeys {
		v := attrMap[k]
		buf.WriteByte(' ')
		if h.useColor {
			buf.WriteString(ansiGray)
		}
		padded := fmt.Sprintf("%-*s", maxW, k)
		buf.WriteString(padded)
		if h.useColor {
			buf.WriteString(ansiReset)
		}
		buf.WriteByte('=')
		// Multi-line values: first line inline, subsequent lines indented.
		lines := strings.Split(v, "\n")
		if needsQuote(lines[0]) {
			buf.WriteString(fmt.Sprintf("%q", lines[0]))
		} else {
			buf.WriteString(lines[0])
		}
		for _, extra := range lines[1:] {
			buf.WriteString("\n  | ")
			buf.WriteString(extra)
		}
	}
	buf.WriteByte('\n')
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
