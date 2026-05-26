package protosmtp_test

// Tests for the inbound seed-on-receive path (REQ-MAIL-11h) carrying an
// RFC 2047 encoded-word display name. Pairs the mailparse-layer test
// (internal/mailparse/rfc2047_address_test.go) with the higher-level
// store test that issue #16 explicitly asks for: a stored SeenAddress
// row must carry the decoded UTF-8 name, never the raw
// `=?utf-8?q?...?=` form that surfaces in the suite's compose
// autocomplete dropdown.

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

func TestSeedFromAddress_DecodesRFC2047DisplayName(t *testing.T) {
	cases := []struct {
		name     string
		from     string // value of the From: header
		wantAddr string
		wantName string
	}{
		{
			name:     "utf-8 Q-encoded (issue #16 exact)",
			from:     "=?utf-8?q?Hans_H=C3=BCbner?= <hans.huebner@gmail.com>",
			wantAddr: "hans.huebner@gmail.com",
			wantName: "Hans Hübner",
		},
		{
			name:     "utf-8 B-encoded",
			from:     "=?utf-8?B?SGFucyBIw7xibmVy?= <hans@example.com>",
			wantAddr: "hans@example.com",
			wantName: "Hans Hübner",
		},
		{
			name:     "iso-8859-1 Q-encoded (Jörg Müller)",
			from:     "=?iso-8859-1?Q?J=F6rg_M=FCller?= <joerg@example.de>",
			wantAddr: "joerg@example.de",
			wantName: "Jörg Müller",
		},
		{
			name:     "plain ASCII display name passes through",
			from:     "Alice Smith <alice.smith@example.com>",
			wantAddr: "alice.smith@example.com",
			wantName: "Alice Smith",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ha, _ := testharness.Start(t, testharness.Options{})
			if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.local", IsLocal: true}); err != nil {
				t.Fatalf("insert domain: %v", err)
			}
			dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
			pid, err := dir.CreatePrincipal(ctx, "alice@example.local", "correct-horse-staple-battery")
			if err != nil {
				t.Fatalf("create principal: %v", err)
			}

			raw := buildInboundMessage(tc.from, "alice@example.local")
			msg, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
			if err != nil {
				t.Fatalf("mailparse: %v", err)
			}

			protosmtp.SeedFromAddressForTest(ctx, ha.Store, slog.Default(), pid, tc.wantAddr, msg)

			got, err := ha.Store.Meta().GetSeenAddressByEmail(ctx, pid, tc.wantAddr)
			if err != nil {
				t.Fatalf("GetSeenAddressByEmail: %v", err)
			}
			if got.DisplayName != tc.wantName {
				t.Errorf("DisplayName: got %q want %q (raw From was %q)",
					got.DisplayName, tc.wantName, tc.from)
			}
			if got.Email != tc.wantAddr {
				t.Errorf("Email: got %q want %q", got.Email, tc.wantAddr)
			}
			if strings.Contains(got.DisplayName, "=?") {
				t.Errorf("DisplayName still contains encoded-word marker: %q", got.DisplayName)
			}
		})
	}
}

// buildInboundMessage returns the minimum valid RFC 5322 message that
// carries a From and To header — enough for mailparse to populate
// Envelope.From.
func buildInboundMessage(from, to string) []byte {
	const tmpl = "From: %FROM%\r\n" +
		"To: %TO%\r\n" +
		"Subject: hi\r\n" +
		"Date: Mon, 26 May 2026 12:00:00 +0000\r\n" +
		"Message-ID: <inbound@example.local>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"hello\r\n"
	out := strings.Replace(tmpl, "%FROM%", from, 1)
	out = strings.Replace(out, "%TO%", to, 1)
	return []byte(out)
}
