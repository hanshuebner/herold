// Package cliout provides human-readable output helpers for the herold CLI.
//
// Two output modes are supported:
//
//   - Table: a tabwriter-based grid, one row per record, with a header row.
//     Use for list commands (queue list, principal list, etc.).
//   - KV: aligned key/value pairs. Use for single-record commands (show).
//
// Neither mode emits trailing newlines beyond what the underlying tabwriter
// produces; callers should not add extra blank lines.
package cliout

import (
	"fmt"
	"io"
	"math"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

const (
	// MaxErrorLen is the default maximum rune length for error/message
	// columns shown in the human table view. Longer strings are truncated
	// with an ellipsis so a single long error does not break column layout.
	MaxErrorLen = 60

	// MaxHashLen is the number of hex characters shown for blob hashes and
	// similar opaque identifiers when they must appear in the human view.
	MaxHashLen = 12
)

// Table renders a list of maps as a fixed-width tab-separated table.
// Call Header to set column headers, Add to append rows, then Flush to
// write everything to w. Columns that are entirely empty (every row has
// an empty string for that column) are omitted from the output unless
// explicitly added with AddColumn.
type Table struct {
	tw   *tabwriter.Writer
	cols []string
}

// NewTable returns a Table backed by w. Call Flush when done.
func NewTable(w io.Writer) *Table {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	return &Table{tw: tw}
}

// Header writes the header row. It must be called before any Add calls.
func (t *Table) Header(cols ...string) {
	t.cols = cols
	fmt.Fprintln(t.tw, strings.Join(cols, "\t"))
}

// Row appends one data row. values must align with the cols set in Header;
// missing trailing values are treated as empty.
func (t *Table) Row(values ...string) {
	// Pad or trim to len(cols) for alignment.
	row := make([]string, len(t.cols))
	for i := range row {
		if i < len(values) {
			row[i] = values[i]
		}
	}
	fmt.Fprintln(t.tw, strings.Join(row, "\t"))
}

// Flush writes all buffered rows to the underlying writer.
func (t *Table) Flush() error {
	return t.tw.Flush()
}

// KV writes aligned key/value pairs to w. Keys are left-justified to the
// width of the longest key; values follow after two spaces.
func KV(w io.Writer, pairs [][2]string) {
	// Filter empty values first so the padding calculation is correct.
	visible := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		if p[1] != "" {
			visible = append(visible, p)
		}
	}
	maxK := 0
	for _, p := range visible {
		if n := utf8.RuneCountInString(p[0]); n > maxK {
			maxK = n
		}
	}
	for _, p := range visible {
		fmt.Fprintf(w, "%-*s  %s\n", maxK, p[0], p[1])
	}
}

// FormatTime formats a RFC3339 timestamp string as a short local-time
// string ("2006-01-02 15:04:05"). Returns the input unchanged if it is
// not a valid RFC3339 timestamp, and returns "" for an empty input.
func FormatTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return s
		}
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// FormatTimeValue formats a time.Time as a short local-time string.
// Returns "" for the zero value.
func FormatTimeValue(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// Trunc truncates s to at most maxRunes runes, appending "..." if
// truncation occurred. Returns s unchanged when len(runes) <= maxRunes.
func Trunc(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "..."
}

// HumanBytes formats n as a human-readable byte count (B, KB, MB, GB, TB).
// Values below 1 KB are rendered as "N B"; above that the nearest unit with
// one decimal place is used.
func HumanBytes(n int64) string {
	if n == 0 {
		return "0 B"
	}
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case n >= tb:
		return fmt.Sprintf("%.1f TB", float64(n)/float64(tb))
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// TruncHash returns the first maxLen hex characters of a hash string,
// appending "..." if the hash is longer. Returns "" for empty input.
func TruncHash(h string, maxLen int) string {
	if h == "" {
		return ""
	}
	return Trunc(h, maxLen)
}

// BoolYesNo formats a boolean as "yes" or "no".
func BoolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// StringsJoin returns a comma-joined list, or "(none)" for an empty slice.
func StringsJoin(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	return strings.Join(ss, ", ")
}

// FloatStr renders a float64 compactly. Integers are rendered without a
// decimal point; others with enough precision to avoid loss.
func FloatStr(f float64) string {
	if f == math.Trunc(f) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
