package admin

import (
	"fmt"

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

	// Phase 3 Wave 3.5c: per-domain inbound attachment policy
	// (REQ-FLOW-ATTPOL-01). The verb deliberately stays narrow —
	// future per-domain attributes extend the same `set` subcommand.
	setCmd := &cobra.Command{
		Use:   "set <domain>",
		Short: "set per-domain attributes (currently --attpol)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			domain := args[0]
			attpol, _ := cmd.Flags().GetString("attpol")
			rejectText, _ := cmd.Flags().GetString("attpol-reject-text")
			if attpol == "" && !cmd.Flags().Changed("attpol-reject-text") {
				return fmt.Errorf("nothing to set: pass --attpol or --attpol-reject-text")
			}
			body := map[string]string{"policy": attpol, "reject_text": rejectText}
			err = client.do(cmd.Context(), "PUT",
				"/api/v1/domains/"+domain+"/attachment-policy", body, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, fmt.Sprintf("domain %s attachment-policy set to %q", domain, attpol))
			return nil
		},
	}
	setCmd.Flags().String("attpol", "", "inbound attachment policy: accept | reject_at_data | empty to clear")
	setCmd.Flags().String("attpol-reject-text", "", "operator-overridable 552 5.3.4 reject text (REQ-FLOW-ATTPOL-01)")
	c.AddCommand(setCmd)
	return c
}
