package admin

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newMailboxCmd registers the `herold mailbox ...` admin sub-command
// tree. Phase 3 Wave 3.5c only ships the `set` verb, used to configure
// per-recipient inbound attachment policy (REQ-FLOW-ATTPOL-01).
func newMailboxCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "mailbox",
		Short: "per-mailbox configuration (attachment policy, ...)",
	}
	setCmd := &cobra.Command{
		Use:   "set <addr>",
		Short: "set per-mailbox attributes (currently --attpol)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			addr := args[0]
			attpol, _ := cmd.Flags().GetString("attpol")
			rejectText, _ := cmd.Flags().GetString("attpol-reject-text")
			if attpol == "" && !cmd.Flags().Changed("attpol-reject-text") {
				return fmt.Errorf("nothing to set: pass --attpol or --attpol-reject-text")
			}
			body := map[string]string{"policy": attpol, "reject_text": rejectText}
			err = client.do(cmd.Context(), "PUT",
				"/api/v1/mailboxes/"+addr+"/attachment-policy", body, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, fmt.Sprintf("mailbox %s attachment-policy set to %q", addr, attpol))
			return nil
		},
	}
	setCmd.Flags().String("attpol", "", "inbound attachment policy: accept | reject_at_data | empty to clear")
	setCmd.Flags().String("attpol-reject-text", "", "operator-overridable 552 5.3.4 reject text (REQ-FLOW-ATTPOL-01)")
	c.AddCommand(setCmd)
	return c
}
