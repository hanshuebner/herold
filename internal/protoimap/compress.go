// COMPRESS=DEFLATE (RFC 4978) — opt-in wire compression for
// authenticated IMAP sessions.
//
// After a client issues "COMPRESS DEFLATE" we wrap the connection's
// reader and writer with compress/flate streams. Both directions use
// raw DEFLATE (no zlib/gzip wrapper) per RFC 4978 §4. The compression
// is sticky — there is no inverse command — and the underlying socket
// is unchanged (so STARTTLS / TLS state is preserved).
//
// Zip-bomb mitigation. flate.NewReader is a streaming decoder; it does
// not allocate based on the compressed payload's claimed size, so a
// pathological "small in, huge out" stream cannot pre-allocate a giant
// buffer. We also keep the per-session command budget in place: a
// single ridiculously-large COMPRESS-decoded line is bounded by the
// existing maxLineLength tokeniser cap (64 KiB per non-literal line).
// The literal reader still applies its maxAppendLiteral ceiling. The
// remaining attack surface — many small commands, each expanding 100x —
// is bounded by MaxCommandsPerSession.

package protoimap

import (
	"bufio"
	"compress/flate"
	"errors"
	"io"
)

// deflateConn is the io.ReadWriteCloser pair installed on the session
// after a successful COMPRESS DEFLATE. It owns the flate readers and
// writers and forwards Close to the underlying transport (the
// flate streams themselves are flushed but never closed; closing the
// flate.Writer would emit the end-of-stream marker, which we do only
// when the session terminates).
type deflateConn struct {
	raw    io.Closer
	reader io.Reader
	writer *flate.Writer
}

// compressConnReader returns the bufio-wrapped reader the session must
// adopt after COMPRESS DEFLATE. flate.NewReader returns a
// io.ReadCloser; we keep the closer in dc so the session shutdown path
// can close it.
func (dc *deflateConn) Read(p []byte) (int, error) {
	return dc.reader.Read(p)
}

// Write writes p through the deflate stream. Per RFC 4978 §3 the server
// MUST flush after every IMAP response so the client sees full lines
// without having to buffer half of them; we Flush after each write.
func (dc *deflateConn) Write(p []byte) (int, error) {
	n, err := dc.writer.Write(p)
	if err != nil {
		return n, err
	}
	return n, dc.writer.Flush()
}

// Close ends the deflate streams and closes the underlying transport.
// Errors from the flate streams are reported but do not mask the
// transport's Close error.
func (dc *deflateConn) Close() error {
	werr := dc.writer.Close()
	cerr := dc.raw.Close()
	if cerr != nil {
		return cerr
	}
	return werr
}

// installDeflate wraps the session's bufio.Reader and respWriter with a
// DEFLATE stream. The raw transport is preserved on the session so TLS /
// remote-addr / read-deadline operations continue to work. Returns an
// error only if the flate.NewWriter call fails (effectively impossible
// with a fixed compression level, but we honour the error path).
func (ses *session) installDeflate() error {
	raw := ses.conn
	if raw == nil {
		return errors.New("protoimap: COMPRESS without active conn")
	}
	// We read directly from the raw conn (not the existing bufio.Reader)
	// because the bufio buffer may already contain post-COMPRESS bytes
	// — but RFC 4978 §3 requires the COMPRESS tagged response to be the
	// last uncompressed byte on the wire. The session sends the OK
	// response before calling installDeflate; any buffered bytes after
	// the OK are already part of the compressed stream. We therefore
	// hand the *existing* buffered reader to flate.NewReader so the
	// already-consumed buffer prefix is honoured.
	zr := flate.NewReader(ses.br)
	zw, err := flate.NewWriter(raw, flate.DefaultCompression)
	if err != nil {
		return err
	}
	dc := &deflateConn{raw: raw, reader: zr, writer: zw}
	// Replace the session's bufio reader and respWriter with views over
	// the deflate streams. The raw conn stays on ses.conn so STARTTLS
	// / SetReadDeadline keep working — only the I/O paths swing onto
	// the compressed streams.
	ses.br = bufio.NewReaderSize(dc, 16*1024)
	ses.resp = newRespWriter(dc)
	ses.compressed = true
	return nil
}
