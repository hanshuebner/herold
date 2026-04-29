package admin

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/auth"
)

func newAPIKeyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "api-key",
		Short: "API key management (create, revoke)",
	}
	createCmd := &cobra.Command{
		Use:   "create <principal-email>",
		Short: "issue an API key for a principal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			label, _ := cmd.Flags().GetString("label")
			scopeRaw, _ := cmd.Flags().GetString("scope")
			allowAdmin, _ := cmd.Flags().GetBool("allow-admin-scope")

			// REQ-AUTH-SCOPE-04: validate the scope list client-side
			// so a typo surfaces before the round-trip and the
			// operator gets a deterministic error message; the
			// server validates again as defence-in-depth.
			scopes, err := auth.ParseScopeList(scopeRaw)
			if err != nil {
				return fmt.Errorf("apikey: --scope: %w", err)
			}
			if len(scopes) == 0 {
				scopes = []auth.Scope{auth.ScopeMailSend}
			}
			hasAdmin := false
			for _, s := range scopes {
				if s == auth.ScopeAdmin {
					hasAdmin = true
				}
			}
			if hasAdmin && !allowAdmin {
				return errors.New("apikey: --scope contains admin; pass --allow-admin-scope to acknowledge (cookies recommended for human admin access)")
			}

			scopeStrs := make([]string, 0, len(scopes))
			for _, s := range scopes {
				scopeStrs = append(scopeStrs, string(s))
			}
			body := map[string]any{
				"principal":         args[0],
				"label":             label,
				"scope":             scopeStrs,
				"allow_admin_scope": allowAdmin,
			}
			var out map[string]any
			err = client.do(cmd.Context(), "POST", "/api/v1/api-keys", body, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeAPIKeyHuman(cmd.OutOrStdout(), out)
		},
	}
	createCmd.Flags().String("label", "", "operator-visible key label")
	createCmd.Flags().String("scope", "mail.send",
		"comma-separated scope list (one or more of "+
			"end-user, admin, mail.send, mail.receive, "+
			"chat.read, chat.write, cal.read, cal.write, "+
			"contacts.read, contacts.write, webhook.publish)")
	createCmd.Flags().Bool("allow-admin-scope", false,
		"required when --scope contains admin (REQ-AUTH-SCOPE-04)")
	c.AddCommand(createCmd)
	c.AddCommand(&cobra.Command{
		Use:   "revoke <key-id>",
		Short: "revoke an API key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "DELETE", "/api/v1/api-keys/"+args[0], nil, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "api key revoked: "+args[0])
			return nil
		},
	})

	// list: GET /api/v1/api-keys (caller's own keys) when --principal is
	// absent, or GET /api/v1/principals/{pid}/api-keys (admin-only)
	// when --principal resolves to a specific principal.
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "list API keys (own keys by default; admin-only --principal scopes to another)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			path := "/api/v1/api-keys"
			if ref, _ := cmd.Flags().GetString("principal"); ref != "" {
				pid, err := resolvePrincipalID(cmd.Context(), client, ref)
				if err != nil {
					return err
				}
				path = "/api/v1/principals/" + pid + "/api-keys"
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", path, nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeAPIKeyListHuman(cmd.OutOrStdout(), out)
		},
	}
	listCmd.Flags().String("principal", "",
		"principal email or numeric id (admin-only; defaults to listing the caller's own keys)")
	c.AddCommand(listCmd)
	return c
}
