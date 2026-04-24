package admin

import (
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
			return writeResult(cmd.OutOrStdout(), g, out)
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
	return c
}
