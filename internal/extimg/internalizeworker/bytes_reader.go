package internalizeworker

import "bytes"

// bytesReader is a thin wrapper around bytes.NewReader so the worker
// keeps its blob-write call (Blobs().Put expects io.Reader) free of
// extra dependencies. Kept in its own file to make the import-graph
// review trivial.
func bytesReader(b []byte) *bytes.Reader {
	return bytes.NewReader(b)
}
