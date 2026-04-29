package admin

import (
	"github.com/spf13/cobra"
)

func newCertCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cert",
		Short: "ACME-managed certificate inspection and renewal",
	}
	c.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "list every ACME-managed cert with expiry and issuer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/certs", nil, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeCertListHuman(cmd.OutOrStdout(), out)
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "show <hostname>",
		Short: "show cert detail (chain PEM, issuer, validity window)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/certs/"+args[0], nil, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeCertHuman(cmd.OutOrStdout(), out)
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "renew <hostname>",
		Short: "trigger an immediate ACME renewal for hostname; blocks until done",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "POST", "/api/v1/certs/"+args[0]+"/renew", nil, &out); err != nil {
				return err
			}
			writeLine(cmd.OutOrStdout(), g, "cert renewed: "+args[0])
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeCertHuman(cmd.OutOrStdout(), out)
		},
	})
	return c
}
