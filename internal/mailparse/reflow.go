package mailparse

import "strings"

// ReflowFormatFlowed implements RFC 3676 reception-side handling for a
// text/plain body carrying Content-Type parameter format=flowed: it
// space-unstuffs each line, then joins soft-broken lines back into
// flowing paragraphs while preserving quote-depth structure (leading
// '>' markers) and hard breaks (paragraph boundaries, the "-- "
// signature separator).
//
// delSp mirrors the message's delsp=yes Content-Type parameter (RFC
// 3676 sec 4.5): when true, the single trailing space that marks a
// soft break is deleted before joining two lines; when false, that
// space is retained as the word separator between the joined lines.
//
// body's line endings may be CRLF or LF; the returned text always uses
// LF. The function never panics on any input, including malformed or
// arbitrary bytes reinterpreted as a Go string -- it feeds a fuzz
// target (FuzzReflowFormatFlowed).
func ReflowFormatFlowed(body string, delSp bool) string {
	if body == "" {
		return body
	}

	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	trailingNewline := strings.HasSuffix(normalized, "\n")
	rawLines := strings.Split(normalized, "\n")
	if trailingNewline {
		// strings.Split on a string ending in the separator yields a
		// trailing "" element; drop it, we re-append the newline below.
		rawLines = rawLines[:len(rawLines)-1]
	}
	if len(rawLines) == 0 {
		return body
	}

	lines := make([]flowedLine, len(rawLines))
	for i, raw := range rawLines {
		lines[i] = parseFlowedLine(raw)
	}

	var out strings.Builder
	i := 0
	for i < len(lines) {
		depth := lines[i].depth
		var para strings.Builder
		for {
			cur := lines[i]
			joinNext := cur.soft && i+1 < len(lines) && lines[i+1].depth == depth
			if joinNext {
				if delSp {
					para.WriteString(strings.TrimSuffix(cur.content, " "))
				} else {
					para.WriteString(cur.content)
				}
				i++
				continue
			}
			para.WriteString(cur.content)
			i++
			break
		}
		content := para.String()
		out.WriteString(strings.Repeat(">", depth))
		// The quote prefix's display separator is our choice, not part of
		// the RFC 3676 wire protocol: unstuffing above removed the
		// transport stuff-space, which leaves a single leading space on
		// content when the sender double-stuffed for readability (sec
		// 4.4), and none at all otherwise. Normalize to exactly one
		// space after the markers so quoted output always reads as
		// "> text" / ">> text" and matches the Suite's quote-collapse
		// detector (`/^>+(\s|$)/` in quoted.ts's isQuotedLine), instead
		// of gluing the markers directly to the text when the sender
		// didn't double-stuff.
		if depth > 0 && content != "" && !strings.HasPrefix(content, " ") {
			out.WriteByte(' ')
		}
		out.WriteString(content)
		if i < len(lines) {
			out.WriteByte('\n')
		}
	}
	if trailingNewline {
		out.WriteByte('\n')
	}
	return out.String()
}

// flowedLine is a single line of a format=flowed body after quote-depth
// extraction and space-unstuffing.
type flowedLine struct {
	// depth is the number of leading '>' quote markers.
	depth int
	// content is the line's text after the quote markers and the
	// single space-stuffing space (if any) have been removed.
	content string
	// soft reports whether this line ends in a soft break: content
	// ends with exactly one trailing space, and content is not the
	// signature separator "-- ".
	soft bool
}

// parseFlowedLine extracts the quote depth and unstuffs raw per RFC 3676
// sec 4.3/4.4, and classifies the resulting content as a soft or hard
// break.
func parseFlowedLine(raw string) flowedLine {
	depth := 0
	j := 0
	for j < len(raw) && raw[j] == '>' {
		depth++
		j++
	}
	content := raw[j:]
	// Space-unstuffing (sec 4.4): a line whose content (after any quote
	// markers) begins with a space had that space added by the sender
	// to prevent it from being mistaken for a quote marker or an mbox
	// "From " line; remove exactly one such space.
	content = strings.TrimPrefix(content, " ")
	// The signature separator is a hard break even though it ends in a
	// space (sec 4.3): a flowed-aware generator would otherwise have to
	// avoid ending "-- " with a space at all, which would break
	// unaware clients' signature detection.
	if content == "-- " {
		return flowedLine{depth: depth, content: content, soft: false}
	}
	soft := strings.HasSuffix(content, " ")
	return flowedLine{depth: depth, content: content, soft: soft}
}
