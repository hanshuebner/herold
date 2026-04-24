//go:build spike

// Package spike compares candidate MIME parsers against the hand-crafted
// corpus under internal/mailparse/testdata/spike/. This file is build-tag
// gated so it only runs when explicitly invoked with -tags spike.
package spike

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jhillyerd/enmime"
)

// partKind classifies a decoded part into the three buckets we care about
// for FTS + attachment pipelines.
type partKind int

const (
	kindText partKind = iota
	kindHTML
	kindAttachment
)

// partInfo captures the minimum metadata we want to compare between parsers.
type partInfo struct {
	kind        partKind
	contentType string
	filename    string
}

// parseResult captures the outcome of running one parser against one file.
type parseResult struct {
	parser      string
	file        string
	panicked    bool
	panicMsg    string
	err         error
	topMIME     string
	parts       []partInfo
	textCount   int
	htmlCount   int
	attachCount int
}

// corpusRecord pairs one file with the results from each parser.
type corpusRecord struct {
	file    string
	results map[string]*parseResult
}

// runEnmime parses raw bytes with jhillyerd/enmime and returns a result.
// Recovers from panics so the harness never aborts.
func runEnmime(file string, data []byte) (r *parseResult) {
	r = &parseResult{parser: "enmime", file: file}
	defer func() {
		if p := recover(); p != nil {
			r.panicked = true
			r.panicMsg = fmt.Sprintf("%v", p)
		}
	}()
	env, err := enmime.ReadEnvelope(strings.NewReader(string(data)))
	if err != nil {
		r.err = err
		if env == nil {
			return r
		}
	}
	if env.Root != nil {
		mt, _, perr := mime.ParseMediaType(env.Root.Header.Get("Content-Type"))
		if perr == nil {
			r.topMIME = mt
		} else if ct := env.Root.ContentType; ct != "" {
			r.topMIME = ct
		}
	}
	// Text body.
	if strings.TrimSpace(env.Text) != "" {
		r.parts = append(r.parts, partInfo{kind: kindText, contentType: "text/plain"})
		r.textCount++
	}
	if strings.TrimSpace(env.HTML) != "" {
		r.parts = append(r.parts, partInfo{kind: kindHTML, contentType: "text/html"})
		r.htmlCount++
	}
	for _, a := range env.Attachments {
		r.parts = append(r.parts, partInfo{kind: kindAttachment, contentType: a.ContentType, filename: a.FileName})
		r.attachCount++
	}
	for _, a := range env.Inlines {
		r.parts = append(r.parts, partInfo{kind: kindAttachment, contentType: a.ContentType, filename: a.FileName})
		r.attachCount++
	}
	for _, a := range env.OtherParts {
		r.parts = append(r.parts, partInfo{kind: kindAttachment, contentType: a.ContentType, filename: a.FileName})
		r.attachCount++
	}
	return r
}

// walkStdlibPart recursively classifies a multipart part using only the stdlib.
func walkStdlibPart(header mail.Header, body io.Reader, out *parseResult, depth int) error {
	if depth > 16 {
		return fmt.Errorf("nested depth limit exceeded")
	}
	ctHeader := header.Get("Content-Type")
	if ctHeader == "" {
		ctHeader = "text/plain; charset=us-ascii"
	}
	mt, params, err := mime.ParseMediaType(ctHeader)
	if err != nil {
		// Tolerate malformed Content-Type — treat as text/plain.
		mt = "text/plain"
		params = map[string]string{}
	}
	if depth == 0 {
		out.topMIME = mt
	}
	switch {
	case strings.HasPrefix(mt, "multipart/"):
		boundary := params["boundary"]
		if boundary == "" {
			return fmt.Errorf("multipart without boundary")
		}
		mr := multipart.NewReader(body, boundary)
		for {
			p, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			if perr != nil {
				// Soft-fail: record and move on.
				return perr
			}
			// Build a mail.Header from textproto.MIMEHeader so the recursion
			// can use the same helper.
			mh := mail.Header{}
			for k, v := range p.Header {
				mh[k] = v
			}
			if werr := walkStdlibPart(mh, p, out, depth+1); werr != nil {
				return werr
			}
		}
	case mt == "message/rfc822":
		inner, perr := mail.ReadMessage(body)
		if perr != nil {
			return perr
		}
		return walkStdlibPart(inner.Header, inner.Body, out, depth+1)
	case strings.HasPrefix(mt, "text/"):
		// Decode Content-Disposition to decide attachment vs body.
		disp := header.Get("Content-Disposition")
		filename := ""
		isAttach := false
		if disp != "" {
			_, dparams, derr := mime.ParseMediaType(disp)
			if derr == nil {
				filename = dparams["filename"]
				if strings.HasPrefix(disp, "attachment") {
					isAttach = true
				}
			}
		}
		// Consume body so subsequent parts can be read.
		_, _ = io.Copy(io.Discard, body)
		if isAttach {
			out.parts = append(out.parts, partInfo{kind: kindAttachment, contentType: mt, filename: filename})
			out.attachCount++
		} else if mt == "text/html" {
			out.parts = append(out.parts, partInfo{kind: kindHTML, contentType: mt})
			out.htmlCount++
		} else {
			out.parts = append(out.parts, partInfo{kind: kindText, contentType: mt})
			out.textCount++
		}
	default:
		// Attachment / non-text.
		disp := header.Get("Content-Disposition")
		filename := params["name"]
		if disp != "" {
			_, dparams, derr := mime.ParseMediaType(disp)
			if derr == nil && dparams["filename"] != "" {
				filename = dparams["filename"]
			}
		}
		// RFC 2047 decode.
		if filename != "" {
			dec := new(mime.WordDecoder)
			if d, derr := dec.DecodeHeader(filename); derr == nil {
				filename = d
			}
		}
		_, _ = io.Copy(io.Discard, body)
		out.parts = append(out.parts, partInfo{kind: kindAttachment, contentType: mt, filename: filename})
		out.attachCount++
	}
	return nil
}

// runStdlib parses raw bytes with net/mail + mime/multipart only.
func runStdlib(file string, data []byte) (r *parseResult) {
	r = &parseResult{parser: "stdlib", file: file}
	defer func() {
		if p := recover(); p != nil {
			r.panicked = true
			r.panicMsg = fmt.Sprintf("%v", p)
		}
	}()
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		r.err = err
		return r
	}
	// Decode subject for completeness (exercises RFC 2047 path).
	dec := new(mime.WordDecoder)
	_, _ = dec.DecodeHeader(msg.Header.Get("Subject"))
	if werr := walkStdlibPart(msg.Header, msg.Body, r, 0); werr != nil {
		// Soft-fail: keep whatever we collected so far.
		r.err = werr
	}
	return r
}

// classify turns a result into a one-word status.
func classify(r *parseResult) string {
	if r.panicked {
		return "panic"
	}
	if r.err != nil && len(r.parts) == 0 {
		return "hard-fail"
	}
	if r.err != nil {
		return "soft-fail"
	}
	if len(r.parts) == 0 {
		return "hard-fail"
	}
	return "pass"
}

func TestMIMEParserSpike(t *testing.T) {
	corpusDir := filepath.Join("..", "testdata", "spike")
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".eml") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)

	records := make([]corpusRecord, 0, len(files))
	for _, name := range files {
		path := filepath.Join(corpusDir, name)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Errorf("%s: read failed: %v", name, rerr)
			continue
		}
		rec := corpusRecord{file: name, results: map[string]*parseResult{}}
		rec.results["enmime"] = runEnmime(name, data)
		rec.results["stdlib"] = runStdlib(name, data)
		records = append(records, rec)
	}

	// Emit a CSV-shaped summary so it can be pasted into the report.
	t.Logf("CSV,file,enmime_status,enmime_mime,enmime_text,enmime_html,enmime_attach,enmime_note,stdlib_status,stdlib_mime,stdlib_text,stdlib_html,stdlib_attach,stdlib_note")
	for _, rec := range records {
		en := rec.results["enmime"]
		sl := rec.results["stdlib"]
		t.Logf("CSV,%s,%s,%s,%d,%d,%d,%q,%s,%s,%d,%d,%d,%q",
			rec.file,
			classify(en), en.topMIME, en.textCount, en.htmlCount, en.attachCount, noteOf(en),
			classify(sl), sl.topMIME, sl.textCount, sl.htmlCount, sl.attachCount, noteOf(sl),
		)
	}

	// Print detailed per-record breakdown for any non-pass cell.
	for _, rec := range records {
		for _, pname := range []string{"enmime", "stdlib"} {
			r := rec.results[pname]
			if classify(r) == "pass" {
				continue
			}
			t.Logf("DETAIL %s %s status=%s err=%v panic=%v parts=%d",
				rec.file, pname, classify(r), r.err, r.panicked, len(r.parts))
		}
	}
}

func noteOf(r *parseResult) string {
	if r.panicked {
		return "panic: " + r.panicMsg
	}
	if r.err != nil {
		msg := r.err.Error()
		if len(msg) > 80 {
			msg = msg[:80]
		}
		return msg
	}
	return ""
}
