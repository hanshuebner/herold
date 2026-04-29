package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// orderRequest is the newOrder POST body.
type orderRequest struct {
	Identifiers []orderIdentifier `json:"identifiers"`
}

// orderIdentifier is one entry in a newOrder request or the server's
// response.
type orderIdentifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// orderResponse is the parsed RFC 8555 §7.1.3 order resource.
type orderResponse struct {
	Status         string            `json:"status"`
	Expires        string            `json:"expires"`
	Identifiers    []orderIdentifier `json:"identifiers"`
	NotBefore      string            `json:"notBefore"`
	NotAfter       string            `json:"notAfter"`
	Authorizations []string          `json:"authorizations"`
	Finalize       string            `json:"finalize"`
	Certificate    string            `json:"certificate"`
	Error          *problem          `json:"error"`
}

// authzResponse is the parsed authorisation resource.
type authzResponse struct {
	Status     string          `json:"status"`
	Identifier orderIdentifier `json:"identifier"`
	Challenges []challenge     `json:"challenges"`
	Wildcard   bool            `json:"wildcard"`
	Expires    string          `json:"expires"`
}

// challenge is one challenge entry inside an authorisation.
type challenge struct {
	Type   string   `json:"type"`
	URL    string   `json:"url"`
	Status string   `json:"status"`
	Token  string   `json:"token"`
	Error  *problem `json:"error"`
}

// finalizeRequest is the body POSTed to the order's finalize URL.
type finalizeRequest struct {
	CSR string `json:"csr"`
}

// Order runs the full RFC 8555 happy path: newOrder, authorise, present
// challenges, finalize, and download the cert. The returned ACMECert
// has been persisted via UpsertACMECert; the caller is responsible for
// adding it to the production TLS store (EnsureCert does this for
// you).
func (c *Client) Order(ctx context.Context, hostnames []string, challengeType store.ChallengeType, dnsPluginName string) (*store.ACMECert, error) {
	if err := c.checkChallengerWired(challengeType); err != nil {
		return nil, err
	}
	if c.account == nil || c.signer == nil {
		return nil, errors.New("acme: client not registered; call Register first")
	}
	c.opts.Logger.InfoContext(ctx, "acme issue start",
		slog.String("activity", observe.ActivitySystem),
		slog.String("hostname", hostnames[0]),
		slog.String("challenge_type", string(challengeType)),
	)
	dir, err := c.fetchDirectory(ctx)
	if err != nil {
		return nil, err
	}

	// 1. Persist a pending order row up front so a crash mid-flight is
	//    recoverable. The OrderURL column is filled in once the server
	//    returns it.
	lower := make([]string, len(hostnames))
	for i, h := range hostnames {
		lower[i] = strings.ToLower(h)
	}
	row := store.ACMEOrder{
		AccountID:     c.account.ID,
		Hostnames:     lower,
		Status:        store.ACMEOrderStatusPending,
		ChallengeType: challengeType,
		UpdatedAt:     c.opts.Clock.Now(),
	}
	row, err = c.opts.Store.Meta().InsertACMEOrder(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("acme: insert order: %w", err)
	}

	// 2. POST newOrder.
	req := orderRequest{Identifiers: make([]orderIdentifier, len(hostnames))}
	for i, h := range hostnames {
		req.Identifiers[i] = orderIdentifier{Type: "dns", Value: h}
	}
	var orderResp orderResponse
	resp, err := c.post(ctx, c.signer, c.account.KID, dir.NewOrder, req, &orderResp)
	if err != nil {
		c.markOrderInvalid(ctx, row, err)
		return nil, fmt.Errorf("acme: newOrder: %w", err)
	}
	row.OrderURL = resp.Location
	row.FinalizeURL = orderResp.Finalize
	row.Status = parseOrderStatus(orderResp.Status)
	if err := c.opts.Store.Meta().UpdateACMEOrder(ctx, row); err != nil {
		return nil, fmt.Errorf("acme: persist order url: %w", err)
	}

	return c.driveOrder(ctx, row, &orderResp, hostnames, challengeType, dnsPluginName)
}

// resumeOrder rejoins the order pipeline for a row already persisted.
// It re-fetches the server's view of the order via POST-as-GET and
// continues from whatever status the server reports.
func (c *Client) resumeOrder(ctx context.Context, row store.ACMEOrder) (*store.ACMECert, error) {
	if row.OrderURL == "" {
		return nil, fmt.Errorf("acme: resume order %d: no order URL", row.ID)
	}
	if err := c.checkChallengerWired(row.ChallengeType); err != nil {
		return nil, err
	}
	var orderResp orderResponse
	if _, err := c.post(ctx, c.signer, c.account.KID, row.OrderURL, nil, &orderResp); err != nil {
		return nil, fmt.Errorf("acme: re-fetch order: %w", err)
	}
	row.Status = parseOrderStatus(orderResp.Status)
	if orderResp.Finalize != "" {
		row.FinalizeURL = orderResp.Finalize
	}
	if err := c.opts.Store.Meta().UpdateACMEOrder(ctx, row); err != nil {
		return nil, fmt.Errorf("acme: persist resumed order: %w", err)
	}
	hostnames := row.Hostnames
	return c.driveOrder(ctx, row, &orderResp, hostnames, row.ChallengeType, "")
}

// driveOrder runs steps 3..7 of the order pipeline: authorisations,
// finalize, certificate download. The order row's status is updated as
// we cross each milestone so a crash recovers cleanly.
func (c *Client) driveOrder(ctx context.Context, row store.ACMEOrder, orderResp *orderResponse, hostnames []string, challengeType store.ChallengeType, dnsPluginName string) (*store.ACMECert, error) {
	cleanup := newChallengeCleanup()
	defer cleanup.run(ctx)

	// 3. Authorise each identifier.
	if row.Status == store.ACMEOrderStatusPending {
		for _, authzURL := range orderResp.Authorizations {
			if err := c.processAuthorization(ctx, authzURL, challengeType, dnsPluginName, cleanup); err != nil {
				c.markOrderInvalid(ctx, row, err)
				return nil, err
			}
		}
		// Re-fetch order to confirm it advanced to ready.
		var refreshed orderResponse
		if _, err := c.post(ctx, c.signer, c.account.KID, row.OrderURL, nil, &refreshed); err != nil {
			c.markOrderInvalid(ctx, row, err)
			return nil, fmt.Errorf("acme: refresh order: %w", err)
		}
		*orderResp = refreshed
		row.Status = parseOrderStatus(refreshed.Status)
		if refreshed.Finalize != "" {
			row.FinalizeURL = refreshed.Finalize
		}
		_ = c.opts.Store.Meta().UpdateACMEOrder(ctx, row)
	}

	// 4. Generate cert key + CSR; POST finalize.
	certKey, csrDER, err := c.buildCSR(hostnames)
	if err != nil {
		c.markOrderInvalid(ctx, row, err)
		return nil, err
	}
	if row.Status == store.ACMEOrderStatusReady {
		var finalizeResp orderResponse
		body := finalizeRequest{CSR: rawURLEncodeBytes(csrDER)}
		if _, err := c.post(ctx, c.signer, c.account.KID, row.FinalizeURL, body, &finalizeResp); err != nil {
			c.markOrderInvalid(ctx, row, err)
			return nil, fmt.Errorf("acme: finalize: %w", err)
		}
		row.Status = parseOrderStatus(finalizeResp.Status)
		if finalizeResp.Certificate != "" {
			row.CertificateURL = finalizeResp.Certificate
		}
		_ = c.opts.Store.Meta().UpdateACMEOrder(ctx, row)
		*orderResp = finalizeResp
	}

	// 5. Poll order until valid.
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := 0; row.Status != store.ACMEOrderStatusValid; i++ {
		if i >= c.opts.MaxPolls {
			err := fmt.Errorf("acme: order poll timeout (status=%s)", row.Status)
			c.markOrderInvalid(ctx, row, err)
			return nil, err
		}
		select {
		case <-pollCtx.Done():
			return nil, pollCtx.Err()
		case <-c.opts.Clock.After(c.opts.PollInterval):
		}
		var refreshed orderResponse
		if _, err := c.post(ctx, c.signer, c.account.KID, row.OrderURL, nil, &refreshed); err != nil {
			c.markOrderInvalid(ctx, row, err)
			return nil, fmt.Errorf("acme: poll order: %w", err)
		}
		row.Status = parseOrderStatus(refreshed.Status)
		if refreshed.Certificate != "" {
			row.CertificateURL = refreshed.Certificate
		}
		_ = c.opts.Store.Meta().UpdateACMEOrder(ctx, row)
		if row.Status == store.ACMEOrderStatusInvalid {
			detail := "invalid"
			if refreshed.Error != nil {
				detail = refreshed.Error.Error()
			}
			err := fmt.Errorf("acme: order invalid: %s", detail)
			c.markOrderInvalid(ctx, row, err)
			return nil, err
		}
		if row.Status == store.ACMEOrderStatusValid {
			break
		}
	}

	// 6. Download certificate.
	if row.CertificateURL == "" {
		err := errors.New("acme: order valid but no certificate URL")
		c.markOrderInvalid(ctx, row, err)
		return nil, err
	}
	resp, err := c.post(ctx, c.signer, c.account.KID, row.CertificateURL, nil, nil)
	if err != nil {
		c.markOrderInvalid(ctx, row, err)
		return nil, fmt.Errorf("acme: download certificate: %w", err)
	}
	chainPEM := string(resp.Body)
	leaf, err := parseLeaf(chainPEM)
	if err != nil {
		c.markOrderInvalid(ctx, row, err)
		return nil, err
	}

	// 7. Persist cert + finalise order row.
	keyPEM, err := encodeCertKeyPEM(certKey)
	if err != nil {
		return nil, err
	}
	cert := store.ACMECert{
		Hostname:      strings.ToLower(hostnames[0]),
		ChainPEM:      chainPEM,
		PrivateKeyPEM: keyPEM,
		NotBefore:     leaf.NotBefore,
		NotAfter:      leaf.NotAfter,
		Issuer:        leaf.Issuer.CommonName,
		OrderID:       row.ID,
	}
	if err := c.opts.Store.Meta().UpsertACMECert(ctx, cert); err != nil {
		return nil, fmt.Errorf("acme: persist cert: %w", err)
	}
	row.Status = store.ACMEOrderStatusValid
	_ = c.opts.Store.Meta().UpdateACMEOrder(ctx, row)
	c.opts.Logger.InfoContext(ctx, "acme issue success",
		slog.String("activity", observe.ActivitySystem),
		slog.String("hostname", cert.Hostname),
		slog.Time("not_after", cert.NotAfter),
	)
	return &cert, nil
}

// processAuthorization fetches one authorisation, picks the matching
// challenge, provisions it, POSTs the challenge URL to acknowledge, and
// polls until the authorisation reaches valid.
func (c *Client) processAuthorization(ctx context.Context, authzURL string, challengeType store.ChallengeType, dnsPluginName string, cleanup *challengeCleanup) error {
	var authz authzResponse
	if _, err := c.post(ctx, c.signer, c.account.KID, authzURL, nil, &authz); err != nil {
		return fmt.Errorf("acme: fetch authz: %w", err)
	}
	if authz.Status == "valid" {
		return nil
	}
	want := wireChallengeType(challengeType)
	var ch *challenge
	for i := range authz.Challenges {
		if authz.Challenges[i].Type == want {
			ch = &authz.Challenges[i]
			break
		}
	}
	if ch == nil {
		return fmt.Errorf("acme: authz for %s lacks %s challenge", authz.Identifier.Value, want)
	}
	thumb, err := jwsThumbprint(c.signer)
	if err != nil {
		return err
	}
	keyAuth := ch.Token + "." + thumb

	host := authz.Identifier.Value
	if err := c.provisionChallenge(ctx, challengeType, ch.Token, keyAuth, host, dnsPluginName); err != nil {
		return err
	}
	cleanup.add(challengeType, ch.Token, host, dnsPluginName, c)

	// POST the challenge URL with an empty object payload to ACK.
	if _, err := c.post(ctx, c.signer, c.account.KID, ch.URL, struct{}{}, nil); err != nil {
		return fmt.Errorf("acme: ack challenge: %w", err)
	}
	// Poll authz until valid.
	for i := 0; ; i++ {
		if i >= c.opts.MaxPolls {
			return fmt.Errorf("acme: authz poll timeout (status=%s)", authz.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.opts.Clock.After(c.opts.PollInterval):
		}
		var refreshed authzResponse
		if _, err := c.post(ctx, c.signer, c.account.KID, authzURL, nil, &refreshed); err != nil {
			return fmt.Errorf("acme: poll authz: %w", err)
		}
		switch refreshed.Status {
		case "valid":
			return nil
		case "invalid":
			detail := "invalid"
			for _, c2 := range refreshed.Challenges {
				if c2.Error != nil {
					detail = c2.Error.Error()
					break
				}
			}
			return fmt.Errorf("acme: authz invalid: %s", detail)
		}
	}
}

// provisionChallenge dispatches to the configured per-type challenger.
func (c *Client) provisionChallenge(ctx context.Context, t store.ChallengeType, token, keyAuth, host, dnsPluginName string) error {
	switch t {
	case store.ChallengeTypeHTTP01:
		c.opts.HTTPChallenger.Provision(token, keyAuth)
		return nil
	case store.ChallengeTypeTLSALPN01:
		return c.opts.TLSALPNChallenger.Provision(host, keyAuth)
	case store.ChallengeTypeDNS01:
		return c.opts.DNS01Challenger.Provision(ctx, host, keyAuth, dnsPluginName)
	}
	return fmt.Errorf("acme: unsupported challenge type %s", t)
}

// checkChallengerWired ensures the right challenger is configured for
// the requested challenge type.
func (c *Client) checkChallengerWired(t store.ChallengeType) error {
	switch t {
	case store.ChallengeTypeHTTP01:
		if c.opts.HTTPChallenger == nil {
			return errors.New("acme: HTTP-01 challenger not configured")
		}
	case store.ChallengeTypeTLSALPN01:
		if c.opts.TLSALPNChallenger == nil {
			return errors.New("acme: tls-alpn-01 challenger not configured")
		}
	case store.ChallengeTypeDNS01:
		if c.opts.DNS01Challenger == nil {
			return errors.New("acme: dns-01 challenger not configured")
		}
	default:
		return fmt.Errorf("acme: unknown challenge type %s", t)
	}
	return nil
}

// markOrderInvalid stamps the persisted row with an invalid status so
// the resume path skips it. Idempotent: failure to persist is logged
// but does not propagate (the caller already has a real error to
// surface).
func (c *Client) markOrderInvalid(ctx context.Context, row store.ACMEOrder, cause error) {
	row.Status = store.ACMEOrderStatusInvalid
	if cause != nil {
		row.Error = cause.Error()
	}
	c.opts.Logger.ErrorContext(ctx, "acme issue failure",
		slog.String("activity", observe.ActivitySystem),
		slog.Any("hostnames", row.Hostnames),
		slog.Any("err", cause),
	)
	if err := c.opts.Store.Meta().UpdateACMEOrder(ctx, row); err != nil {
		c.opts.Logger.Warn("acme mark order invalid",
			"activity", observe.ActivitySystem,
			"order_id", row.ID, "err", err)
	}
}

// challengeCleanup tracks the per-challenge teardown closures so a
// failure midway still removes any presented HTTP-01 token, TLS-ALPN-01
// cert, or DNS-01 record.
type challengeCleanup struct {
	fns []func(ctx context.Context)
}

func newChallengeCleanup() *challengeCleanup { return &challengeCleanup{} }

func (c *challengeCleanup) add(t store.ChallengeType, token, host, plugin string, client *Client) {
	switch t {
	case store.ChallengeTypeHTTP01:
		c.fns = append(c.fns, func(ctx context.Context) {
			client.opts.HTTPChallenger.Cleanup(token)
		})
	case store.ChallengeTypeTLSALPN01:
		c.fns = append(c.fns, func(ctx context.Context) {
			client.opts.TLSALPNChallenger.Cleanup(host)
		})
	case store.ChallengeTypeDNS01:
		c.fns = append(c.fns, func(ctx context.Context) {
			if err := client.opts.DNS01Challenger.Cleanup(ctx, host, plugin); err != nil {
				client.opts.Logger.Warn("acme dns-01 cleanup",
					"activity", observe.ActivitySystem,
					"err", err)
			}
		})
	}
}

func (c *challengeCleanup) run(ctx context.Context) {
	for _, fn := range c.fns {
		fn(ctx)
	}
}

// buildCSR generates a fresh per-cert subject key, builds a CSR
// covering hostnames, and returns the DER-encoded CSR plus the key.
func (c *Client) buildCSR(hostnames []string) (crypto.PrivateKey, []byte, error) {
	var key crypto.PrivateKey
	var pub crypto.PublicKey
	switch c.opts.CertKey {
	case CertKeyRSA2048:
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, nil, fmt.Errorf("acme: gen rsa key: %w", err)
		}
		key = k
		pub = &k.PublicKey
	case CertKeyECDSA:
		fallthrough
	default:
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, fmt.Errorf("acme: gen ecdsa key: %w", err)
		}
		key = k
		pub = &k.PublicKey
	}
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostnames[0]},
		DNSNames: hostnames,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, nil, fmt.Errorf("acme: create CSR: %w", err)
	}
	_ = pub
	return key, der, nil
}

// encodeCertKeyPEM encodes the per-cert subject key as PKCS#8 PEM.
func encodeCertKeyPEM(key crypto.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("acme: marshal cert key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// parseLeaf parses the first certificate in a PEM chain.
func parseLeaf(chainPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(chainPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("acme: chain has no leaf CERTIFICATE block")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("acme: parse leaf: %w", err)
	}
	return leaf, nil
}

// rawURLEncodeBytes is a small helper that avoids importing
// encoding/base64 in two more files. The CSR carries the DER bytes
// base64url-encoded with no padding (RFC 8555 §7.4).
func rawURLEncodeBytes(b []byte) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	if len(b) == 0 {
		return ""
	}
	out := make([]byte, 0, (len(b)*8+5)/6)
	var bits, n uint32
	for _, c := range b {
		bits = (bits << 8) | uint32(c)
		n += 8
		for n >= 6 {
			n -= 6
			out = append(out, enc[(bits>>n)&0x3f])
		}
	}
	if n > 0 {
		out = append(out, enc[(bits<<(6-n))&0x3f])
	}
	return string(out)
}

// parseOrderStatus maps the wire token to the ACMEOrderStatus enum.
func parseOrderStatus(s string) store.ACMEOrderStatus {
	switch s {
	case "pending":
		return store.ACMEOrderStatusPending
	case "ready":
		return store.ACMEOrderStatusReady
	case "processing":
		return store.ACMEOrderStatusProcessing
	case "valid":
		return store.ACMEOrderStatusValid
	case "invalid":
		return store.ACMEOrderStatusInvalid
	}
	return store.ACMEOrderStatusUnknown
}

// wireChallengeType maps the enum to the RFC 8555 wire token.
func wireChallengeType(t store.ChallengeType) string {
	switch t {
	case store.ChallengeTypeHTTP01:
		return "http-01"
	case store.ChallengeTypeTLSALPN01:
		return "tls-alpn-01"
	case store.ChallengeTypeDNS01:
		return "dns-01"
	}
	return ""
}

// useT keeps the import for time when challenges go through the clock
// API. The order pipeline schedules retries via clock.After.
var _ = time.Second
