package admin

import (
	"github.com/spf13/cobra"
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
			body := map[string]string{
				"principal": args[0],
				"label":     label,
			}
			var out map[string]any
			err = client.do(cmd.Context(), "POST", "/api/v1/api-keys", body, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	createCmd.Flags().String("label", "", "operator-visible key label")
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
	return c
}
