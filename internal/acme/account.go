package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// accountKeyPEMBlockType is the PEM block label we write for the
// account private key. Choosing PRIVATE KEY (PKCS#8) keeps the format
// stable across crypto algorithms; future RSA support drops in without
// a format break.
const accountKeyPEMBlockType = "PRIVATE KEY"

// Register loads or creates the ACME account associated with
// (DirectoryURL, contactEmail). When an account row already exists with
// a non-empty KID, it is reused verbatim; otherwise the client
// generates a fresh ECDSA P-256 key, registers a new account, and
// persists the row via store.UpsertACMEAccount. The method is
// idempotent: calling it twice with the same arguments performs at
// most one network round-trip.
func (c *Client) Register(ctx context.Context, contactEmail string) error {
	if contactEmail == "" {
		return errors.New("acme: contact email empty")
	}
	if c.opts.Store == nil {
		return errors.New("acme: store not configured")
	}
	c.accountMu.Lock()
	defer c.accountMu.Unlock()

	// Reuse a running-process cached account if we already loaded one.
	if c.account != nil && strings.EqualFold(c.account.ContactEmail, contactEmail) {
		return nil
	}

	existing, err := c.opts.Store.Meta().GetACMEAccount(ctx, c.opts.DirectoryURL, contactEmail)
	if err == nil && existing.KID != "" {
		signer, err := parseAccountKey(existing.AccountKeyPEM)
		if err != nil {
			return fmt.Errorf("acme: parse stored account key: %w", err)
		}
		c.account = &existing
		c.signer = signer
		return nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("acme: lookup account: %w", err)
	}

	// Either no row at all, or a row without a KID — register fresh.
	priv := existing.AccountKeyPEM
	var signer *ecdsaSigner
	if priv == "" {
		key, err := generateECDSA()
		if err != nil {
			return fmt.Errorf("acme: generate account key: %w", err)
		}
		pemBytes, err := encodeECDSAPrivateKeyPEM(key)
		if err != nil {
			return err
		}
		priv = pemBytes
		signer = &ecdsaSigner{key: key}
	} else {
		s, err := parseAccountKey(priv)
		if err != nil {
			return fmt.Errorf("acme: parse partial account key: %w", err)
		}
		signer = s
	}

	dir, err := c.fetchDirectory(ctx)
	if err != nil {
		return err
	}

	payload := struct {
		Contact              []string `json:"contact"`
		TermsOfServiceAgreed bool     `json:"termsOfServiceAgreed"`
	}{
		Contact:              []string{"mailto:" + contactEmail},
		TermsOfServiceAgreed: true,
	}
	resp, err := c.post(ctx, signer, "", dir.NewAccount, payload, nil)
	if err != nil {
		return fmt.Errorf("acme: newAccount: %w", err)
	}
	if resp.Location == "" {
		return errors.New("acme: newAccount response missing Location header")
	}

	row := store.ACMEAccount{
		DirectoryURL:  c.opts.DirectoryURL,
		ContactEmail:  contactEmail,
		AccountKeyPEM: priv,
		KID:           resp.Location,
	}
	saved, err := c.opts.Store.Meta().UpsertACMEAccount(ctx, row)
	if err != nil {
		return fmt.Errorf("acme: persist account: %w", err)
	}
	c.account = &saved
	c.signer = signer
	c.opts.Logger.Info("acme account registered",
		"directory", c.opts.DirectoryURL,
		"contact", contactEmail,
		"kid", saved.KID)
	return nil
}

// parseAccountKey decodes a PEM-encoded ECDSA P-256 private key. We
// accept both PKCS#8 and SEC1 framings so externally-provisioned keys
// drop in without normalisation.
func parseAccountKey(pemStr string) (*ecdsaSigner, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("acme: account key not PEM-encoded")
	}
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("acme: parse PKCS8: %w", err)
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("acme: PKCS8 key is not ECDSA")
		}
		return &ecdsaSigner{key: ec}, nil
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("acme: parse EC key: %w", err)
		}
		return &ecdsaSigner{key: k}, nil
	default:
		return nil, fmt.Errorf("acme: unsupported PEM type %q", block.Type)
	}
}

// encodeECDSAPrivateKeyPEM marshals key as PKCS#8 PEM.
func encodeECDSAPrivateKeyPEM(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("acme: marshal PKCS8: %w", err)
	}
	out := pem.EncodeToMemory(&pem.Block{Type: accountKeyPEMBlockType, Bytes: der})
	return string(out), nil
}
