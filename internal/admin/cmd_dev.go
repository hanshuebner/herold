package admin

// cmd_dev.go — hidden development utilities for ephemeral dev instances.
//
// These commands are NOT part of the production operator surface. They open
// the store directly (bypassing the REST layer) to seed states that are
// impossible to reach through normal wire paths, such as an auth-failed
// IdentitySubmission row (the REST PUT requires a successful probe).
//
// The commands are marked Hidden=true so they never appear in `herold --help`
// or man-page generation. They are invoked by scripts/dev-instance.sh when
// HEROLD_DEV_EXTERNAL_SUBMISSION is set.

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// devSeedForeignDomain is the non-authoritative domain used for the three
// seeded external-identity shapes. It is deliberately not registered as a
// local domain so GET .../submission returns domain_authoritative=false.
const devSeedForeignDomain = "foreign.example"

// devIdentityIDs groups the four deterministic identity IDs used by the
// seed command and readable by dev-instance.sh callers.
const (
	devIdentitySetupNeeded = "800001"
	devIdentityWorkingExt  = "800002"
	devIdentityBrokenExt   = "800003"
	// devIdentityOAuthExt is an oauth2-configured identity pointed at the
	// fakesmtp sink, in auth-failed state. It exists so the "Verbindung
	// testen" button and the "Neu autorisieren" re-auth popup can be
	// exercised in the dev instance without a real Gmail account (re #131).
	devIdentityOAuthExt = "800004"
)

// devSeedEntries describes the four foreign-domain identity shapes to seed.
// IDs are chosen outside the auto-allocator range (nanosecond timestamps
// ~1.7e18) so they never collide with identities created at runtime.
var devSeedEntries = []struct {
	id    string
	email string
	note  string
}{
	{devIdentitySetupNeeded, "alice@" + devSeedForeignDomain, "setup-needed"},
	{devIdentityWorkingExt, "alice-work@" + devSeedForeignDomain, "working-external"},
	{devIdentityBrokenExt, "alice-broken@" + devSeedForeignDomain, "broken-external"},
	{devIdentityOAuthExt, "alice-oauth@" + devSeedForeignDomain, "oauth-broken"},
}

// newDevCmd returns the hidden "dev" command group containing development
// utilities for ephemeral dev instances (scripts/dev-instance.sh). Commands
// here open the store directly and must never be run against a production
// store.
func newDevCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "dev",
		Short:  "Development utilities (not for production use)",
		Hidden: true,
	}
	c.AddCommand(newDevSeedExternalIdentitiesCmd())
	c.AddCommand(newDevEnrollAdminTOTPCmd())
	c.AddCommand(newDevGenTOTPCodeCmd())
	return c
}

// newDevEnrollAdminTOTPCmd implements direct TOTP enrollment for a named
// principal without the two-step REST enroll+confirm flow. It opens the
// store directly, generates a TOTP secret, writes it to the principal's
// row with PrincipalFlagTOTPEnabled already set (skipping the confirmation
// step), and prints the raw base32 secret to stdout.
//
// Called by scripts/dev-instance.sh after `herold bootstrap` and before
// the server starts so the admin principal has a confirmed TOTP secret
// from the first HTTP request onward.
func newDevEnrollAdminTOTPCmd() *cobra.Command {
	var principalEmail string
	c := &cobra.Command{
		Use:   "enroll-admin-totp",
		Short: "enroll and confirm TOTP for a dev-instance principal (not for production)",
		Long: "Opens the store directly, generates a TOTP secret for the named\n" +
			"principal, and writes it with PrincipalFlagTOTPEnabled set — bypassing\n" +
			"the REST two-step flow (enroll + confirm). Prints the base32 secret to\n" +
			"stdout. Run after `herold bootstrap` and before the server starts.\n\n" +
			"Pair with `herold dev gen-totp-code --secret <secret>` to derive a\n" +
			"live TOTP code for POST /api/v1/auth/step-up during puppeteer flows.",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDevEnrollAdminTOTP(cmd, principalEmail)
		},
	}
	c.Flags().StringVar(&principalEmail, "principal", "admin@example.local",
		"email of the principal to enroll TOTP for")
	return c
}

func runDevEnrollAdminTOTP(cmd *cobra.Command, principalEmail string) error {
	g := globals(cmd.Context())
	cfg, err := requireConfig(g)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		return fmt.Errorf("dev enroll-admin-totp: open store: %w", err)
	}
	defer st.Close()

	p, err := st.Meta().GetPrincipalByEmail(ctx, principalEmail)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("dev enroll-admin-totp: principal %q not found; run bootstrap first", principalEmail)
		}
		return fmt.Errorf("dev enroll-admin-totp: lookup principal: %w", err)
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "Herold",
		AccountName: p.CanonicalEmail,
	})
	if err != nil {
		return fmt.Errorf("dev enroll-admin-totp: generate totp key: %w", err)
	}

	// Write the secret and mark TOTP confirmed in one UpdatePrincipal call.
	// This is the clean dev-seed path: no REST enroll step, no confirmation
	// code round-trip — the secret and the enabled flag are written atomically.
	p.TOTPSecret = []byte(key.Secret())
	p.Flags |= store.PrincipalFlagTOTPEnabled
	if err := st.Meta().UpdatePrincipal(ctx, p); err != nil {
		return fmt.Errorf("dev enroll-admin-totp: update principal: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), key.Secret())
	return nil
}

// newDevGenTOTPCodeCmd generates the current TOTP code for a given base32
// secret using the same parameters the directory enforces (SHA-1, 6 digits,
// 30 s period). Intended for use in puppeteer flows that need to POST a live
// code to /api/v1/auth/step-up.
func newDevGenTOTPCodeCmd() *cobra.Command {
	var secret string
	c := &cobra.Command{
		Use:    "gen-totp-code",
		Short:  "generate the current TOTP code for a base32 secret (not for production)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDevGenTOTPCode(cmd, secret)
		},
	}
	c.Flags().StringVar(&secret, "secret", "", "base32 TOTP secret (required)")
	_ = c.MarkFlagRequired("secret")
	return c
}

func runDevGenTOTPCode(cmd *cobra.Command, secret string) error {
	code, err := totp.GenerateCodeCustom(secret, time.Now(), totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return fmt.Errorf("dev gen-totp-code: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), code)
	return nil
}

// newDevSeedExternalIdentitiesCmd implements the per-principal identity
// seeding step that enables external-identity rendering states in dev
// instances started with HEROLD_DEV_EXTERNAL_SUBMISSION=1.
//
// It opens the store directly and, for the named principal:
//   - inserts three verified JMAP identities on devSeedForeignDomain
//   - inserts IdentitySubmission rows for two of them (state ok, state
//     auth-failed) with real AEAD-sealed placeholder credentials
//   - the third identity (setup-needed) has no submission row, so GET
//     returns {configured:false, domain_authoritative:false}
//
// The foreign domain is not registered as a local domain, so all three
// identities report domain_authoritative=false. The principal's own
// authoritative identity (@example.local) is untouched and reports
// domain_authoritative=true.
func newDevSeedExternalIdentitiesCmd() *cobra.Command {
	var principalEmail string
	var sinkAddr string
	c := &cobra.Command{
		Use:   "seed-external-identities",
		Short: "seed foreign-domain identities for a dev instance (not for production)",
		Long: "Inserts three verified JMAP identities on " + devSeedForeignDomain + " " +
			"for the named principal, plus two IdentitySubmission rows covering " +
			"the ok and auth-failed health states. Requires [server.secrets].data_key_ref " +
			"to be configured so credentials can be properly AEAD-sealed.\n\n" +
			"Identity IDs are deterministic:\n" +
			"  " + devIdentitySetupNeeded + " = setup-needed  (no submission row)\n" +
			"  " + devIdentityWorkingExt + " = working-external (state: ok)\n" +
			"  " + devIdentityBrokenExt + " = broken-external  (state: auth-failed)\n\n" +
			"The principal must already exist. Run after `herold bootstrap` and " +
			"after the principal is created (e.g. via `herold principal create`).\n\n" +
			"When --sink-addr is set (host:port of a running heroldfakesmtp), the " +
			"working-external identity is seeded pointing at that address with " +
			"submit_security=none so the connection-test endpoint can reach the sink.",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDevSeedExternalIdentities(cmd, principalEmail, sinkAddr)
		},
	}
	c.Flags().StringVar(&principalEmail, "principal", "alice@example.local",
		"email of the principal that will own the seeded identities")
	c.Flags().StringVar(&sinkAddr, "sink-addr", "",
		"host:port of the running heroldfakesmtp sink; overrides the working-external identity's submit_host/port and sets submit_security=none")
	return c
}

func runDevSeedExternalIdentities(cmd *cobra.Command, principalEmail, sinkAddr string) error {
	g := globals(cmd.Context())
	cfg, err := requireConfig(g)
	if err != nil {
		return err
	}

	// The data key must be present: submission rows require AEAD-sealed
	// credential fields (ValidateIdentitySubmissionCTs rejects unencrypted
	// plaintext regardless of the auth method).
	dataKey, err := secrets.LoadDataKey(cfg.Server.Secrets)
	if err != nil {
		return fmt.Errorf("dev seed-external-identities: load data key: %w (configure [server.secrets].data_key_ref)", err)
	}

	ctx := cmd.Context()
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		return fmt.Errorf("dev seed-external-identities: open store: %w", err)
	}
	defer st.Close()

	// Resolve the target principal by email.
	principal, err := st.Meta().GetPrincipalByEmail(ctx, principalEmail)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("dev seed-external-identities: principal %q not found; create it first (e.g. herold principal create)", principalEmail)
		}
		return fmt.Errorf("dev seed-external-identities: lookup principal: %w", err)
	}

	now := clk.Now()

	// Insert the three foreign-domain identities; all are marked verified
	// (VerifiedAtUs != 0) to bypass the email verification gate (REQ-IDENT-60)
	// so they appear immediately in the compose From picker and Settings UI.
	for _, entry := range devSeedEntries {
		if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
			ID:           entry.id,
			PrincipalID:  principal.ID,
			Name:         "Alice (" + entry.note + ")",
			Email:        entry.email,
			MayDelete:    true,
			VerifiedAtUs: now.UnixMicro(),
		}); err != nil {
			return fmt.Errorf("dev seed-external-identities: insert identity %s (%s): %w", entry.id, entry.email, err)
		}
	}

	// Seal a placeholder password once; reuse the sealed blob for both
	// submission rows. The sealed value is never decrypted during rendering;
	// it satisfies ValidateIdentitySubmissionCTs (v1: prefix present) and
	// keeps the store schema consistent with production rows.
	pwCT, err := secrets.Seal(dataKey, []byte("dev-placeholder-password"))
	if err != nil {
		return fmt.Errorf("dev seed-external-identities: seal placeholder: %w", err)
	}

	// Working external identity: state ok. When --sink-addr is provided
	// (host:port of a live heroldfakesmtp), the working submission row points
	// at that address with submit_security=none so the connection-test and
	// real relay can reach the fake sink without TLS.
	workingHost := "smtp." + devSeedForeignDomain
	workingPort := 587
	workingSecurity := "starttls"
	if sinkAddr != "" {
		h, portStr, err := net.SplitHostPort(sinkAddr)
		if err != nil {
			return fmt.Errorf("dev seed-external-identities: --sink-addr %q: %w", sinkAddr, err)
		}
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("dev seed-external-identities: --sink-addr %q: port not numeric: %w", sinkAddr, err)
		}
		workingHost = h
		workingPort = p
		workingSecurity = "none"
	}
	if err := st.Meta().UpsertIdentitySubmission(ctx, store.IdentitySubmission{
		IdentityID:       devIdentityWorkingExt,
		SubmitHost:       workingHost,
		SubmitPort:       workingPort,
		SubmitSecurity:   workingSecurity,
		SubmitAuthMethod: "password",
		PasswordCT:       pwCT,
		OAuthClientID:    "alice-work@" + devSeedForeignDomain,
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		return fmt.Errorf("dev seed-external-identities: upsert working submission: %w", err)
	}

	// Broken external identity: state auth-failed. Direct-store insertion
	// bypasses the probe that the REST PUT enforces (the probe would need to
	// reach a real SMTP server to return ok), allowing this unreachable state
	// to be seeded deterministically for UI testing.
	//
	// When --sink-addr is provided the broken-external row points at the same
	// fake SMTP sink as the working-external row. This lets puppeteer drive a
	// successful "Verbindung testen" on the broken-external identity and observe
	// the badge clearing from SMTP-Fehler to ok, exercising the
	// handleTestSubmission state-update fix (re #131).
	brokenHost := "smtp." + devSeedForeignDomain
	brokenPort := 587
	brokenSecurity := "starttls"
	if sinkAddr != "" {
		brokenHost = workingHost
		brokenPort = workingPort
		brokenSecurity = workingSecurity
	}
	if err := st.Meta().UpsertIdentitySubmission(ctx, store.IdentitySubmission{
		IdentityID:       devIdentityBrokenExt,
		SubmitHost:       brokenHost,
		SubmitPort:       brokenPort,
		SubmitSecurity:   brokenSecurity,
		SubmitAuthMethod: "password",
		PasswordCT:       pwCT,
		OAuthClientID:    "alice-broken@" + devSeedForeignDomain,
		State:            store.IdentitySubmissionStateAuthFailed,
		StateAt:          now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		return fmt.Errorf("dev seed-external-identities: upsert broken submission: %w", err)
	}

	// OAuth external identity: state auth-failed, pointed at the same fake
	// SMTP sink as the working-external identity. When --sink-addr is provided
	// and the "gmail" provider is configured in system.toml (set by
	// dev-instance.sh when HEROLD_DEV_EXTERNAL_SUBMISSION=1), this identity
	// lets puppeteer drive the full "Verbindung testen" → failure modal →
	// "Neu autorisieren" popup flow against the fake IdP + SMTP sink without
	// a real Gmail account (re #131).
	//
	// The stored OAuthAccessCT contains a placeholder token that is
	// deliberately invalid (extsubmit: open access token will fail at test
	// time, surfacing the failure modal). The re-auth popup exchanges a fresh
	// code via the fake IdP, which writes a real sealed token and probes the
	// fake SMTP sink (which accepts any XOAUTH2 credential), transitioning
	// the identity to state=ok.
	if sinkAddr != "" {
		if gmailProv, ok := cfg.Server.OAuthProviders["gmail"]; ok && gmailProv.TokenURL != "" {
			oauthAccessCT, oaErr := secrets.Seal(dataKey, []byte("dev-placeholder-oauth-token"))
			if oaErr != nil {
				return fmt.Errorf("dev seed-external-identities: seal oauth placeholder: %w", oaErr)
			}
			if err := st.Meta().UpsertIdentitySubmission(ctx, store.IdentitySubmission{
				IdentityID:         devIdentityOAuthExt,
				SubmitHost:         workingHost,
				SubmitPort:         workingPort,
				SubmitSecurity:     workingSecurity,
				SubmitAuthMethod:   "oauth2",
				OAuthAccessCT:      oauthAccessCT,
				OAuthTokenEndpoint: gmailProv.TokenURL,
				OAuthClientID:      "alice-oauth@" + devSeedForeignDomain,
				State:              store.IdentitySubmissionStateAuthFailed,
				StateAt:            now,
				CreatedAt:          now,
				UpdatedAt:          now,
			}); err != nil {
				return fmt.Errorf("dev seed-external-identities: upsert oauth submission: %w", err)
			}
		}
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "dev-seed: seeded 4 external identities for %s on %s\n", principalEmail, devSeedForeignDomain)
	for _, entry := range devSeedEntries {
		fmt.Fprintf(w, "  identity_id=%s  email=%s  note=%s\n", entry.id, entry.email, entry.note)
	}
	if sinkAddr != "" {
		fmt.Fprintf(w, "  working-external sink: %s (security=none)\n", sinkAddr)
	}
	return nil
}
