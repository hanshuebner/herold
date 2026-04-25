package admin

import (
	"github.com/spf13/cobra"
)

// newDiagDNSCheckCmd is the `herold diag dns-check <domain>` leaf. It
// hits the admin REST surface and renders the publisher-side drift
// report for the named domain.
//
// Reconcile note: the parent `diag` cobra command lives in cmd_diag.go
// (parallel backup-migrate agent's file). Both surfaces register their
// leaves on that one parent so `herold diag --help` lists everything.
func newDiagDNSCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dns-check <domain>",
		Short: "compare herold-published DNS records for <domain> against live DNS state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/diag/dns-check/"+args[0], nil, &out); err != nil {
				return err
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
}
