package admin

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newOperatorCmd returns the "herold operator" subcommand tree for managing
// domain-scoped operators (REQ-ADM-307, re #145).
func newOperatorCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "operator",
		Short: "domain-scoped operator management (list, assign-domain, revoke-domain, show-domains)",
	}

	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list domain-scoped operators and their managed domains",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", "/api/v1/admin/operators", nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "show-domains <principal-id>",
		Short: "list the domains managed by a principal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET",
				fmt.Sprintf("/api/v1/principals/%s/managed-domains", args[0]),
				nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "assign-domain <principal-id> <domain>",
		Short: "add a domain to the principal's managed-domain set",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "POST",
				fmt.Sprintf("/api/v1/principals/%s/managed-domains", args[0]),
				map[string]string{"domain": args[1]}, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g,
				fmt.Sprintf("domain %s assigned to operator %s", args[1], args[0]))
			return nil
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "revoke-domain <principal-id> <domain>",
		Short: "remove a domain from the principal's managed-domain set",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "DELETE",
				fmt.Sprintf("/api/v1/principals/%s/managed-domains/%s", args[0], args[1]),
				nil, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g,
				fmt.Sprintf("domain %s revoked from operator %s", args[1], args[0]))
			return nil
		},
	})

	return c
}
