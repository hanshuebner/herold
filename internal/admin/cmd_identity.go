package admin

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/cliout"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/identityverify"
	"github.com/hanshuebner/herold/internal/store"
)

// defaultIdentityID is the wire-form id of the synthesised default
// identity (REQ-IDENT-02). The synthesised default is verified-by-
// construction and has no row in jmap_identities; the admin CLI refuses
// verify/unverify on this id with a clear error.
const defaultIdentityID = "default"

// newIdentityCmd assembles the `herold identity` admin sub-command tree
// (verify / unverify / list / reissue-code). Documents REQ-IDENT-50..52.
//
// These commands open the store directly from --system-config rather
// than going through the admin REST surface. Identity verification is
// an operator-recovery affordance (REQ-IDENT-50): the CLI must work even
// when the REST layer or outbound queue is down. Each mutation emits an
// audit log entry through Metadata.AppendAuditLog.
func newIdentityCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "identity",
		Short: "Identity verification management (verify, unverify, list, reissue-code)",
		Long: "Operator-side admin surface for the per-principal Identity " +
			"verification flow (REQ-IDENT-50..52). Used for incident " +
			"recovery, bootstrap of import flows, and operator-assisted " +
			"setup when email delivery to the target is broken.\n\n" +
			"Subcommands:\n" +
			"  verify        mark a JMAP Identity verified\n" +
			"  unverify      revert a JMAP Identity to unverified\n" +
			"  list          list JMAP Identities with verification status\n" +
			"  reissue-code  issue a fresh verification code and print it " +
			"(testing aid)",
	}
	c.AddCommand(newIdentityVerifyCmd())
	c.AddCommand(newIdentityUnverifyCmd())
	c.AddCommand(newIdentityListCmd())
	c.AddCommand(newIdentityReissueCodeCmd())
	return c
}

// newIdentityVerifyCmd implements REQ-IDENT-50.
func newIdentityVerifyCmd() *cobra.Command {
	var operatorRef string
	cmd := &cobra.Command{
		Use:   "verify <identity-id>",
		Short: "mark a JMAP Identity verified (REQ-IDENT-50)",
		Long: "Sets verified_at_us = now on the Identity row identified " +
			"by <identity-id> and clears any pending verification token. " +
			"Audit-logged as identity.verify.admin. Refused on the " +
			"synthesised default identity (id \"default\") because the " +
			"default is verified by construction.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIdentityVerify(cmd, args[0], operatorRef)
		},
	}
	cmd.Flags().StringVar(&operatorRef, "operator", "",
		"operator principal (email or numeric id) recorded in the audit log; "+
			"defaults to a system actor when unset")
	return cmd
}

// newIdentityUnverifyCmd implements REQ-IDENT-51.
func newIdentityUnverifyCmd() *cobra.Command {
	var operatorRef string
	cmd := &cobra.Command{
		Use:   "unverify <identity-id>",
		Short: "revert a JMAP Identity to unverified (REQ-IDENT-51)",
		Long: "Clears verified_at_us on the Identity row identified by " +
			"<identity-id>. Used to revoke a compromised identity. " +
			"Audit-logged as identity.unverify. Refused on the " +
			"synthesised default identity (id \"default\") because the " +
			"default cannot be unverified.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIdentityUnverify(cmd, args[0], operatorRef)
		},
	}
	cmd.Flags().StringVar(&operatorRef, "operator", "",
		"operator principal (email or numeric id) recorded in the audit log; "+
			"defaults to a system actor when unset")
	return cmd
}

// newIdentityListCmd implements REQ-IDENT-52.
func newIdentityListCmd() *cobra.Command {
	var principalRef string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list JMAP Identities with verification status (REQ-IDENT-52)",
		Long: "Enumerates persisted Identity rows. With --principal, scope " +
			"is one principal; otherwise every principal in the store is " +
			"included. The synthesised default identity is rendered as the " +
			"first row of each principal's group (id \"default\", marked " +
			"verified by construction).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runIdentityList(cmd, principalRef)
		},
	}
	cmd.Flags().StringVar(&principalRef, "principal", "",
		"restrict listing to one principal (email or numeric id)")
	return cmd
}

// newIdentityReissueCodeCmd implements a testing aid: it re-issues the
// verification token + 6-digit code on an unverified Identity row and
// prints the plaintext code (and token / link) to stdout.
//
// The live verification flow stores only sha256 hashes of the token and
// code (migration 0048), so an in-flight code can never be recovered.
// This command therefore generates a *fresh* token+code pair, persists
// the new hashes and a fresh 24h expiry, and prints the plaintext so a
// tester has a usable code without having to receive the email.
func newIdentityReissueCodeCmd() *cobra.Command {
	var operatorRef string
	cmd := &cobra.Command{
		Use:   "reissue-code <identity-id>",
		Short: "issue a fresh verification code and print it (testing aid)",
		Long: "Generates a fresh verification token + 6-digit code for the " +
			"unverified Identity row identified by <identity-id>, persists " +
			"the new sha256 hashes and a fresh 24h expiry, and prints the " +
			"plaintext code, token, and verification link to stdout.\n\n" +
			"Because the live verification flow stores only the hashes, an " +
			"in-flight code cannot be recovered; this command always " +
			"re-issues. Intended for testing the verification flow without " +
			"receiving the verification email.\n\n" +
			"Audit-logged as identity.reissue-code.admin. Refused on the " +
			"synthesised default identity (id \"default\") because it has " +
			"no row and is verified by construction. Refused on an " +
			"already-verified Identity because re-issuing a code there is " +
			"meaningless; unverify it first if you intend to re-test.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIdentityReissueCode(cmd, args[0], operatorRef)
		},
	}
	cmd.Flags().StringVar(&operatorRef, "operator", "",
		"operator principal (email or numeric id) recorded in the audit log; "+
			"defaults to a system actor when unset")
	return cmd
}

// runIdentityReissueCode wires the reissue-code testing aid.
func runIdentityReissueCode(cmd *cobra.Command, identityID, operatorRef string) error {
	if identityID == defaultIdentityID {
		return errors.New("identity reissue-code: the default identity is verified by construction; nothing to re-issue")
	}
	g := globals(cmd.Context())
	cfg, err := requireConfig(g)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		return fmt.Errorf("identity reissue-code: open store: %w", err)
	}
	defer st.Close()

	row, err := st.Meta().GetJMAPIdentity(ctx, identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("identity reissue-code: identity %q not found", identityID)
		}
		return fmt.Errorf("identity reissue-code: lookup: %w", err)
	}
	if row.VerifiedAtUs != 0 {
		return fmt.Errorf("identity reissue-code: identity %q is already verified; "+
			"unverify it first if you intend to re-test", identityID)
	}

	// Generate a fresh token + code via the identityverify package; the
	// live flow (identityverify.Dispatcher) does exactly this. Persist
	// sha256 hashes of the ASCII forms and a fresh 24h expiry.
	token, _, err := identityverify.GenerateToken()
	if err != nil {
		return fmt.Errorf("identity reissue-code: generate token: %w", err)
	}
	code, err := identityverify.GenerateCode()
	if err != nil {
		return fmt.Errorf("identity reissue-code: generate code: %w", err)
	}
	tokenHash := sha256.Sum256([]byte(token))
	codeHash := sha256.Sum256([]byte(code))
	expiresAtUs := clk.Now().Add(identityverify.TokenTTL).UnixMicro()

	// ResetIdentityVerificationToken overwrites the verification trio
	// without rejecting a live token and without the resend rate-limit
	// gate — the right path for an operator-driven re-issue.
	if err := st.Meta().ResetIdentityVerificationToken(
		ctx, identityID, tokenHash[:], codeHash[:], expiresAtUs); err != nil {
		return fmt.Errorf("identity reissue-code: %w", err)
	}

	emitIdentityAudit(ctx, st, clk, "identity.reissue-code.admin", row, operatorRef,
		fmt.Sprintf("verification code re-issued for identity %s by admin CLI", identityID))

	w := cmd.OutOrStdout()
	link := "https://" + cfg.Server.Hostname + "/verify-identity?token=" + token
	fmt.Fprintf(w, "identity:   %s %s\n", row.ID, row.Email)
	fmt.Fprintf(w, "code:       %s\n", code)
	fmt.Fprintf(w, "token:      %s\n", token)
	fmt.Fprintf(w, "link:       %s\n", link)
	fmt.Fprintf(w, "expires-at: %s\n", cliout.FormatTimeValue(time.UnixMicro(expiresAtUs).UTC()))
	return nil
}

// runIdentityVerify wires REQ-IDENT-50.
func runIdentityVerify(cmd *cobra.Command, identityID, operatorRef string) error {
	if identityID == defaultIdentityID {
		return errors.New("identity verify: the default identity is verified by construction; nothing to do")
	}
	g := globals(cmd.Context())
	cfg, err := requireConfig(g)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		return fmt.Errorf("identity verify: open store: %w", err)
	}
	defer st.Close()

	row, err := st.Meta().GetJMAPIdentity(ctx, identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("identity verify: identity %q not found", identityID)
		}
		return fmt.Errorf("identity verify: lookup: %w", err)
	}

	if err := st.Meta().MarkIdentityVerified(ctx, identityID); err != nil {
		return fmt.Errorf("identity verify: %w", err)
	}

	// Audit. operator may be empty; auditActor falls back to system.
	emitIdentityAudit(ctx, st, clk, "identity.verify.admin", row, operatorRef,
		fmt.Sprintf("identity %s verified by admin CLI", identityID))

	writeLine(cmd.OutOrStdout(), g, fmt.Sprintf("verified %s %s", row.ID, row.Email))
	return nil
}

// runIdentityUnverify wires REQ-IDENT-51.
func runIdentityUnverify(cmd *cobra.Command, identityID, operatorRef string) error {
	if identityID == defaultIdentityID {
		return errors.New("identity unverify: the default identity cannot be unverified")
	}
	g := globals(cmd.Context())
	cfg, err := requireConfig(g)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		return fmt.Errorf("identity unverify: open store: %w", err)
	}
	defer st.Close()

	row, err := st.Meta().GetJMAPIdentity(ctx, identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("identity unverify: identity %q not found", identityID)
		}
		return fmt.Errorf("identity unverify: lookup: %w", err)
	}

	if err := st.Meta().UnmarkIdentityVerified(ctx, identityID); err != nil {
		return fmt.Errorf("identity unverify: %w", err)
	}

	emitIdentityAudit(ctx, st, clk, "identity.unverify", row, operatorRef,
		fmt.Sprintf("identity %s reverted to unverified by admin CLI", identityID))

	writeLine(cmd.OutOrStdout(), g, fmt.Sprintf("unverified %s %s", row.ID, row.Email))
	return nil
}

// runIdentityList wires REQ-IDENT-52.
func runIdentityList(cmd *cobra.Command, principalRef string) error {
	g := globals(cmd.Context())
	cfg, err := requireConfig(g)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		return fmt.Errorf("identity list: open store: %w", err)
	}
	defer st.Close()

	var principals []store.Principal
	if principalRef != "" {
		pid, err := resolvePrincipalFromStore(ctx, st, principalRef)
		if err != nil {
			return fmt.Errorf("identity list: %w", err)
		}
		p, err := st.Meta().GetPrincipalByID(ctx, pid)
		if err != nil {
			return fmt.Errorf("identity list: get principal: %w", err)
		}
		principals = []store.Principal{p}
	} else {
		// Page through all principals. ListPrincipals caps at 1000 per
		// call; loop until exhausted so operators never silently miss a
		// principal in a sparse-but-paginated store.
		var after store.PrincipalID
		for {
			page, err := st.Meta().ListPrincipals(ctx, after, 1000)
			if err != nil {
				return fmt.Errorf("identity list: list principals: %w", err)
			}
			if len(page) == 0 {
				break
			}
			principals = append(principals, page...)
			after = page[len(page)-1].ID
			if len(page) < 1000 {
				break
			}
		}
	}

	// Build the output rows. For each principal we emit a synthesised
	// "default" row first, then the persisted overlay rows in ascending
	// CreatedAtUs order.
	type listRow struct {
		PrincipalID string
		IdentityID  string
		Email       string
		Verified    string
		CreatedAt   string
		Note        string // "default" for the synthesised row.
	}
	var rows []listRow
	for _, p := range principals {
		// Synthesised default — always verified by construction, derived
		// from the principal row (REQ-IDENT-02). The CreatedAt mirrors
		// the principal's CreatedAt so the timestamp column has a value.
		rows = append(rows, listRow{
			PrincipalID: strconv.FormatUint(uint64(p.ID), 10),
			IdentityID:  defaultIdentityID,
			Email:       p.CanonicalEmail,
			Verified:    "yes",
			CreatedAt:   cliout.FormatTimeValue(p.CreatedAt),
			Note:        "default",
		})
		// Persisted overlay rows for this principal.
		ids, err := st.Meta().ListJMAPIdentities(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("identity list: list identities for principal %d: %w", p.ID, err)
		}
		sort.SliceStable(ids, func(i, j int) bool {
			if ids[i].CreatedAtUs != ids[j].CreatedAtUs {
				return ids[i].CreatedAtUs < ids[j].CreatedAtUs
			}
			return ids[i].ID < ids[j].ID
		})
		for _, r := range ids {
			rows = append(rows, listRow{
				PrincipalID: strconv.FormatUint(uint64(r.PrincipalID), 10),
				IdentityID:  r.ID,
				Email:       r.Email,
				Verified:    formatVerified(r.VerifiedAtUs),
				CreatedAt:   cliout.FormatTimeValue(time.UnixMicro(r.CreatedAtUs).UTC()),
			})
		}
	}

	w := cmd.OutOrStdout()
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no identities)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("PRINCIPAL-ID", "IDENTITY-ID", "EMAIL", "VERIFIED", "CREATED-AT", "NOTE")
	for _, r := range rows {
		t.Row(r.PrincipalID, r.IdentityID, r.Email, r.Verified, r.CreatedAt, r.Note)
	}
	return t.Flush()
}

// formatVerified renders the verified_at_us column for the human table.
// Zero -> "no"; non-zero -> "yes (<wallclock>)". The wallclock value is
// useful when a row was verified some time ago and the operator is
// triaging a compromised-identity report.
func formatVerified(verifiedAtUs int64) string {
	if verifiedAtUs == 0 {
		return "no"
	}
	return "yes (" + cliout.FormatTimeValue(time.UnixMicro(verifiedAtUs).UTC()) + ")"
}

// emitIdentityAudit writes one audit-log entry covering an identity
// mutation initiated by the admin CLI. The actor defaults to a system
// actor identified as "admin-cli"; an --operator flag (email or numeric
// id) overrides the actor to ActorPrincipal with the resolved id. Audit
// failures are logged but do not propagate — losing audit visibility on
// a successful mutation is preferable to rolling back the mutation.
func emitIdentityAudit(ctx context.Context, st store.Store, clk clock.Clock, action string, row store.JMAPIdentity, operatorRef, message string) {
	actorKind := store.ActorSystem
	actorID := "admin-cli"
	if operatorRef != "" {
		if pid, err := resolvePrincipalFromStore(ctx, st, operatorRef); err == nil {
			actorKind = store.ActorPrincipal
			actorID = strconv.FormatUint(uint64(pid), 10)
		}
	}
	meta := map[string]string{
		"identity_id":  row.ID,
		"email":        row.Email,
		"principal_id": strconv.FormatUint(uint64(row.PrincipalID), 10),
	}
	if operatorRef != "" {
		meta["operator"] = strings.ToLower(strings.TrimSpace(operatorRef))
	}
	_ = st.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:        clk.Now(),
		ActorKind: actorKind,
		ActorID:   actorID,
		Action:    action,
		Subject:   "identity:" + row.ID,
		Outcome:   store.OutcomeSuccess,
		Message:   message,
		Metadata:  meta,
	})
}
