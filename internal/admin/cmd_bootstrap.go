package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

func newBootstrapCmd() *cobra.Command {
	var adminEmail string
	var password string
	var saveCredentialsFlag bool
	var serverURL string

	c := &cobra.Command{
		Use:   "bootstrap",
		Short: "one-shot bootstrap: create the first admin principal and API key",
		Long: `Opens the store referenced by --system-config, creates the first admin
principal if none exists, prints the credentials once, and saves the API
key to ~/.herold/credentials.toml (disable with --save-credentials=false).

Exits 10 if a principal already exists.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			existing, err := st.Meta().ListPrincipals(ctx, 0, 1)
			if err != nil {
				return fmt.Errorf("bootstrap: list principals: %w", err)
			}
			if len(existing) > 0 {
				return withExit(errors.New("bootstrap: a principal already exists; refusing to run"), ExitBootstrapAlreadyDone)
			}
			if adminEmail == "" {
				return errors.New("bootstrap: --email required")
			}
			if password == "" {
				pw, err := generateRandomPassword()
				if err != nil {
					return fmt.Errorf("bootstrap: generate password: %w", err)
				}
				password = pw
			}
			dir := directory.New(st.Meta(), discardLogger(), clk, nil)
			pid, err := dir.CreatePrincipal(ctx, adminEmail, password)
			if err != nil {
				return fmt.Errorf("bootstrap: create admin principal: %w", err)
			}
			// First principal is the operator-admin: set PrincipalFlagAdmin so
			// the printed API key can run admin-gated REST mutations
			// (domain add, principal create, etc.). Every later principal
			// is non-admin by default; the admin flag is granted via
			// PATCH /api/v1/principals/{pid}.
			p, err := st.Meta().GetPrincipalByID(ctx, pid)
			if err != nil {
				return fmt.Errorf("bootstrap: load admin principal: %w", err)
			}
			p.Flags |= store.PrincipalFlagAdmin
			if err := st.Meta().UpdatePrincipal(ctx, p); err != nil {
				return fmt.Errorf("bootstrap: grant admin flag: %w", err)
			}
			keyPlain, keyHash, err := generateAPIKey()
			if err != nil {
				return fmt.Errorf("bootstrap: generate api key: %w", err)
			}
			if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
				PrincipalID: pid,
				Hash:        keyHash,
				Name:        "bootstrap",
			}); err != nil {
				return fmt.Errorf("bootstrap: insert api key: %w", err)
			}
			// Derive the admin REST URL from the system config when the
			// operator has not supplied one explicitly via --server-url.
			effectiveServerURL := serverURL
			if effectiveServerURL == "" {
				if derived, warns, ok := sysconfig.AdminRESTURL(cfg); ok {
					effectiveServerURL = derived
					for _, w := range warns {
						fmt.Fprintf(cmd.ErrOrStderr(), "bootstrap: warn: %s\n", w)
					}
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"bootstrap: warn: no [[listener]] with kind=\"admin\" found in system config; "+
							"set server_url manually in ~/.herold/credentials.toml or pass "+
							"--server-url to subsequent CLI calls\n",
					)
				}
			}
			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "bootstrap: first admin principal created")
			fmt.Fprintf(w, "  email:   %s\n", adminEmail)
			fmt.Fprintf(w, "  password: %s\n", password)
			fmt.Fprintf(w, "  api_key: %s\n", keyPlain)
			fmt.Fprintln(w, "keep these values; they are not recoverable from the server after this point.")
			if saveCredentialsFlag {
				path, savedURL, err := saveCredentials(keyPlain, effectiveServerURL, cmd.ErrOrStderr())
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "bootstrap: warn: could not save credentials: %v\n", err)
				} else {
					fmt.Fprintf(w, "credentials saved to %s\n", path)
					if savedURL != "" {
						fmt.Fprintf(w, "  server_url: %s\n", savedURL)
					}
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&adminEmail, "email", "", "admin email address (required)")
	c.Flags().StringVar(&password, "password", "", "admin password; generated when empty")
	c.Flags().BoolVar(&saveCredentialsFlag, "save-credentials", true, "write the API key to ~/.herold/credentials.toml")
	c.Flags().StringVar(&serverURL, "server-url", "", "record this URL in the saved credentials file")
	return c
}

// generateRandomPassword returns a 20-character base64url password.
func generateRandomPassword() (string, error) {
	var b [15]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// generateAPIKey returns a plaintext API key and its stored-hash form.
// The hash uses the same SHA-256 encoding as protoadmin.HashAPIKey so a
// key minted by `herold bootstrap` round-trips through the admin REST
// API's Bearer authentication. STANDARDS §9 forbids storing
// authentication material unhashed, so the plaintext is hashed before
// being persisted.
func generateAPIKey() (plain, hash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	plain = protoadmin.APIKeyPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	hash = protoadmin.HashAPIKey(plain)
	return plain, hash, nil
}

// discardLogger returns a slog.Logger that drops every record. Used for
// CLI command paths (bootstrap) that run without an observability setup.
func discardLogger() *slog.Logger {
	return slog.New(discardHandler{})
}

// discardHandler is a slog.Handler that suppresses every record; the
// bootstrap command wants no log output on success.
type discardHandler struct{}

func (discardHandler) Enabled(_ context.Context, _ slog.Level) bool  { return false }
func (discardHandler) Handle(_ context.Context, _ slog.Record) error { return nil }
func (h discardHandler) WithAttrs(_ []slog.Attr) slog.Handler        { return h }
func (h discardHandler) WithGroup(_ string) slog.Handler             { return h }
