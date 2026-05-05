package load

// FetchThroughputScenario drives a single IMAP session over a large mailbox and
// measures how long FETCH 1:* (FLAGS UID) takes.
//
// Target per 03-testing-strategy.md §7: < 1 s on reference hardware.
//
// Acceptance thresholds are intentionally conservative until real numbers are
// available from the nightly runner.  The gate uses a relaxed 60 s value that
// will pass on any hardware while still exercising the full code path.
//
// Open question for the maintainer: what counts as "reference hardware"?  The
// CI self-hosted runner spec should be documented so the 1 s gate value is
// defensible.  Until then the default FetchTimeoutSeconds is 60.
//
// Full-scale configuration:
//
//	MessageCount:         100 000
//	FetchTimeoutSeconds:  1.0  (after hardware is characterised)

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// FetchThroughputScenario measures IMAP FETCH 1:* throughput for a large mailbox.
type FetchThroughputScenario struct {
	// MessageCount is the number of messages to pre-seed.
	// Full-scale: 100 000.  Smoke test: 100.
	MessageCount int
	// FetchTimeoutSeconds is the pass/fail gate for the FETCH duration.
	// Full-scale target: 1.0.  Default: 60.0 (relaxed).
	FetchTimeoutSeconds float64
}

// Name implements Scenario.
func (s *FetchThroughputScenario) Name() string { return "fetch_throughput" }

// Run implements Scenario.
func (s *FetchThroughputScenario) Run(ctx context.Context, h *Harness) *RunResult {
	r, start := newRunResult(s.Name(), h.Backend)

	count := s.MessageCount
	if count <= 0 {
		count = 100
	}
	fetchTimeout := s.FetchTimeoutSeconds
	if fetchTimeout <= 0 {
		fetchTimeout = 60.0
	}

	setupCtx, setupCancel := context.WithTimeout(ctx, 60*time.Second)
	defer setupCancel()

	pid, err := h.CreatePrincipal(setupCtx, "fetch-user", "fetch-password-01!")
	if err != nil {
		r.addError(fmt.Errorf("create principal: %w", err))
		r.finish(start)
		return r
	}

	seedStart := time.Now()
	if seedErr := seedMessages(setupCtx, h, pid, count); seedErr != nil {
		r.addError(fmt.Errorf("seed messages: %w", seedErr))
		r.finish(start)
		return r
	}
	r.Metrics["seed_duration_seconds"] = time.Since(seedStart).Seconds()
	r.Metrics["messages_seeded"] = float64(count)

	fetchStart := time.Now()
	fetchedCount, fetchErr := imapFetchAll(ctx, h, "fetch-user@"+h.Domain, "fetch-password-01!")
	fetchDur := time.Since(fetchStart).Seconds()

	r.Metrics["fetch_duration_seconds"] = fetchDur
	r.Metrics["messages_fetched"] = float64(fetchedCount)
	if fetchDur > 0 {
		r.Metrics["fetch_rate_msg_per_sec"] = float64(fetchedCount) / fetchDur
	}
	if fetchErr != nil {
		r.addError(fetchErr)
	}

	r.addGateGTE("messages_fetched", float64(count), float64(fetchedCount))
	r.addGateLTE("fetch_duration_seconds", fetchTimeout, fetchDur)

	if d := h.StopPprof(); d != "" {
		r.PprofDir = d
	}
	r.finish(start)
	return r
}

// seedMessages inserts count minimal messages into pid's INBOX directly via
// the store layer, bypassing SMTP.
func seedMessages(ctx context.Context, h *Harness, pid store.PrincipalID, count int) error {
	mbs, err := h.HA.Store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return fmt.Errorf("list mailboxes: %w", err)
	}
	var inbox store.Mailbox
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, "INBOX") {
			inbox = mb
			break
		}
	}
	if inbox.ID == 0 {
		return errors.New("INBOX not found after CreatePrincipal")
	}

	for i := 0; i < count; i++ {
		body := fmt.Sprintf(
			"From: seed@load.test\r\nTo: fetch-user@load.test\r\n"+
				"Subject: seed-%d\r\nDate: Mon, 01 Jan 2026 00:00:00 +0000\r\n"+
				"Message-ID: <seed-%d@load.test>\r\n\r\nload test seed body %d\r\n",
			i, i, i,
		)
		blobRef, err := h.HA.Store.Blobs().Put(ctx, strings.NewReader(body))
		if err != nil {
			return fmt.Errorf("blob put %d: %w", i, err)
		}

		msg := store.Message{
			PrincipalID:  pid,
			ReceivedAt:   time.Now(),
			InternalDate: time.Now(),
			Size:         int64(len(body)),
			Blob:         blobRef,
			Envelope: store.Envelope{
				Subject: fmt.Sprintf("seed-%d", i),
			},
		}
		target := store.MessageMailbox{
			MailboxID: inbox.ID,
		}
		if _, _, err = h.HA.Store.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{target}); err != nil {
			return fmt.Errorf("insert message %d: %w", i, err)
		}
	}
	return nil
}

// imapFetchAll performs LOGIN + SELECT INBOX + FETCH 1:* (FLAGS UID) against
// the harness IMAPS listener and returns the count of * FETCH lines received.
func imapFetchAll(ctx context.Context, h *Harness, email, password string) (int, error) {
	dialCtx, dialCancel := context.WithTimeout(ctx, 30*time.Second)
	defer dialCancel()

	var d net.Dialer
	rawConn, err := d.DialContext(dialCtx, "tcp", h.IMAPSAddr)
	if err != nil {
		return 0, fmt.Errorf("dial imaps %s: %w", h.IMAPSAddr, err)
	}
	defer rawConn.Close()

	tlsConn := tls.Client(rawConn, h.TLSClient)
	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		return 0, fmt.Errorf("tls handshake: %w", err)
	}

	br := bufio.NewReader(tlsConn)
	// Drain greeting line.
	if _, err := br.ReadString('\n'); err != nil {
		return 0, fmt.Errorf("greeting: %w", err)
	}

	sendCmd := func(cmd string) error {
		_ = tlsConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, err := fmt.Fprintf(tlsConn, "%s\r\n", cmd)
		return err
	}

	readTagged := func(tag string, readDL time.Duration) ([]string, error) {
		_ = tlsConn.SetReadDeadline(time.Now().Add(readDL))
		var lines []string
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return lines, fmt.Errorf("read: %w", err)
			}
			line = strings.TrimRight(line, "\r\n")
			lines = append(lines, line)
			if strings.HasPrefix(line, tag+" ") {
				return lines, nil
			}
		}
	}

	if err := sendCmd("a1 LOGIN " + email + " " + password); err != nil {
		return 0, fmt.Errorf("send LOGIN: %w", err)
	}
	loginLines, err := readTagged("a1", 30*time.Second)
	if err != nil {
		return 0, fmt.Errorf("LOGIN resp: %w", err)
	}
	if !imapTaggedOK("a1", loginLines) {
		return 0, fmt.Errorf("LOGIN failed: %v", loginLines)
	}

	if err := sendCmd("a2 SELECT INBOX"); err != nil {
		return 0, fmt.Errorf("send SELECT: %w", err)
	}
	selectLines, err := readTagged("a2", 30*time.Second)
	if err != nil {
		return 0, fmt.Errorf("SELECT resp: %w", err)
	}
	if !imapTaggedOK("a2", selectLines) {
		return 0, fmt.Errorf("SELECT failed: %v", selectLines)
	}

	if err := sendCmd("a3 FETCH 1:* (FLAGS UID)"); err != nil {
		return 0, fmt.Errorf("send FETCH: %w", err)
	}
	fetchLines, err := readTagged("a3", 120*time.Second)
	if err != nil {
		return 0, fmt.Errorf("FETCH resp: %w", err)
	}
	if !imapTaggedOK("a3", fetchLines) {
		return 0, fmt.Errorf("FETCH failed: last=%q", fetchLines[len(fetchLines)-1])
	}

	n := 0
	for _, l := range fetchLines {
		if strings.Contains(l, "FETCH (") {
			n++
		}
	}
	return n, nil
}

// imapTaggedOK returns true when any line begins with "tag OK".
func imapTaggedOK(tag string, lines []string) bool {
	prefix := tag + " OK"
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
