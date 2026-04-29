package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newOIDCCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "oidc",
		Short: "external OIDC provider management",
	}
	prov := &cobra.Command{
		Use:   "provider",
		Short: "OIDC provider CRUD",
	}
	c.AddCommand(prov)

	addCmd := &cobra.Command{
		Use:   "add <name>",
		Short: "register a new OIDC provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			issuer, _ := cmd.Flags().GetString("issuer")
			clientID, _ := cmd.Flags().GetString("client-id")
			clientSecret, _ := cmd.Flags().GetString("client-secret")
			body := map[string]any{
				"name":          args[0],
				"issuer_url":    issuer,
				"client_id":     clientID,
				"client_secret": clientSecret,
			}
			err = client.do(cmd.Context(), "POST", "/api/v1/oidc/providers", body, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "provider added: "+args[0])
			return nil
		},
	}
	addCmd.Flags().String("issuer", "", "OIDC issuer URL")
	addCmd.Flags().String("client-id", "", "OAuth2 client ID")
	addCmd.Flags().String("client-secret", "", "OAuth2 client secret")
	_ = addCmd.MarkFlagRequired("issuer")
	_ = addCmd.MarkFlagRequired("client-id")
	_ = addCmd.MarkFlagRequired("client-secret")
	prov.AddCommand(addCmd)

	prov.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list OIDC providers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", "/api/v1/oidc/providers", nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeOIDCProviderListHuman(cmd.OutOrStdout(), out)
		},
	})
	prov.AddCommand(&cobra.Command{
		Use:   "remove <name-or-id>",
		Short: "remove an OIDC provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "DELETE", "/api/v1/oidc/providers/"+args[0], nil, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "provider removed: "+args[0])
			return nil
		},
	})

	prov.AddCommand(&cobra.Command{
		Use:   "show <id-or-name>",
		Short: "show one OIDC provider's configuration (secret omitted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", "/api/v1/oidc/providers/"+args[0], nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeOIDCProviderHuman(cmd.OutOrStdout(), out)
		},
	})

	updCmd := &cobra.Command{
		Use:   "update <id-or-name>",
		Short: "update mutable provider fields (e.g. rotate the client secret env reference)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			body := map[string]any{}
			if cmd.Flags().Changed("client-secret-env") {
				v, _ := cmd.Flags().GetString("client-secret-env")
				body["client_secret_env"] = v
			}
			if len(body) == 0 {
				return errors.New("oidc provider update: --client-secret-env is required")
			}
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "PATCH", "/api/v1/oidc/providers/"+args[0], body, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeOIDCProviderHuman(cmd.OutOrStdout(), out)
		},
	}
	updCmd.Flags().String("client-secret-env", "", "name of the environment variable holding the new client secret")
	prov.AddCommand(updCmd)

	c.AddCommand(&cobra.Command{
		Use:   "link-list <principal-email>",
		Short: "list a principal's external OIDC link mappings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			pid, err := resolvePrincipalID(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", "/api/v1/principals/"+pid+"/oidc-links", nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeOIDCLinkListHuman(cmd.OutOrStdout(), out)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "link-delete <principal-email> <provider-name>",
		Short: "remove one external-sub link from a principal",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			pid, err := resolvePrincipalID(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "DELETE", "/api/v1/principals/"+pid+"/oidc-links/"+args[1], nil, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "oidc link removed: "+args[0]+" -> "+args[1])
			return nil
		},
	})
	return c
}

// resolvePrincipalID accepts either a numeric principal id or a canonical
// email and resolves it to the numeric id by listing principals through
// the admin REST API. Numeric ids are returned verbatim.
func resolvePrincipalID(ctx context.Context, client *Client, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("principal reference required")
	}
	if _, err := strconv.ParseUint(ref, 10, 64); err == nil {
		return ref, nil
	}
	// List principals (the admin REST does not expose a by-email lookup
	// endpoint in Phase 1; we scan the first page, which is enough for the
	// 'list members of a small operator team' case the CLI is built for).
	var out struct {
		Items []struct {
			ID             uint64 `json:"id"`
			CanonicalEmail string `json:"canonical_email"`
		} `json:"items"`
	}
	if err := client.do(ctx, "GET", "/api/v1/principals?limit=1000", nil, &out); err != nil {
		return "", fmt.Errorf("resolve principal %q: %w", ref, err)
	}
	target := strings.ToLower(strings.TrimSpace(ref))
	for _, p := range out.Items {
		if strings.EqualFold(p.CanonicalEmail, target) {
			return strconv.FormatUint(p.ID, 10), nil
		}
	}
	return "", fmt.Errorf("principal %q not found", ref)
}
