package protosmtp_test

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestDeliver_EnvelopeAddressGuard: an envelope address with angle
// brackets, whitespace or control characters never reaches the wire as
// MAIL FROM / RCPT TO (SMTP parameter injection, re #562); the request
// fails permanently. The well-formed control case is delivered by the
// same server, so a refusal cannot be a harness failure.
func TestDeliver_EnvelopeAddressGuard(t *testing.T) {
	cases := map[string]struct {
		req  protosmtp.DeliveryRequest
		want protosmtp.DeliveryStatus
	}{
		"ok":             {protosmtp.DeliveryRequest{MailFrom: "n@a.test", RcptTo: "bob@example.com"}, protosmtp.DeliverySuccess},
		"null-sender-ok": {protosmtp.DeliveryRequest{MailFrom: "", RcptTo: "bob@example.com"}, protosmtp.DeliverySuccess},
		"rcpt-injection": {protosmtp.DeliveryRequest{MailFrom: "n@a.test", RcptTo: "bob@example.com (a> notify=never <b)"}, protosmtp.DeliveryPermanent},
		"rcpt-display":   {protosmtp.DeliveryRequest{MailFrom: "n@a.test", RcptTo: "bob <bob@example.com>"}, protosmtp.DeliveryPermanent},
		"rcpt-space":     {protosmtp.DeliveryRequest{MailFrom: "n@a.test", RcptTo: "bob@example.com foo"}, protosmtp.DeliveryPermanent},
		"rcpt-ctl":       {protosmtp.DeliveryRequest{MailFrom: "n@a.test", RcptTo: "bob@example.com\x00"}, protosmtp.DeliveryPermanent},
		"from-injection": {protosmtp.DeliveryRequest{MailFrom: "n@a.test> RET=FULL <x", RcptTo: "bob@example.com"}, protosmtp.DeliveryPermanent},
		"from-display":   {protosmtp.DeliveryRequest{MailFrom: "N <n@a.test>", RcptTo: "bob@example.com"}, protosmtp.DeliveryPermanent},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cert, pemCert := generateSelfSignedCert(t)
			cfg := &authCfg{
				ExpectedMech: "PLAIN", Username: "alice@example.com", Password: "hunter2",
				AdvertiseSTARTTLS: true, Cert: cert,
			}
			ln, stop := startTestSMTPServer(t, cfg)
			defer stop()
			pinPath := filepath.Join(t.TempDir(), "pin.pem")
			if err := os.WriteFile(pinPath, pemCert, 0o600); err != nil {
				t.Fatal(err)
			}
			host, port, _ := net.SplitHostPort(ln.Addr().String())
			portN, _ := strconv.Atoi(port)
			sh := sysconfig.SmartHostConfig{
				Enabled: true, Host: host, Port: portN, TLSMode: "starttls",
				AuthMethod: "plain", Username: "alice@example.com", PasswordEnv: "$X",
				FallbackPolicy: "smart_host_only", ConnectTimeoutSeconds: 5,
				ReadTimeoutSeconds: 5, FallbackAfterFailureSeconds: 300,
				TLSVerifyMode: "insecure_skip_verify", PinnedCertPath: pinPath,
			}
			c := newSmartHostClient(t, sh, "hunter2", clock.NewReal())
			req := tc.req
			req.Message = bytes.NewReader([]byte("Subject: x\r\n\r\nbody\r\n"))
			out, err := c.Deliver(context.Background(), req)
			if err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if out.Status != tc.want {
				t.Fatalf("status=%v code=%d diag=%q; want %v", out.Status, out.SMTPCode, out.Diagnostic, tc.want)
			}
			if tc.want == protosmtp.DeliveryPermanent && out.SMTPCode != 550 {
				t.Errorf("code=%d want 550", out.SMTPCode)
			}
		})
	}
}
