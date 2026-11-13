package protosmtp_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/protosmtp"
)

// addrScript describes how the in-process server behind one MX address
// answers. Empty reply fields use the accepting default.
type addrScript struct {
	closeAtConnect bool   // accept TCP, then close before any greeting
	greeting       string // default "220 mx.test ESMTP"
	ehlo           string // default "250 mx.test"
	mail           string // default "250 2.1.0 ok"
	rcpt           string // default "250 2.1.5 ok"
	final          string // default "250 2.0.0 queued"
}

func (s addrScript) or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// fallbackEnv is the Client under test plus the per-address bookkeeping.
type fallbackEnv struct {
	client *protosmtp.Client

	mu          sync.Mutex
	dialed      []string // "ip:25" in dial order
	dataStarted map[string]bool
}

// fallbackResolver serves one MX host (mx.test) with a fixed address list.
type fallbackResolver struct{ ips []net.IP }

func (fallbackResolver) TXTLookup(context.Context, string) ([]string, error) {
	return nil, mailauth.ErrNoRecords
}
func (fallbackResolver) MXLookup(context.Context, string) ([]*net.MX, error) {
	return []*net.MX{{Host: "mx.test.", Pref: 10}}, nil
}
func (r fallbackResolver) IPLookup(context.Context, string) ([]net.IP, error) {
	return r.ips, nil
}

// orderedEnv wires a Client whose DialFunc hands out net.Pipe ends
// served by the script registered for the dialed "ip:25" address, and
// whose resolver lists the MX addresses in the given order. The
// production dial loop (IPLookup, ordering, cap, per-address fallback)
// runs unchanged; only the TCP connect is replaced.
func orderedEnv(t *testing.T, order []string, scripts map[string]addrScript) *fallbackEnv {
	t.Helper()
	env := &fallbackEnv{dataStarted: map[string]bool{}}
	var ips []net.IP
	for _, a := range order {
		ips = append(ips, net.ParseIP(a))
	}
	env.client = protosmtp.NewClient(protosmtp.ClientOptions{
		HostName: "client.test",
		Resolver: fallbackResolver{ips: ips},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		// The session deadline is set on the conn from this clock, and
		// net.Pipe enforces it against wall time, so the clock is real.
		Clock: clock.NewReal(),
		DialFunc: func(_ context.Context, _, address string) (net.Conn, error) {
			env.mu.Lock()
			env.dialed = append(env.dialed, address)
			env.mu.Unlock()
			sc, ok := scripts[address]
			if !ok {
				return nil, fmt.Errorf("dial %s: no script (connection refused)", address)
			}
			c, s := net.Pipe()
			go env.serve(address, sc, s)
			return c, nil
		},
	})
	return env
}

func (e *fallbackEnv) serve(addr string, sc addrScript, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if sc.closeAtConnect {
		return
	}
	w := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	w(sc.or(sc.greeting, "220 mx.test ESMTP"))
	if replyCode(sc.greeting) >= 400 {
		return
	}
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w(sc.or(sc.ehlo, "250 mx.test"))
		case strings.HasPrefix(cmd, "MAIL"):
			w(sc.or(sc.mail, "250 2.1.0 ok"))
		case strings.HasPrefix(cmd, "RCPT"):
			w(sc.or(sc.rcpt, "250 2.1.5 ok"))
		case cmd == "DATA":
			e.mu.Lock()
			e.dataStarted[addr] = true
			e.mu.Unlock()
			w("354 go ahead")
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
			}
			w(sc.or(sc.final, "250 2.0.0 queued"))
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("500 unknown")
		}
	}
}

func replyCode(s string) int {
	if len(s) < 3 {
		return 0
	}
	var c int
	_, _ = fmt.Sscanf(s[:3], "%d", &c)
	return c
}

func (e *fallbackEnv) deliver(t *testing.T) protosmtp.DeliveryOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := e.client.Deliver(ctx, protosmtp.DeliveryRequest{
		MailFrom: "sender@example.org",
		RcptTo:   "empfaenger@beispiel.test",
		Message:  strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	return out
}

func (e *fallbackEnv) dialedAddrs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.dialed...)
}

func (e *fallbackEnv) gotData(addr string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dataStarted[addr]
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const (
	v6a = "2001:db8::1"
	v6b = "2001:db8::2"
	v6c = "2001:db8::3"
	v4a = "192.0.2.1"
	v4b = "192.0.2.2"
	v4c = "192.0.2.3"
)

func a25(ip string) string { return net.JoinHostPort(ip, "25") }

// Pre-MAIL transient failures on the first address move on to the next
// address of the same MX host.
func TestMXAddressFallback_PreMailTransientTriesNextAddress(t *testing.T) {
	cases := []struct {
		name  string
		first addrScript
	}{
		{"greeting_421", addrScript{greeting: "421 4.3.2 too many connections"}},
		{"reset_before_greeting", addrScript{closeAtConnect: true}},
		{"ehlo_4xx", addrScript{ehlo: "421 4.3.2 busy"}},
		{"mail_452", addrScript{mail: "452 4.3.1 Insufficient system resources"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := orderedEnv(t, []string{v6a, v4a}, map[string]addrScript{
				a25(v6a): tc.first,
				a25(v4a): {},
			})
			out := env.deliver(t)
			if out.Status != protosmtp.DeliverySuccess {
				t.Fatalf("status = %v diag=%q, want success on the second address", out.Status, out.Diagnostic)
			}
			if got, want := env.dialedAddrs(), []string{a25(v6a), a25(v4a)}; !eqStrings(got, want) {
				t.Fatalf("dialed %v, want %v", got, want)
			}
		})
	}
}

// A permanent greeting refusal keeps its semantics: no further address.
func TestMXAddressFallback_PermanentStops(t *testing.T) {
	env := orderedEnv(t, []string{v4a, v4b}, map[string]addrScript{
		a25(v4a): {greeting: "554 5.3.2 go away"},
		a25(v4b): {},
	})
	out := env.deliver(t)
	if out.Status != protosmtp.DeliveryPermanent {
		t.Fatalf("status = %v, want permanent", out.Status)
	}
	if got := env.dialedAddrs(); len(got) != 1 {
		t.Fatalf("dialed %v, want only the first address", got)
	}
}

// The dial order alternates address families, keeping the resolver's
// order within each family and starting with the family listed first.
func TestMXAddressFallback_InterleavesFamilies(t *testing.T) {
	fail := addrScript{greeting: "421 4.3.2 busy"}
	all := map[string]addrScript{
		a25(v6a): fail, a25(v6b): fail, a25(v6c): fail,
		a25(v4a): fail, a25(v4b): fail, a25(v4c): fail,
	}
	t.Run("v6_first", func(t *testing.T) {
		env := orderedEnv(t, []string{v6a, v6b, v6c, v4a, v4b, v4c}, all)
		_ = env.deliver(t)
		want := []string{a25(v6a), a25(v4a), a25(v6b), a25(v4b)}
		if got := env.dialedAddrs(); !eqStrings(got, want) {
			t.Fatalf("dialed %v, want %v", got, want)
		}
	})
	t.Run("v4_first", func(t *testing.T) {
		env := orderedEnv(t, []string{v4a, v4b, v4c, v6a, v6b, v6c}, all)
		_ = env.deliver(t)
		want := []string{a25(v4a), a25(v6a), a25(v4b), a25(v6b)}
		if got := env.dialedAddrs(); !eqStrings(got, want) {
			t.Fatalf("dialed %v, want %v", got, want)
		}
	})
}

// At most MaxSessionAttemptsPerMX sessions are opened per MX host.
func TestMXAddressFallback_Cap(t *testing.T) {
	fail := addrScript{greeting: "421 4.3.2 busy"}
	scripts := map[string]addrScript{}
	var order []string
	for i := 1; i <= 8; i++ {
		ip := fmt.Sprintf("192.0.2.%d", i)
		order = append(order, ip)
		scripts[a25(ip)] = fail
	}
	env := orderedEnv(t, order, scripts)
	out := env.deliver(t)
	if out.Status != protosmtp.DeliveryTransient || out.SMTPCode != 421 {
		t.Fatalf("outcome = %v/%d, want transient 421", out.Status, out.SMTPCode)
	}
	if got := len(env.dialedAddrs()); got != protosmtp.MaxSessionAttemptsPerMXForTest {
		t.Fatalf("dialed %d addresses, want cap %d", got, protosmtp.MaxSessionAttemptsPerMXForTest)
	}
}

// Once the DATA body is being sent, a transient final reply is returned
// without trying another address.
func TestMXAddressFallback_NoRetryAfterData(t *testing.T) {
	env := orderedEnv(t, []string{v6a, v4a}, map[string]addrScript{
		a25(v6a): {final: "451 4.7.0 Temporary server error"},
		a25(v4a): {},
	})
	out := env.deliver(t)
	if out.Status != protosmtp.DeliveryTransient || out.SMTPCode != 451 {
		t.Fatalf("outcome = %v/%d, want transient 451", out.Status, out.SMTPCode)
	}
	if got := env.dialedAddrs(); len(got) != 1 {
		t.Fatalf("dialed %v, want only the first address", got)
	}
	if env.gotData(a25(v4a)) {
		t.Fatal("second address received DATA")
	}
}

// A 4xx on RCPT (after MAIL was accepted) is the host's verdict on the
// recipient and ends the attempt for this host.
func TestMXAddressFallback_NoRetryAfterMailAccepted(t *testing.T) {
	env := orderedEnv(t, []string{v6a, v4a}, map[string]addrScript{
		a25(v6a): {rcpt: "450 4.2.0 greylisted"},
		a25(v4a): {},
	})
	out := env.deliver(t)
	if out.Status != protosmtp.DeliveryTransient || out.SMTPCode != 450 {
		t.Fatalf("outcome = %v/%d, want transient 450", out.Status, out.SMTPCode)
	}
	if got := env.dialedAddrs(); len(got) != 1 {
		t.Fatalf("dialed %v, want only the first address", got)
	}
}
