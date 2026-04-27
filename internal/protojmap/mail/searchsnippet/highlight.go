package searchsnippet

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// snippetWindow bounds the length of the rendered preview around the
// first matching term. RFC 8621 §6.1 leaves the figure to the server;
// 200 characters is a typical envelope across other JMAP servers and
// fits comfortably in client UI columns.
const snippetWindow = 200

// markOpen / markClose wrap each matching token in the rendered
// snippet. Using <mark> matches the conventional HTML5 highlighting
// element JMAP clients consume.
const (
	markOpen  = "<mark>"
	markClose = "</mark>"
)

// highlight renders text with each occurrence of any term in terms
// wrapped in <mark>..</mark>. Matching is case-insensitive on whole
// tokens (Unicode letter / digit runs); partial matches do not
// highlight. Returns the rendered string verbatim when terms is empty
// (the caller decides what to do with no-term filters).
func highlight(text string, terms []string) string {
	if len(terms) == 0 || text == "" {
		return text
	}
	termSet := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		termSet[t] = struct{}{}
	}
	if len(termSet) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text) + 32)
	i := 0
	for i < len(text) {
		r, sz := utf8.DecodeRuneInString(text[i:])
		if !isWordRune(r) {
			b.WriteRune(r)
			i += sz
			continue
		}
		// Scan a word.
		j := i + sz
		for j < len(text) {
			r2, sz2 := utf8.DecodeRuneInString(text[j:])
			if !isWordRune(r2) {
				break
			}
			j += sz2
		}
		word := text[i:j]
		if _, ok := termSet[strings.ToLower(word)]; ok {
			b.WriteString(markOpen)
			b.WriteString(word)
			b.WriteString(markClose)
		} else {
			b.WriteString(word)
		}
		i = j
	}
	return b.String()
}

// snippet returns a window of text centred on the first occurrence of
// any term, with the matching tokens highlighted. When no term hits,
// returns the leading window of text verbatim. The window is capped
// at snippetWindow runes.
func snippet(text string, terms []string) string {
	text = collapseWhitespace(text)
	if text == "" {
		return ""
	}
	idx := firstMatchIndex(text, terms)
	if idx < 0 {
		return highlight(truncate(text, snippetWindow), terms)
	}
	// Centre the window on idx.
	start := idx - snippetWindow/2
	if start < 0 {
		start = 0
	}
	// Snap to a rune boundary.
	for start > 0 {
		if utf8.RuneStart(text[start]) {
			break
		}
		start--
	}
	end := start + snippetWindow
	if end > len(text) {
		end = len(text)
	}
	for end < len(text) {
		if utf8.RuneStart(text[end]) {
			break
		}
		end++
	}
	prefix := ""
	if start > 0 {
		prefix = "…" // horizontal ellipsis
	}
	suffix := ""
	if end < len(text) {
		suffix = "…"
	}
	return prefix + highlight(text[start:end], terms) + suffix
}

// firstMatchIndex returns the byte offset of the first whole-token
// occurrence of any term in text, or -1 when none match.
func firstMatchIndex(text string, terms []string) int {
	if len(terms) == 0 {
		return -1
	}
	lower := strings.ToLower(text)
	earliest := -1
	for _, term := range terms {
		t := strings.ToLower(strings.TrimSpace(term))
		if t == "" {
			continue
		}
		off := 0
		for {
			rel := strings.Index(lower[off:], t)
			if rel < 0 {
				break
			}
			start := off + rel
			end := start + len(t)
			beforeOK := start == 0 || !isWordRune(decodeRunePrev(text, start))
			afterOK := end == len(text) || !isWordRune(decodeRuneAt(text, end))
			if beforeOK && afterOK {
				if earliest < 0 || start < earliest {
					earliest = start
				}
				break
			}
			off = start + 1
		}
	}
	return earliest
}

// decodeRunePrev returns the rune ending at byte offset i (i.e. the
// last rune in text[:i]).
func decodeRunePrev(text string, i int) rune {
	if i <= 0 {
		return 0
	}
	for j := i - 1; j >= 0 && i-j <= 4; j-- {
		if utf8.RuneStart(text[j]) {
			r, _ := utf8.DecodeRuneInString(text[j:i])
			return r
		}
	}
	return 0
}

// decodeRuneAt returns the rune starting at byte offset i.
func decodeRuneAt(text string, i int) rune {
	if i >= len(text) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(text[i:])
	return r
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// collapseWhitespace folds runs of whitespace into a single space and
// trims leading/trailing spaces. Used to render readable previews
// from MIME-extracted text that may carry CR/LF and indentation.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	out := strings.TrimSuffix(b.String(), " ")
	return out
}

// truncate returns text capped at n runes (not bytes), appending a
// horizontal-ellipsis when truncation occurred.
func truncate(text string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range text {
		count++
		if count > n {
			return text[:i] + "…"
		}
	}
	return text
}
