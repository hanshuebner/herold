package e2e

import (
	"bytes"
	"testing"

	"github.com/hanshuebner/herold/test/e2e/fixtures"
)

// TestPhase1_BothBackends_SameSuite is the meta-test the phase-1 exit
// criterion calls for. It stands up the e2e harness (SMTP + IMAP on
// real 127.0.0.1:0 sockets) against each registered backend and runs
// one round-trip delivery per backend. The deeper store-compliance
// matrix lives in internal/store/storetest.Run; this test proves the
// *server boot path* works on both backends — i.e. the wiring in
// protosmtp + protoimap + delivery.go is store-agnostic.
//
// The Postgres leg skips cleanly when HEROLD_PG_DSN is unset; the
// SQLite leg is always exercised.
func TestPhase1_BothBackends_SameSuite(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		f := fixtures.Build(t, fixtures.Opts{Store: fixtures.Prepare(t, newStore)})

		body := "From: bob@sender.test\r\n" +
			"To: " + f.Email + "\r\n" +
			"Subject: backends-matrix\r\n" +
			"\r\n" +
			"round-trip through the server boot path\r\n"
		f.SendMessage(t, "bob@sender.test", []string{f.Email}, body, true)

		msgs := fixtures.LoadMessagesIn(t, f, f.Principal, "INBOX")
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message in INBOX, got %d", len(msgs))
		}
		if !bytes.Contains(msgs[0].Bytes, []byte("backends-matrix")) {
			t.Fatalf("wrong body:\n%s", msgs[0].Bytes)
		}

		// Verify we can LOGIN and SELECT the inbox too — otherwise we
		// could have a delivery-path-only regression going unnoticed.
		c := f.DialIMAP(t)
		defer c.Close()
		c.Login(f.Email, f.Password)
		resp := c.Send("s1", "SELECT INBOX")
		last := resp[len(resp)-1]
		if last == "" || (last[0] != 's') {
			t.Fatalf("SELECT response shape unexpected: %v", resp)
		}
	})
}
