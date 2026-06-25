package admin

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// newRecoverCmd builds the `herold recover` subcommand. It opens the
// store directly (offline, like `bootstrap`) so no running server is
// needed. Host access is already full trust; the command makes recovery
// formal, audited, and safer than direct DB edits (re #24).
//
// Supported recovery actions:
//
//   - --reset-totp    clear the TOTP secret and disable the TOTPEnabled flag
//   - --mint-api-key  mint a fresh one-shot Bearer key for TOTP re-enrollment
//
// At least one of the two action flags must be supplied. The command
// composes cleanly with the one-shot bootstrap-key model (re #21): a
// minted recovery key is also one-shot and is consumed on the first
// successful POST /api/v1/principals/{pid}/totp/confirm.
func newRecoverCmd() *cobra.Command {
	var adminEmail string
	var resetTOTP bool
	var mintAPIKey bool
	var saveCredentialsFlag bool
	var serverURL string

	c := &cobra.Command{
		Use:   "recover",
		Short: "offline admin-access recovery: reset TOTP and/or mint a new one-shot API key",
		Long: `Opens the store referenced by --system-config without a running server.
Finds the named principal by email and applies the requested recovery
actions.  Requires host access (same trust level as direct DB surgery).

At least one of --reset-totp and --mint-api-key must be supplied.

--reset-totp clears the principal's TOTP secret and disables the
TOTPEnabled flag.  The principal can subsequently re-enroll via the
TOTP endpoints.

--mint-api-key mints a fresh one-shot Bearer token for the principal.
The key is consumed automatically after the first successful
POST /api/v1/principals/{pid}/totp/confirm (re #21).  The key is
printed and optionally saved to ~/.herold/credentials.toml.

Audit entries are written to the store for every action performed.
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !resetTOTP && !mintAPIKey {
				return errors.New("recover: at least one of --reset-totp or --mint-api-key is required")
			}
			if adminEmail == "" {
				return errors.New("recover: --email is required")
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
				return err
			}
			defer st.Close()

			p, err := st.Meta().GetPrincipalByEmail(ctx, adminEmail)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("recover: principal %q not found", adminEmail)
				}
				return fmt.Errorf("recover: look up principal: %w", err)
			}

			w := cmd.OutOrStdout()

			if resetTOTP {
				p.TOTPSecret = nil
				p.Flags &^= store.PrincipalFlagTOTPEnabled
				if err := st.Meta().UpdatePrincipal(ctx, p); err != nil {
					return fmt.Errorf("recover: clear TOTP: %w", err)
				}
				entry := store.AuditLogEntry{
					At:        clk.Now(),
					ActorKind: store.ActorSystem,
					ActorID:   "system",
					Action:    "principal.totp.reset",
					Subject:   fmt.Sprintf("principal:%d", p.ID),
					Outcome:   store.OutcomeSuccess,
					Message:   "TOTP enrollment cleared by herold recover (re #24)",
				}
				_ = st.Meta().AppendAuditLog(ctx, entry)
				fmt.Fprintf(w, "recover: TOTP enrollment cleared for %s\n", adminEmail)
			}

			if mintAPIKey {
				keyPlain, keyHash, err := generateAPIKey()
				if err != nil {
					return fmt.Errorf("recover: generate api key: %w", err)
				}
				if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
					PrincipalID: p.ID,
					Hash:        keyHash,
					Name:        "recovery",
					OneShot:     true,
				}); err != nil {
					return fmt.Errorf("recover: insert api key: %w", err)
				}
				entry := store.AuditLogEntry{
					At:        clk.Now(),
					ActorKind: store.ActorSystem,
					ActorID:   "system",
					Action:    "apikey.recovery.create",
					Subject:   fmt.Sprintf("principal:%d", p.ID),
					Outcome:   store.OutcomeSuccess,
					Message:   "one-shot recovery API key minted by herold recover (re #24)",
				}
				_ = st.Meta().AppendAuditLog(ctx, entry)

				effectiveServerURL := serverURL
				if effectiveServerURL == "" {
					if derived, warns, ok := sysconfig.AdminRESTURL(cfg); ok {
						effectiveServerURL = derived
						for _, warn := range warns {
							fmt.Fprintf(cmd.ErrOrStderr(), "recover: warn: %s\n", warn)
						}
					}
				}
				fmt.Fprintln(w, "recover: one-shot recovery API key minted")
				fmt.Fprintf(w, "  email:   %s\n", adminEmail)
				fmt.Fprintf(w, "  api_key: %s\n", keyPlain)
				fmt.Fprintln(w, "the key is consumed after the first successful TOTP confirm.")

				if saveCredentialsFlag {
					path, savedURL, err := saveCredentials(keyPlain, effectiveServerURL, cmd.ErrOrStderr())
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "recover: warn: could not save credentials: %v\n", err)
					} else {
						fmt.Fprintf(w, "credentials saved to %s\n", path)
						if savedURL != "" {
							fmt.Fprintf(w, "  server_url: %s\n", savedURL)
						}
					}
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&adminEmail, "email", "", "principal email address (required)")
	c.Flags().BoolVar(&resetTOTP, "reset-totp", false, "clear TOTP enrollment for the principal")
	c.Flags().BoolVar(&mintAPIKey, "mint-api-key", false, "mint a one-shot Bearer key for TOTP re-enrollment")
	c.Flags().BoolVar(&saveCredentialsFlag, "save-credentials", true, "write the API key to ~/.herold/credentials.toml")
	c.Flags().StringVar(&serverURL, "server-url", "", "record this URL in the saved credentials file")
	return c
}
