package protosmtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hanshuebner/herold/internal/netguard"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// effectiveSmartHost picks the SmartHostConfig that applies to
// recipient domain. Per-domain overrides take precedence; in their
// absence the top-level block is used when Enabled. Returns
// (config, isOverride, applies).
func (c *Client) effectiveSmartHost(domain string) (sysconfig.SmartHostConfig, bool, bool) {
	if ov, ok := c.smartHost.PerDomain[strings.ToLower(domain)]; ok {
		// Per-domain override implicitly applies. PerDomain on the
		// override is ignored at validate-time.
		return ov, true, true
	}
	if c.smartHost.Enabled {
		return c.smartHost, false, true
	}
	return sysconfig.SmartHostConfig{}, false, false
}

// dispatchSmartHost executes the FallbackPolicy state machine for the
// effective smart-host config. It is the entry point Deliver forks to
// when the smart-host predicate fires.
func (c *Client) dispatchSmartHost(
	ctx context.Context,
	req DeliveryRequest,
	domain string,
	sh sysconfig.SmartHostConfig,
	startedAt time.Time,
) DeliveryOutcome {
	switch sh.FallbackPolicy {
	case "smart_host_only", "":
		out := c.deliverViaSmartHost(ctx, req, sh, startedAt)
		c.auditPath(ctx, req, domain, "smart_host", sh.FallbackPolicy, out, false)
		observe.SMTPOutboundTotal.WithLabelValues("smart_host", outboundOutcomeLabel(out)).Inc()
		return out
	case "smart_host_then_mx":
		out := c.deliverViaSmartHost(ctx, req, sh, startedAt)
		observe.SMTPOutboundTotal.WithLabelValues("smart_host", outboundOutcomeLabel(out)).Inc()
		if c.shouldFallbackToMX(sh, out) {
			c.auditPath(ctx, req, domain, "smart_host", sh.FallbackPolicy, out, true)
			observe.SMTPSmartHostFallbackTotal.WithLabelValues(sh.FallbackPolicy, "direct_mx").Inc()
			mxOut := c.deliverDirectMX(ctx, req, domain, startedAt)
			c.auditPath(ctx, req, domain, "direct_mx", sh.FallbackPolicy, mxOut, false)
			observe.SMTPOutboundTotal.WithLabelValues("direct_mx", outboundOutcomeLabel(mxOut)).Inc()
			return mxOut
		}
		c.auditPath(ctx, req, domain, "smart_host", sh.FallbackPolicy, out, false)
		return out
	case "mx_then_smart_host":
		mxOut := c.deliverDirectMX(ctx, req, domain, startedAt)
		observe.SMTPOutboundTotal.WithLabelValues("direct_mx", outboundOutcomeLabel(mxOut)).Inc()
		if mxOut.Status == DeliveryTransient || mxOut.Status == DeliveryPermanent {
			c.auditPath(ctx, req, domain, "direct_mx", sh.FallbackPolicy, mxOut, true)
			observe.SMTPSmartHostFallbackTotal.WithLabelValues(sh.FallbackPolicy, "smart_host").Inc()
			out := c.deliverViaSmartHost(ctx, req, sh, startedAt)
			observe.SMTPOutboundTotal.WithLabelValues("smart_host", outboundOutcomeLabel(out)).Inc()
			c.auditPath(ctx, req, domain, "smart_host", sh.FallbackPolicy, out, false)
			return out
		}
		c.auditPath(ctx, req, domain, "direct_mx", sh.FallbackPolicy, mxOut, false)
		return mxOut
	default:
		// Validate should have caught this; defer to smart_host_only.
		out := c.deliverViaSmartHost(ctx, req, sh, startedAt)
		observe.SMTPOutboundTotal.WithLabelValues("smart_host", outboundOutcomeLabel(out)).Inc()
		c.auditPath(ctx, req, domain, "smart_host", sh.FallbackPolicy, out, false)
		return out
	}
}

// shouldFallbackToMX reports whether the smart-host outcome warrants a
// direct-MX fallback under the "smart_host_then_mx" policy. The two
// triggers are (a) any 5xx from the upstream or (b) sustained
// connection refusal for >= FallbackAfterFailureSeconds. Successful or
// 4xx outcomes do not fall back: a 4xx is treated as transient and
// retried by the queue against the same smart host.
func (c *Client) shouldFallbackToMX(sh sysconfig.SmartHostConfig, out DeliveryOutcome) bool {
	upstream := net.JoinHostPort(sh.Host, strconv.Itoa(sh.Port))
	switch out.Status {
	case DeliverySuccess:
		c.clearSmartHostFailure(upstream)
		return false
	case DeliveryPermanent:
		c.clearSmartHostFailure(upstream)
		return true
	case DeliveryTransient:
		// Connection-refused / unreachable: arm or check the sustained-
		// outage timer. Other 4xx-shaped transient failures do not
		// fall back.
		if !isConnectionRefusal(out.Diagnostic) {
			c.clearSmartHostFailure(upstream)
			return false
		}
		first := c.markSmartHostFailure(upstream)
		threshold := time.Duration(sh.FallbackAfterFailureSeconds) * time.Second
		return c.clk.Now().Sub(first) >= threshold
	}
	return false
}

// markSmartHostFailure records the first time we observed a connection
// refusal for upstream, returning that timestamp. Repeated calls return
// the same first-observation time so the caller can compute the
// elapsed sustained-outage window. clearSmartHostFailure resets it.
func (c *Client) markSmartHostFailure(upstream string) time.Time {
	c.smartHostFailureMu.Lock()
	defer c.smartHostFailureMu.Unlock()
	if t, ok := c.smartHostFailureSince[upstream]; ok {
		return t
	}
	t := c.clk.Now()
	c.smartHostFailureSince[upstream] = t
	return t
}

func (c *Client) clearSmartHostFailure(upstream string) {
	c.smartHostFailureMu.Lock()
	defer c.smartHostFailureMu.Unlock()
	delete(c.smartHostFailureSince, upstream)
}

// isConnectionRefusal returns true when diag matches one of the
// connection-establishment failure shapes: refused, no route, or DNS-
// shaped "no addresses". These signal the smart host is down rather
// than the message itself being bad.
func isConnectionRefusal(diag string) bool {
	d := strings.ToLower(diag)
	return strings.Contains(d, "connection refused") ||
		strings.Contains(d, "no route to host") ||
		strings.Contains(d, "network is unreachable") ||
		strings.Contains(d, "no such host") ||
		strings.Contains(d, "i/o timeout") ||
		strings.Contains(d, "tcp unreachable") ||
		strings.Contains(d, "dial tcp")
}

// deliverDirectMX is the sibling entry point that runs the existing
// direct-MX state machine. It is split out so dispatchSmartHost can
// invoke it for fallback paths without re-running the smart-host
// predicate from Deliver. Mirrors the body of Deliver below the
// smart-host fork; any change to MX dispatch must update both sites.
func (c *Client) deliverDirectMX(
	ctx context.Context,
	req DeliveryRequest,
	domain string,
	startedAt time.Time,
) DeliveryOutcome {
	var policy *MTASTSPolicy
	if c.stsCache != nil {
		p, perr := c.stsCache.Lookup(ctx, domain)
		if perr != nil {
			c.log.WarnContext(ctx, "outbound: mta-sts lookup failed; treating as no-policy",
				slog.String("domain", domain), slog.String("err", perr.Error()))
		} else {
			policy = p
		}
	}
	candidates, mxErr := c.resolveMX(ctx, domain)
	if mxErr != nil {
		if errors.Is(mxErr, mailauthIsTemporary(mxErr)) {
			return DeliveryOutcome{
				Status:      DeliveryTransient,
				Diagnostic:  fmt.Sprintf("mx resolution: %s", mxErr.Error()),
				AttemptedAt: startedAt,
			}
		}
		return DeliveryOutcome{
			Status:      DeliveryTransient,
			Diagnostic:  fmt.Sprintf("mx resolution: %s", mxErr.Error()),
			AttemptedAt: startedAt,
		}
	}
	var lastOutcome DeliveryOutcome
	lastOutcome.AttemptedAt = startedAt
	for _, cand := range candidates {
		outcome := c.deliverToMX(ctx, req, domain, cand, policy, startedAt)
		switch outcome.Status {
		case DeliverySuccess, DeliveryPermanent:
			return outcome
		case DeliveryTransient:
			lastOutcome = outcome
			continue
		}
	}
	if lastOutcome.Status == DeliveryUnknown {
		lastOutcome.Status = DeliveryTransient
		lastOutcome.Diagnostic = "no MX candidates produced an outcome"
	}
	return lastOutcome
}

// mailauthIsTemporary is a placeholder so the import graph for this
// file does not change; we just compare against err itself, which
// makes the conditional always-false. Kept for parity with the parent
// Deliver method's shape so the diff stays small.
func mailauthIsTemporary(err error) error { return err }

// deliverViaSmartHost runs one delivery attempt against the configured
// upstream relay. It is purposely close to deliverToMX in shape so
// changes to one path are easy to mirror to the other; the divergence
// is the dial target, the netguard predicate, the AUTH step, and the
// TLS posture (including implicit TLS and pinned verification).
func (c *Client) deliverViaSmartHost(
	ctx context.Context,
	req DeliveryRequest,
	sh sysconfig.SmartHostConfig,
	startedAt time.Time,
) DeliveryOutcome {
	upstream := net.JoinHostPort(sh.Host, strconv.Itoa(sh.Port))
	out := DeliveryOutcome{
		MXHost:      sh.Host,
		AttemptedAt: startedAt,
	}

	// netguard: refuse private/loopback/CGNAT/etc unless the operator
	// has explicitly opted into the dev-mode TLS posture
	// (insecure_skip_verify). REQ-FLOW-SMARTHOST-04 documents the
	// dev-only override.
	if sh.TLSVerifyMode != "insecure_skip_verify" {
		if err := netguard.CheckHost(ctx, nil, sh.Host); err != nil {
			out.Status = DeliveryPermanent
			out.SMTPCode = 550
			out.EnhancedCode = "5.7.1"
			out.Diagnostic = fmt.Sprintf("smart-host dial blocked by netguard: %s", err.Error())
			return out
		}
	}

	// Connect: implicit TLS goes straight to a TLS handshake; STARTTLS
	// and "none" both start with a plaintext greeting.
	connectStart := c.clk.Now()
	dialCtx, cancel := context.WithTimeout(ctx, time.Duration(sh.ConnectTimeoutSeconds)*time.Second)
	defer cancel()

	conn, derr := c.dialSmartHost(dialCtx, sh, upstream)
	if derr != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host dial %s: %s", upstream, derr.Error())
		observe.SMTPOutboundConnectSeconds.WithLabelValues("smart_host").Observe(c.clk.Now().Sub(connectStart).Seconds())
		return out
	}
	deadline := c.clk.Now().Add(time.Duration(sh.ReadTimeoutSeconds) * time.Second)
	_ = conn.SetDeadline(deadline)

	// Implicit TLS: wrap conn in tls.Client now.
	if sh.TLSMode == "implicit_tls" {
		tlsConn, terr := c.smartHostTLSWrap(ctx, conn, sh)
		if terr != nil {
			_ = conn.Close()
			out.Status = DeliveryPermanent
			out.SMTPCode = 554
			out.EnhancedCode = "5.7.0"
			out.Diagnostic = fmt.Sprintf("smart-host implicit TLS: %s", terr.Error())
			observe.SMTPOutboundConnectSeconds.WithLabelValues("smart_host").Observe(c.clk.Now().Sub(connectStart).Seconds())
			return out
		}
		conn = tlsConn
		_ = conn.SetDeadline(deadline)
	}

	observe.SMTPOutboundConnectSeconds.WithLabelValues("smart_host").Observe(c.clk.Now().Sub(connectStart).Seconds())

	sess := newOutboundSession(conn, sh.Host, sh.Host, c.clk)
	defer sess.close(ctx)

	// Greeting.
	gcode, gtext, err := sess.readGreeting(ctx)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host greeting read: %s", err.Error())
		return out
	}
	if gcode/100 != 2 {
		return mapReplyOutcome(out, gcode, "", gtext, "smart-host greeting")
	}

	// EHLO.
	ecode, etext, err := sess.ehlo(ctx, c.hostName)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host ehlo: %s", err.Error())
		return out
	}
	if ecode != 250 {
		return mapReplyOutcome(out, ecode, "", etext, "smart-host ehlo")
	}

	// STARTTLS upgrade if requested.
	if sh.TLSMode == "starttls" {
		if !sess.hasExtension("STARTTLS") {
			out.Status = DeliveryPermanent
			out.SMTPCode = 530
			out.EnhancedCode = "5.7.10"
			out.Diagnostic = fmt.Sprintf("smart host %s does not advertise STARTTLS but tls_mode=starttls", sh.Host)
			return out
		}
		if cmdErr := sess.command(ctx, "STARTTLS"); cmdErr != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("smart-host starttls write: %s", cmdErr.Error())
			return out
		}
		tcode, _, ttext, rerr := sess.readReply(ctx)
		if rerr != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("smart-host starttls read: %s", rerr.Error())
			return out
		}
		if tcode != 220 {
			return mapReplyOutcome(out, tcode, "", ttext, "smart-host starttls")
		}
		tlsConn, terr := c.smartHostTLSWrap(ctx, sess.conn, sh)
		if terr != nil {
			out.Status = DeliveryPermanent
			out.SMTPCode = 554
			out.EnhancedCode = "5.7.0"
			out.Diagnostic = fmt.Sprintf("smart-host TLS handshake: %s", terr.Error())
			return out
		}
		sess.upgradeConn(tlsConn)
		_ = tlsConn.SetDeadline(deadline)
		// Re-EHLO post-TLS per RFC 3207 §4.2.
		ecode2, etext2, err2 := sess.ehlo(ctx, c.hostName)
		if err2 != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("smart-host post-tls ehlo: %s", err2.Error())
			return out
		}
		if ecode2 != 250 {
			return mapReplyOutcome(out, ecode2, "", etext2, "smart-host post-tls ehlo")
		}
	}

	if sh.TLSMode == "starttls" || sh.TLSMode == "implicit_tls" {
		out.TLSUsed = true
		out.TLSPolicy = "smart_host_" + sh.TLSVerifyMode
	} else {
		out.TLSPolicy = "none"
	}

	// AUTH.
	if sh.AuthMethod != "none" {
		if authErr := c.runSmartHostAuth(ctx, sess, sh); authErr != nil {
			out.Status = DeliveryPermanent
			out.SMTPCode = 535
			out.EnhancedCode = "5.7.8"
			out.Diagnostic = fmt.Sprintf("smart-host AUTH %s: %s", sh.AuthMethod, authErr.Error())
			return out
		}
	}

	// MAIL FROM / RCPT TO / DATA / body, identical content to direct-MX.
	mailLine := buildMailFromLine(req, sess)
	rcptLine := buildRcptToLine(req, sess)

	if cmdErr := sess.command(ctx, mailLine); cmdErr != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host mail write: %s", cmdErr.Error())
		return out
	}
	mc, me, mt, err := sess.readReply(ctx)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host mail reply: %s", err.Error())
		return out
	}
	if mc/100 != 2 {
		return mapReplyOutcome(out, mc, me, mt, "smart-host mail from")
	}
	if cmdErr := sess.command(ctx, rcptLine); cmdErr != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host rcpt write: %s", cmdErr.Error())
		return out
	}
	rc, re, rt, err := sess.readReply(ctx)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host rcpt reply: %s", err.Error())
		return out
	}
	if rc/100 != 2 {
		return mapReplyOutcome(out, rc, re, rt, "smart-host rcpt to")
	}
	if cmdErr := sess.command(ctx, "DATA"); cmdErr != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host data write: %s", cmdErr.Error())
		return out
	}
	dc, de, dt, err := sess.readReply(ctx)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host data reply: %s", err.Error())
		return out
	}
	if dc != 354 {
		return mapReplyOutcome(out, dc, de, dt, "smart-host data")
	}
	if werr := writeDotStuffed(sess.writer, req.Message); werr != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host data body: %s", werr.Error())
		return out
	}
	if _, werr := sess.writer.WriteString(".\r\n"); werr != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host data terminator: %s", werr.Error())
		return out
	}
	if ferr := sess.writer.Flush(); ferr != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host data flush: %s", ferr.Error())
		return out
	}
	fc, fe, ft, err := sess.readReply(ctx)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("smart-host data final: %s", err.Error())
		return out
	}
	if fc/100 == 2 {
		out.Status = DeliverySuccess
		out.SMTPCode = fc
		out.EnhancedCode = fe
		out.Diagnostic = ft
		c.log.InfoContext(ctx, "outbound: smart-host delivered",
			slog.String("upstream", upstream),
			slog.String("rcpt", req.RcptTo),
			slog.Bool("tls", out.TLSUsed),
			slog.String("tls_policy", out.TLSPolicy),
			slog.String("message_id", req.MessageID))
		return out
	}
	return mapReplyOutcome(out, fc, fe, ft, "smart-host data final")
}

// dialSmartHost performs the TCP dial. The dialer installs netguard's
// ControlContext when TLSVerifyMode is not "insecure_skip_verify" so a
// DNS rebind to a private address fails late as well as early.
func (c *Client) dialSmartHost(ctx context.Context, sh sysconfig.SmartHostConfig, address string) (net.Conn, error) {
	if c.dialFunc != nil {
		return c.dialFunc(ctx, "tcp", address)
	}
	d := &net.Dialer{Timeout: time.Duration(sh.ConnectTimeoutSeconds) * time.Second}
	if sh.TLSVerifyMode != "insecure_skip_verify" {
		d.ControlContext = func(ctx context.Context, network, addr string, rc syscall.RawConn) error {
			return netguard.ControlContext()(ctx, network, addr, rc)
		}
	}
	return d.DialContext(ctx, "tcp", address)
}

// smartHostTLSWrap returns a *tls.Conn around raw with the TLS posture
// the operator chose. system_roots uses the host's CA pool and
// validates the server cert against sh.Host. pinned compares the
// server cert against the PEM at sh.PinnedCertPath.
// insecure_skip_verify disables verification entirely (dev-only).
func (c *Client) smartHostTLSWrap(ctx context.Context, raw net.Conn, sh sysconfig.SmartHostConfig) (*tls.Conn, error) {
	cfg := &tls.Config{
		ServerName: sh.Host,
		MinVersion: tls.VersionTLS12,
	}
	switch sh.TLSVerifyMode {
	case "system_roots", "":
		// Default: stdlib PKIX validation against system roots.
	case "pinned":
		pem, err := os.ReadFile(sh.PinnedCertPath)
		if err != nil {
			return nil, fmt.Errorf("read pinned cert %q: %w", sh.PinnedCertPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("pinned cert %q: no PEM certificates found", sh.PinnedCertPath)
		}
		cfg.RootCAs = pool
	case "insecure_skip_verify":
		cfg.InsecureSkipVerify = true
	default:
		return nil, fmt.Errorf("unrecognised tls_verify_mode %q", sh.TLSVerifyMode)
	}
	tlsConn := tls.Client(raw, cfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}
	return tlsConn, nil
}

// runSmartHostAuth drives the SASL exchange with the upstream. The
// password is materialised at call time via the operator-configured
// resolver (env / file).
func (c *Client) runSmartHostAuth(ctx context.Context, sess *outboundSession, sh sysconfig.SmartHostConfig) error {
	if c.passwordResolver == nil {
		return errors.New("no password resolver configured")
	}
	password, err := c.passwordResolver()
	if err != nil {
		return fmt.Errorf("resolve secret: %w", err)
	}
	var mech sasl.ClientMechanism
	switch sh.AuthMethod {
	case "plain":
		mech = sasl.NewClientPLAIN("", sh.Username, password)
	case "login":
		mech = sasl.NewClientLOGIN(sh.Username, password)
	case "scram-sha-256":
		mech = sasl.NewClientSCRAMSHA256(sh.Username, password, nil)
	case "xoauth2":
		mech = sasl.NewClientXOAUTH2(sh.Username, password)
	default:
		return fmt.Errorf("unsupported auth_method %q", sh.AuthMethod)
	}

	ir, _, err := mech.Start()
	if err != nil {
		return fmt.Errorf("%s start: %w", mech.Name(), err)
	}
	// Build the AUTH verb. RFC 4954 §4: when the mechanism has IR,
	// the client MAY send "AUTH MECH <base64(ir)>" inline; "=" is the
	// special token for an empty IR.
	authLine := "AUTH " + mech.Name()
	if ir != nil {
		if len(ir) == 0 {
			authLine += " ="
		} else {
			authLine += " " + base64.StdEncoding.EncodeToString(ir)
		}
	}
	if cmdErr := sess.command(ctx, authLine); cmdErr != nil {
		return fmt.Errorf("auth write: %w", cmdErr)
	}

	for {
		code, _, text, rerr := sess.readReply(ctx)
		if rerr != nil {
			return fmt.Errorf("auth read: %w", rerr)
		}
		switch code {
		case 235:
			return nil
		case 334:
			// Server expects another client message.
			challenge, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
			if derr != nil {
				return fmt.Errorf("decode challenge: %w", derr)
			}
			resp, _, mErr := mech.Next(challenge)
			if mErr != nil {
				// Cancel the exchange with a "*" token so the server
				// returns a 5xx final reply we can map.
				_ = sess.command(ctx, "*")
				return fmt.Errorf("%s next: %w", mech.Name(), mErr)
			}
			line := ""
			if resp != nil {
				line = base64.StdEncoding.EncodeToString(resp)
			}
			if cmdErr := sess.command(ctx, line); cmdErr != nil {
				return fmt.Errorf("auth client write: %w", cmdErr)
			}
			continue
		default:
			return fmt.Errorf("auth rejected: %d %s", code, strings.TrimSpace(text))
		}
	}
}

// auditPath records the chosen path on this delivery (REQ-FLOW-
// SMARTHOST-08). The audit data rides on the structured log so it can
// be picked up by an external collector without coupling protosmtp to
// a store dependency. fellBack is true when the policy decided to
// switch paths after this attempt.
func (c *Client) auditPath(
	ctx context.Context,
	req DeliveryRequest,
	domain, path, policy string,
	out DeliveryOutcome,
	fellBack bool,
) {
	c.log.LogAttrs(ctx, slog.LevelInfo, "outbound: audit",
		slog.Bool("audit", true),
		slog.String("domain", domain),
		slog.String("rcpt", req.RcptTo),
		slog.String("path", path),
		slog.String("fallback_policy", policy),
		slog.Bool("fell_back", fellBack),
		slog.String("status", outboundOutcomeLabel(out)),
		slog.Int("smtp_code", out.SMTPCode),
		slog.String("message_id", req.MessageID),
	)
}

// outboundOutcomeLabel maps a DeliveryOutcome to the closed-vocabulary
// outcome label used by herold_smtp_outbound_total. Connection-refused,
// TLS-failed, and auth-failed are split out for operator visibility;
// everything else collapses to status (success / transient / permanent).
func outboundOutcomeLabel(out DeliveryOutcome) string {
	d := strings.ToLower(out.Diagnostic)
	switch {
	case out.Status == DeliverySuccess:
		return "success"
	case strings.Contains(d, "auth"):
		return "auth_failed"
	case strings.Contains(d, "tls") || strings.Contains(d, "starttls"):
		return "tls_failed"
	case strings.Contains(d, "connection refused") ||
		strings.Contains(d, "no route") ||
		strings.Contains(d, "network is unreachable") ||
		strings.Contains(d, "no such host") ||
		strings.Contains(d, "i/o timeout"):
		return "connection_refused"
	case out.Status == DeliveryTransient:
		return "transient"
	case out.Status == DeliveryPermanent:
		return "permanent"
	}
	return "transient"
}
