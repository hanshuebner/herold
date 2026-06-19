package storefts

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/jaytaylor/html2text"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
)

// Attachment-extraction defaults. Source: docs/design/server/architecture/
// 02-storage-architecture.md §FTS — per-attachment + per-message ceilings,
// silently truncated with a counter when exceeded.
//
// Lowered 2026-05-09 from 5 MiB / 20 MiB to 256 KiB / 1 MiB after the FTS
// indexer was observed pinning 144 GiB resident: the old ceilings were
// speculative, search relevance for plain-text body indexing is dominated
// by the first few KiB of an attachment, and the old per-message ceiling
// (20 MiB × thousands of pending docs in a Bleve batch) was the dominant
// term in the worker's resident memory.
const (
	defaultPerAttachmentMaxBytes = 256 * 1024
	defaultPerMessageMaxBytes    = 1 * 1024 * 1024
)

// extractAttachmentText routes a single attachment Part to the format
// handler that matches its Content-Type and returns the extracted plain
// text capped at maxBytes. The boolean reports whether the cap was hit
// (so the caller can record the truncation in the metric). Unrecognised
// formats return "" with no error and no counter bump.
//
// src is the io.ReaderAt for the message raw bytes, used to stream
// non-text part bodies via p.OpenBody. It may be nil when the caller
// knows only text/* parts will be processed.
func extractAttachmentText(p mailparse.Part, src io.ReaderAt, maxBytes int) (text string, format string, truncated bool, err error) {
	ct := strings.ToLower(strings.TrimSpace(p.ContentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	// mailparse decodes text/* leaves into p.Text. Non-text leaves have
	// no decoded body in RAM; partRaw streams via OpenBody for those.
	switch {
	case ct == "text/html":
		t, err := html2text.FromString(partRaw(p, src), html2text.Options{OmitLinks: true})
		if err != nil {
			return "", "html", false, fmt.Errorf("html2text: %w", err)
		}
		out, trunc := capString(t, maxBytes)
		return out, "html", trunc, nil
	case strings.HasPrefix(ct, "text/"):
		out, trunc := capString(partRaw(p, src), maxBytes)
		return out, "text", trunc, nil
	case ct == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		blob, berr := readPartBody(p, src)
		if berr != nil {
			return "", "docx", false, berr
		}
		t, err := extractOOXMLText(blob, "docx")
		if err != nil {
			return "", "docx", false, err
		}
		out, trunc := capString(t, maxBytes)
		return out, "docx", trunc, nil
	case ct == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		blob, berr := readPartBody(p, src)
		if berr != nil {
			return "", "xlsx", false, berr
		}
		t, err := extractOOXMLText(blob, "xlsx")
		if err != nil {
			return "", "xlsx", false, err
		}
		out, trunc := capString(t, maxBytes)
		return out, "xlsx", trunc, nil
	case ct == "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		blob, berr := readPartBody(p, src)
		if berr != nil {
			return "", "pptx", false, berr
		}
		t, err := extractOOXMLText(blob, "pptx")
		if err != nil {
			return "", "pptx", false, err
		}
		out, trunc := capString(t, maxBytes)
		return out, "pptx", trunc, nil
	case ct == "application/pdf":
		// In-process PDF extraction is disabled per REQ-PDFEX-110: a single
		// pathological PDF in production drove the prior pure-Go parser past
		// 100 GiB resident heap (verified via /debug/pprof/heap; 100% of
		// in-use bytes were one goroutine stuck in pdf.(*buffer).readArray).
		// The full fix is the subprocess + pdftotext wrapper specified in
		// docs/design/server/requirements/20-pdf-extraction-isolation.md;
		// this stopgap routes every PDF to "disabled" so the FTS worker
		// advances past such messages instead of pinning the process.
		// Sentinel format value is consumed by appendAttachmentText to emit
		// the right metric.
		return "", formatPDFDisabled, false, nil
	default:
		return "", "skipped", false, nil
	}
}

// formatPDFDisabled is the sentinel format value returned by
// extractAttachmentText for application/pdf parts during the stopgap
// window (REQ-PDFEX-110). appendAttachmentText emits the metric and
// drops the part. Replaced by the subprocess wrapper specified in
// docs/design/server/requirements/20-pdf-extraction-isolation.md.
const formatPDFDisabled = "pdf-disabled"

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

// partRaw returns the attachment's body as a string. For text/* parts
// the decoded content is already in p.Text. For non-text parts (e.g.
// text/html treated as an attachment) we stream via OpenBody when src
// is available; if src is nil or the open fails we return empty.
func partRaw(p mailparse.Part, src io.ReaderAt) string {
	if p.Text != "" {
		return p.Text
	}
	if src == nil {
		return ""
	}
	blob, err := readPartBody(p, src)
	if err != nil {
		return ""
	}
	return string(blob)
}

// readPartBody opens p.OpenBody(src) and reads all bytes into memory.
// It is used by extractAttachmentText for OOXML and similar formats
// that require random-access (zip.NewReader). The caller is responsible
// for enforcing any size cap before calling this function.
func readPartBody(p mailparse.Part, src io.ReaderAt) ([]byte, error) {
	if src == nil {
		return nil, nil
	}
	rc, err := p.OpenBody(src)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
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
