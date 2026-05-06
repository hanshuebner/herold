package storefts

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/jaytaylor/html2text"
	"github.com/ledongthuc/pdf"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
)

// Attachment-extraction defaults. Source: docs/design/server/architecture/
// 02-storage-architecture.md §FTS — "per-attachment max text size
// (default 5 MB) + per-message max total extracted text (default 20 MB).
// Exceeding: silently truncated with a counter."
const (
	defaultPerAttachmentMaxBytes = 5 * 1024 * 1024
	defaultPerMessageMaxBytes    = 20 * 1024 * 1024
)

// extractAttachmentText routes a single attachment Part to the format
// handler that matches its Content-Type and returns the extracted plain
// text capped at maxBytes. The boolean reports whether the cap was hit
// (so the caller can record the truncation in the metric). Unrecognised
// formats return "" with no error and no counter bump.
func extractAttachmentText(p mailparse.Part, maxBytes int) (text string, format string, truncated bool, err error) {
	ct := strings.ToLower(strings.TrimSpace(p.ContentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	// mailparse decodes text/* parts into p.Text and leaves p.Bytes
	// empty (parse.go:395). Non-text parts get the raw decoded bytes
	// in p.Bytes. partRaw normalises that for text-based attachments
	// so HTML extraction sees the body even when enmime took the
	// Text-decoded path.
	switch {
	case ct == "text/html":
		t, err := html2text.FromString(partRaw(p), html2text.Options{OmitLinks: true})
		if err != nil {
			return "", "html", false, fmt.Errorf("html2text: %w", err)
		}
		out, trunc := capString(t, maxBytes)
		return out, "html", trunc, nil
	case strings.HasPrefix(ct, "text/"):
		out, trunc := capString(partRaw(p), maxBytes)
		return out, "text", trunc, nil
	case ct == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		t, err := extractOOXMLText(p.Bytes, "docx")
		if err != nil {
			return "", "docx", false, err
		}
		out, trunc := capString(t, maxBytes)
		return out, "docx", trunc, nil
	case ct == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		t, err := extractOOXMLText(p.Bytes, "xlsx")
		if err != nil {
			return "", "xlsx", false, err
		}
		out, trunc := capString(t, maxBytes)
		return out, "xlsx", trunc, nil
	case ct == "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		t, err := extractOOXMLText(p.Bytes, "pptx")
		if err != nil {
			return "", "pptx", false, err
		}
		out, trunc := capString(t, maxBytes)
		return out, "pptx", trunc, nil
	case ct == "application/pdf":
		t, err := extractPDFText(p.Bytes, maxBytes)
		if err != nil {
			return "", "pdf", false, err
		}
		// extractPDFText already enforces maxBytes during the read, so
		// the returned string is at most maxBytes long; trunc is true
		// when the reader was cut short.
		trunc := maxBytes > 0 && len(t) >= maxBytes
		return t, "pdf", trunc, nil
	default:
		return "", "skipped", false, nil
	}
}

// extractPDFText reads the text layer of a PDF blob and returns up to
// maxBytes of plain text. Encrypted PDFs and malformed structures
// surface as errors; the caller records them in the format=pdf,
// outcome=error counter and skips the attachment. Per the architecture
// spec we do not OCR images or rasterised pages.
//
// A defensive recover() is wrapped around the parse: ledongthuc/pdf
// uses panic for some malformed-input branches and would otherwise
// crash the FTS worker. Recovering converts that into a normal error
// so the worker logs and moves on.
func extractPDFText(blob []byte, maxBytes int) (out string, err error) {
	if len(blob) == 0 {
		return "", nil
	}
	defer func() {
		if r := recover(); r != nil {
			out = ""
			err = fmt.Errorf("storefts: pdf parse panic: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		return "", fmt.Errorf("storefts: pdf reader: %w", err)
	}
	rd, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("storefts: pdf text: %w", err)
	}
	limit := maxBytes
	if limit <= 0 {
		limit = defaultPerAttachmentMaxBytes
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < limit {
		n, rerr := rd.Read(tmp)
		if n > 0 {
			room := limit - len(buf)
			if n > room {
				n = room
			}
			buf = append(buf, tmp[:n]...)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", fmt.Errorf("storefts: pdf read: %w", rerr)
		}
	}
	return string(buf), nil
}

// extractOOXMLText walks every XML file inside an OOXML zip and
// concatenates the character data of every element whose local name is
// "t" (DOCX <w:t>, PPTX <a:t>, XLSX <t> in shared strings and inline
// cell strings). Whitespace between text runs is preserved as a single
// space; line breaks (<w:br/>) become newlines.
//
// kind is used only for diagnostics ("docx" / "xlsx" / "pptx").
func extractOOXMLText(blob []byte, kind string) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		return "", fmt.Errorf("storefts: %s zip: %w", kind, err)
	}
	var buf strings.Builder
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			continue
		}
		// Only docs that carry runtime text. Drop _rels, theme,
		// settings, fontTable, styles, app.xml, core.xml, etc. so we
		// do not pull in stylesheet metadata.
		if !ooxmlContentBearing(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("storefts: %s open %s: %w", kind, f.Name, err)
		}
		err = streamOOXMLText(rc, &buf)
		_ = rc.Close()
		if err != nil {
			return "", fmt.Errorf("storefts: %s parse %s: %w", kind, f.Name, err)
		}
	}
	return buf.String(), nil
}

// ooxmlContentBearing reports whether the named entry inside an OOXML
// zip carries user-visible text. We walk only document/sheet/slide
// content and shared strings; metadata (styles, theme, settings,
// _rels) is excluded so the index does not get polluted with format
// boilerplate.
func ooxmlContentBearing(name string) bool {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "word/document"):
		return true
	case strings.HasPrefix(n, "word/header"), strings.HasPrefix(n, "word/footer"):
		return true
	case strings.HasPrefix(n, "word/footnotes"), strings.HasPrefix(n, "word/endnotes"):
		return true
	case strings.HasPrefix(n, "ppt/slides/slide"):
		return true
	case strings.HasPrefix(n, "ppt/notesslides/"):
		return true
	case strings.HasPrefix(n, "xl/sharedstrings"):
		return true
	case strings.HasPrefix(n, "xl/worksheets/sheet"):
		return true
	}
	return false
}

// streamOOXMLText walks the XML token stream and writes the chardata of
// every <*:t> element to out, separated by single spaces. <*:br/> and
// <*:p> boundaries become newlines so paragraph breaks survive the
// flattening.
func streamOOXMLText(r io.Reader, out *strings.Builder) error {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	inTextRun := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inTextRun = true
			case "br":
				out.WriteByte('\n')
			case "p":
				// Word/PowerPoint paragraph break.
				if out.Len() > 0 {
					out.WriteByte('\n')
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inTextRun = false
				out.WriteByte(' ')
			case "tr", "row":
				out.WriteByte('\n')
			}
		case xml.CharData:
			if inTextRun {
				out.Write(t)
			}
		}
	}
}

// capString truncates s to a UTF-8-aware byte budget and reports whether
// truncation happened. Truncation falls back to s[:maxBytes] when s is
// not valid UTF-8 around the cut point; the FTS analyzer tolerates
// stray bytes.
func capString(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut], true
}

// partRaw returns the attachment's body bytes as a string, preferring
// p.Bytes (filled for non-text parts) and falling back to p.Text
// (filled for text/* parts). mailparse splits these two fields on the
// text/non-text axis (see internal/mailparse/parse.go:395), so a
// text/html attachment lands in p.Text rather than p.Bytes.
func partRaw(p mailparse.Part) string {
	if len(p.Bytes) > 0 {
		return string(p.Bytes)
	}
	return p.Text
}

// recordExtraction bumps the FTS attachment metric in a closed-vocab
// way. The metric is registered lazily by storefts.NewWorker; calls
// before that no-op on the nil-CounterVec guard.
func recordExtraction(format, outcome string) {
	if observe.FTSAttachmentExtractedTotal == nil {
		return
	}
	observe.FTSAttachmentExtractedTotal.WithLabelValues(format, outcome).Inc()
}
