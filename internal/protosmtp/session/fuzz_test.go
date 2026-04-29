package session_test

import (
	"bufio"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp/session"
)

// FuzzReplyParser exercises the SMTP reply parser with arbitrary input.
// It must not panic regardless of what bytes are fed to it.
func FuzzReplyParser(f *testing.F) {
	// Seed corpus: valid single-line replies.
	f.Add("220 smtp.test ESMTP\r\n", true)
	f.Add("250 OK\r\n", false)
	f.Add("250 2.0.0 OK\r\n", true)
	f.Add("354 send data\r\n", false)
	f.Add("421 4.4.1 service unavailable\r\n", true)
	f.Add("535 5.7.8 bad credentials\r\n", true)

	// Seed corpus: multi-line replies.
	f.Add("250-smtp.test\r\n250-STARTTLS\r\n250 ENHANCEDSTATUSCODES\r\n", true)
	f.Add("250-2.0.0 First\r\n250-2.0.0 Second\r\n250 2.0.0 Third\r\n", true)

	// Seed corpus: edge cases.
	f.Add("", false)
	f.Add("\r\n", false)
	f.Add("250\r\n", false)
	f.Add("250 \r\n", false)

	f.Fuzz(func(t *testing.T, input string, enhanced bool) {
		r := bufio.NewReader(strings.NewReader(input))
		// ParseReply must not panic. Error return is acceptable.
		_, _ = session.ParseReply(r, enhanced)
	})
}
