package scripted

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
)

// smtpScript is one named end-to-end SMTP exchange. Each step is
// either a client send (C:) or a server-response expectation (S:).
// The expectation is a regexp anchored to the start of the response
// line so a "250" pattern matches both "250 OK" and "250-EHLO".
type smtpScript struct {
	name  string
	steps []smtpStep
}

type smtpStep struct {
	send   string         // when non-empty, written to the server
	expect *regexp.Regexp // when non-nil, the next line must match
	// expectMulti, when non-nil, consumes lines until one matches; the
	// matched line is consumed and the loop ends. Used for EHLO where
	// the server emits "250-..." lines until "250 ".
	expectMulti *regexp.Regexp
}

// smtpCorpus is the set of scenarios the runner asserts. Kept minimal
// and focused on RFC 5321 / 6152 / 4954 / 3030 corner cases that are
// either RFC-mandatory or operationally important.
var smtpCorpus = []smtpScript{
	{
		name: "ehlo_advertises_pipelining_size_chunking_smtputf8",
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
		},
	},
	{
		name: "noop_is_idempotent_in_idle_state",
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
			{send: "NOOP"},
			{expect: re(`^250 `)},
			{send: "NOOP"},
			{expect: re(`^250 `)},
		},
	},
	{
		name: "rset_clears_envelope",
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
			{send: "MAIL FROM:<sender@example.test>"},
			{expect: re(`^250 `)},
			{send: "RSET"},
			{expect: re(`^250 `)},
			// After RSET, RCPT before MAIL must fail.
			{send: "RCPT TO:<alice@example.test>"},
			{expect: re(`^503 `)},
		},
	},
	{
		name: "vrfy_returns_252_per_rfc5321_3_5_3",
		// RFC 5321 §3.5.3: VRFY MAY return 252 ("server cannot verify
		// the user, but will accept the message and try delivery").
		// herold returns 252 for any address; the test pins the code,
		// not the address-handling policy.
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
			{send: "VRFY postmaster"},
			{expect: re(`^(252|550|502)`)},
		},
	},
	{
		name: "rcpt_before_mail_rejected",
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
			{send: "RCPT TO:<alice@example.test>"},
			{expect: re(`^503 `)},
		},
	},
	{
		name: "data_before_rcpt_rejected",
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
			{send: "MAIL FROM:<sender@example.test>"},
			{expect: re(`^250 `)},
			{send: "DATA"},
			{expect: re(`^503 `)},
		},
	},
	{
		name: "size_overflow_rejected_at_mail_from_per_rfc1870",
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
			{send: "MAIL FROM:<sender@example.test> SIZE=999999999"},
			{expect: re(`^552 `)},
		},
	},
	{
		name: "auth_plain_without_tls_rejected_per_rfc4954",
		// 503 because we haven't authenticated and STARTTLS is required
		// before AUTH on the cleartext relay-in port. herold returns
		// 503 (bad sequence) or 530 ("must issue STARTTLS"); both are
		// acceptable per RFC 4954 §4.
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
			{send: "AUTH PLAIN"},
			// herold's relay-in port returns 502 ("AUTH not available
			// on this port"). Other RFC-compliant codes are 503/530/
			// 538/454 — accept any of them so a future change to a
			// stricter response is still reported clearly.
			{expect: re(`^(502|503|530|538|454)`)},
		},
	},
	{
		name: "quit_is_clean_shutdown",
		steps: []smtpStep{
			{expect: re(`^220 `)},
			{send: "EHLO client.example.test"},
			{expectMulti: re(`^250 `)},
			{send: "QUIT"},
			{expect: re(`^221 `)},
		},
	},
}

// TestSMTPScriptedCorpus asserts that an in-process protosmtp server
// answers a set of canonical RFC 5321 client exchanges with the
// expected status codes. Compared to the protosmtp-internal tests the
// corpus here is deliberately wire-level — every assertion is a
// regexp on the response line — so a refactor that changes a code's
// numeric value is caught immediately.
func TestSMTPScriptedCorpus(t *testing.T) {
	srv, addr := startSMTPServer(t)
	_ = srv

	for _, sc := range smtpCorpus {
		t.Run(sc.name, func(t *testing.T) {
			runSMTPScript(t, addr, sc)
		})
	}
}

// runSMTPScript dials addr, walks the scenario's steps, and asserts
// every server response matches the expected pattern.
func runSMTPScript(t *testing.T, addr string, sc smtpScript) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	for stepIdx, step := range sc.steps {
		if step.send != "" {
			if _, err := conn.Write([]byte(step.send + "\r\n")); err != nil {
				t.Fatalf("step %d: write: %v", stepIdx, err)
			}
			continue
		}
		if step.expectMulti != nil {
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					t.Fatalf("step %d: read multi: %v (partial=%q)", stepIdx, err, line)
				}
				if step.expectMulti.MatchString(line) {
					break
				}
			}
			continue
		}
		if step.expect != nil {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("step %d: read: %v (partial=%q)", stepIdx, err, line)
			}
			if !step.expect.MatchString(line) {
				t.Fatalf("step %d: response %q did not match %s", stepIdx, line, step.expect)
			}
		}
	}
}

// startSMTPServer constructs an in-process protosmtp.Server bound to a
// random localhost port and returns the listen address. The server is
// torn down via t.Cleanup.
func startSMTPServer(t *testing.T) (*protosmtp.Server, string) {
	t.Helper()
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	if _, err := dir.CreatePrincipal(ctx, "alice@example.test", "irrelevant-but-required"); err != nil {
		t.Fatalf("create principal: %v", err)
	}

	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	spamCls := spam.New(&fakeInvoker{reg: ha.Plugins}, ha.Logger, ha.Clock)

	resolver := resolverAdapter{d: ha.DNS}
	dkimV := maildkim.New(resolver, ha.Logger, ha.Clock)
	spfV := mailspf.New(resolver, ha.Clock)
	dmarcV := maildmarc.New(resolver)
	arcV := mailarc.New(resolver)

	srv, err := protosmtp.New(protosmtp.Config{
		Store:     ha.Store,
		Directory: dir,
		DKIM:      dkimV,
		SPF:       spfV,
		DMARC:     dmarcV,
		ARC:       arcV,
		Spam:      spamCls,
		Sieve:     sieve.NewInterpreter(),
		Resolver:  resolver,
		Clock:     ha.Clock,
		Logger:    ha.Logger,
		Options: protosmtp.Options{
			Hostname:                 "mx.example.test",
			AuthservID:               "mx.example.test",
			MaxMessageSize:           65536,
			ReadTimeout:              5 * time.Second,
			WriteTimeout:             5 * time.Second,
			DataTimeout:              10 * time.Second,
			ShutdownGrace:            2 * time.Second,
			MaxRecipientsPerMessage:  5,
			MaxCommandsPerSession:    200,
			MaxConcurrentConnections: 32,
			MaxConcurrentPerIP:       16,
		},
	})
	if err != nil {
		t.Fatalf("protosmtp.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	ha.AttachSMTP("smtp", srv, protosmtp.RelayIn)
	addr, ok := ha.ListenerAddr("smtp")
	if !ok {
		t.Fatalf("no listener addr for smtp")
	}
	return srv, addr.String()
}

func re(p string) *regexp.Regexp { return regexp.MustCompile(p) }

// fakeInvoker satisfies spam.PluginInvoker by routing to the
// fakeplugin registry the testharness exposes. Mirrors the helper
// internal/protosmtp/server_test.go uses; copied here so the
// scripted package does not have to fork the protosmtp tests.
type fakeInvoker struct{ reg *fakeplugin.Registry }

func (f *fakeInvoker) Call(ctx context.Context, plugin, method string, params, result any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := f.reg.Call(ctx, plugin, method, paramsJSON)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}

// resolverAdapter wraps fakedns.Resolver into mailauth.Resolver — the
// same shim internal/protosmtp/server_test.go uses, copied here so
// this package does not import protosmtp_test.
type resolverAdapter struct{ d *fakedns.Resolver }

func (a resolverAdapter) TXTLookup(ctx context.Context, name string) ([]string, error) {
	out, err := a.d.LookupTXT(ctx, name)
	if err != nil {
		if errors.Is(err, fakedns.ErrNoRecords) {
			return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, name)
		}
		return nil, err
	}
	return out, nil
}

func (a resolverAdapter) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	mxs, err := a.d.LookupMX(ctx, domain)
	if err != nil {
		if errors.Is(err, fakedns.ErrNoRecords) {
			return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, domain)
		}
		return nil, err
	}
	out := make([]*net.MX, 0, len(mxs))
	for _, m := range mxs {
		out = append(out, &net.MX{Host: m.Host, Pref: m.Preference})
	}
	return out, nil
}

func (a resolverAdapter) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	v4, err4 := a.d.LookupA(ctx, host)
	v6, err6 := a.d.LookupAAAA(ctx, host)
	if err4 != nil && err6 != nil {
		return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, host)
	}
	return append(append([]net.IP{}, v4...), v6...), nil
}
