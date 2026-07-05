package mailparse

import (
	"bytes"
	"mime"
	"strings"

	"golang.org/x/net/html"
)

// isLatinSingleByteCharset reports whether charset is one of the common
// single-byte Latin encodings that are frequently mislabelled on real-world
// UTF-8 HTML mail. Windows-1252 is the htmlindex canonical form of ISO-8859-1
// and covers the most common mislabelling pattern.
func isLatinSingleByteCharset(charset string) bool {
	norm := strings.ToLower(strings.TrimSpace(charset))
	switch norm {
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin-1", "latin1",
		"windows-1252", "cp1252", "win-1252", "x-windows-1252",
		"iso-8859-15", "iso8859-15", "iso-8859-2", "iso8859-2",
		"iso-8859-3", "iso8859-3", "iso-8859-4", "iso8859-4",
		"iso-8859-5", "iso8859-5", "iso-8859-7", "iso8859-7",
		"iso-8859-9", "iso8859-9":
		return true
	}
	return false
}

// isUTF8Charset reports whether charset names a UTF-8 encoding.
func isUTF8Charset(charset string) bool {
	norm := strings.ToLower(strings.TrimSpace(charset))
	return norm == "utf-8" || norm == "utf8"
}

// extractHTMLMetaCharset scans the first 4096 bytes of an HTML document and
// returns the charset declared in a <meta> element, or "" when none is found.
//
// Two declaration forms are recognised:
//   - HTML5:  <meta charset="UTF-8">
//   - HTML4:  <meta http-equiv="Content-Type" content="text/html; charset=UTF-8">
//
// The scan stops at the first </head> or <body> tag and never reads beyond 4096
// bytes, so it is safe to call on large documents.
func extractHTMLMetaCharset(b []byte) string {
	const scanLimit = 4096
	if len(b) > scanLimit {
		b = b[:scanLimit]
	}
	z := html.NewTokenizer(bytes.NewReader(b))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""

		case html.StartTagToken, html.SelfClosingTagToken:
			tagName, hasAttr := z.TagName()
			tagStr := strings.ToLower(string(tagName))
			// Stop scanning once <body> begins — charset meta is always in <head>.
			if tagStr == "body" {
				return ""
			}
			if tagStr != "meta" || !hasAttr {
				continue
			}
			var charsetAttr, httpEquiv, contentAttr string
			for {
				k, v, more := z.TagAttr()
				switch strings.ToLower(string(k)) {
				case "charset":
					charsetAttr = strings.TrimSpace(string(v))
				case "http-equiv":
					httpEquiv = strings.TrimSpace(string(v))
				case "content":
					contentAttr = strings.TrimSpace(string(v))
				}
				if !more {
					break
				}
			}
			// HTML5: <meta charset="UTF-8">
			if charsetAttr != "" {
				return charsetAttr
			}
			// HTML4: <meta http-equiv="Content-Type" content="text/html; charset=UTF-8">
			if strings.EqualFold(httpEquiv, "Content-Type") && contentAttr != "" {
				_, params, err := mime.ParseMediaType(contentAttr)
				if err == nil {
					if cs := params["charset"]; cs != "" {
						return strings.TrimSpace(cs)
					}
				}
			}

		case html.EndTagToken:
			tagName, _ := z.TagName()
			if strings.ToLower(string(tagName)) == "head" {
				return ""
			}
		}
	}
}
