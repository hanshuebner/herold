package admin

import (
	"github.com/spf13/cobra"
)

func newDomainCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "domain",
		Short: "domain management (add, remove, list)",
	}
	c.AddCommand(&cobra.Command{
		Use:   "add <name>",
		Short: "register a local domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "POST", "/api/v1/domains", map[string]string{"name": args[0]}, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "domain added: "+args[0])
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "remove <name>",
		Short: "remove a local domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "DELETE", "/api/v1/domains/"+args[0], nil, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "domain removed: "+args[0])
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list local domains",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", "/api/v1/domains", nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})
	return c
}
