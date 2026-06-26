package mailparse

import (
	"fmt"
	"io"
	"strings"
)

// PartIndexVersion is the schema version of the serialized part index. Bump it
// whenever the PartIndexEntry shape or the enumeration changes so the body-index
// worker rebuilds stale indexes and consumers reject indexes they cannot read.
const PartIndexVersion = 1

// PartIndexEntry is the persistable descriptor of one MIME part: the metadata
// and raw byte range needed to stream and decode that part's body directly from
// the original message blob, without re-parsing the message. The Index field
// follows the same 1-based pre-order DFS enumeration that the JMAP renderer uses
// to assign per-part blobId values ("<blobHash>/p<Index>"), so a persisted index
// resolves the same part a fresh parse would.
//
// JSON tags are terse because a full index is serialised once per blob hash and
// stored in the metadata DB; the field set is small and never hand-edited.
type PartIndexEntry struct {
	Index       int    `json:"i"`
	Parent      int    `json:"p,omitempty"`
	ContentType string `json:"ct,omitempty"`
	Charset     string `json:"cs,omitempty"`
	CTE         string `json:"cte,omitempty"`
	Disposition string `json:"disp,omitempty"`
	CID         string `json:"cid,omitempty"`
	Filename    string `json:"fn,omitempty"`
	RawOffset   int64  `json:"off,omitempty"`
	RawLen      int64  `json:"len,omitempty"`
	DecodedSize int64  `json:"sz,omitempty"`
	IsText      bool   `json:"txt,omitempty"`
	// Container is true when the part has children (a multipart/* branch or a
	// message/rfc822 wrapper). Body streaming is defined only for leaves.
	Container bool `json:"cont,omitempty"`
}

// BuildPartIndex walks msg.Body in 1-based pre-order DFS, returning one entry
// per part in the same order the renderer enumerates blobId values. The raw byte
// ranges are relative to the bytes passed to Parse (the stored blob), so callers
// that consume the index must stream from those same bytes.
func BuildPartIndex(msg Message) []PartIndexEntry {
	var out []PartIndexEntry
	idx := 0
	var walk func(p Part, parent int)
	walk = func(p Part, parent int) {
		idx++
		me := idx
		out = append(out, PartIndexEntry{
			Index:       me,
			Parent:      parent,
			ContentType: p.ContentType,
			Charset:     p.Charset,
			CTE:         p.ContentTransferEncoding,
			Disposition: p.Disposition.String(),
			CID:         normalizeCID(p.Headers.Get("Content-ID")),
			Filename:    p.Filename,
			RawOffset:   p.rawOffset,
			RawLen:      p.rawLen,
			DecodedSize: p.Size,
			IsText:      p.IsText(),
			Container:   len(p.Children) > 0,
		})
		for _, c := range p.Children {
			walk(c, me)
		}
	}
	walk(msg.Body, 0)
	return out
}

// normalizeCID strips surrounding angle brackets and whitespace from a
// Content-ID header value, yielding the bare cid token that an HTML
// "cid:<token>" reference carries.
func normalizeCID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// OpenBody streams the CTE-decoded, charset-converted body of this part from
// src, the io.ReaderAt over the same bytes that produced the index (the store
// blob). It performs no message parse: it seeks straight to the part's raw byte
// range and applies the same decoder Part.OpenBody uses. Returns an error for
// container parts or a nil src.
func (e PartIndexEntry) OpenBody(src io.ReaderAt) (io.ReadCloser, error) {
	if src == nil {
		return nil, fmt.Errorf("mailparse: PartIndexEntry.OpenBody: src is nil")
	}
	if e.Container {
		return nil, fmt.Errorf("mailparse: PartIndexEntry.OpenBody: part is a container")
	}
	rawSection := io.NewSectionReader(src, e.RawOffset, e.RawLen)
	return openBodyDecoder(rawSection, e.CTE, e.Charset, e.IsText)
}

// RawBody streams the raw (CTE-encoded, as-received) body of this part from src,
// without decoding. Returns an error for container parts or a nil src.
func (e PartIndexEntry) RawBody(src io.ReaderAt) (io.Reader, error) {
	if src == nil {
		return nil, fmt.Errorf("mailparse: PartIndexEntry.RawBody: src is nil")
	}
	if e.Container {
		return nil, fmt.Errorf("mailparse: PartIndexEntry.RawBody: part is a container")
	}
	return io.NewSectionReader(src, e.RawOffset, e.RawLen), nil
}
