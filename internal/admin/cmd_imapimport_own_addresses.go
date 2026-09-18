package admin

// cmd_imapimport_own_addresses.go — `herold imapimport own-addresses
// list|set` (re #396, third round, required outcome 1): the CLI half of
// the admin REST surface protoadmin/imap_import.go exposes for an IMAP-
// import account's own-address configuration
// (store.IMAPImportAccount.OwnAddresses/LearnedAddresses/
// AddressesLearnedAt). Both commands are thin HTTP-client wrappers, like
// `herold alias`/`herold identity`, rather than store-backed like `herold
// imapimport repair-orphans`: own-address configuration is ordinary
// principal-scoped account state an operator sets against a running
// server, not an offline maintenance pass.

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newIMAPImportOwnAddressesCmd registers `herold imapimport
// own-addresses list|set`.
func newIMAPImportOwnAddressesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "own-addresses",
		Short: "view or set an IMAP-import account's own-address list (re #396)",
	}
	c.AddCommand(newIMAPImportOwnAddressesListCmd())
	c.AddCommand(newIMAPImportOwnAddressesSetCmd())
	return c
}

// newIMAPImportOwnAddressesListCmd builds `herold imapimport
// own-addresses list <principal-email-or-id> [account-id]`.
func newIMAPImportOwnAddressesListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list <principal-email-or-id> [account-id]",
		Short: "list an account's configured and learned own addresses",
		Long: `Lists the own-address configuration/learning state for every IMAP-import
account owned by the given principal, or just the one named by
[account-id] when given: own_addresses (operator-configured, settable via
"own-addresses set"), learned_addresses (automatically learned from
Delivered-To/X-Original-To headers of imported mail), and
addresses_learned_at (absent until the one-shot learning pass has run for
that account).`,
		Args: cobra.RangeArgs(1, 2),
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
			var out struct {
				Items []map[string]any `json:"items"`
			}
			if err := client.do(cmd.Context(), "GET", "/api/v1/principals/"+pid+"/imap-imports", nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			items := out.Items
			if len(args) == 2 {
				filtered := items[:0]
				for _, it := range items {
					if id, _ := it["id"].(string); id == args[1] {
						filtered = append(filtered, it)
					}
				}
				items = filtered
				if len(items) == 0 {
					return fmt.Errorf("imapimport own-addresses list: account %q not found for this principal", args[1])
				}
			}
			return writeResult(cmd.OutOrStdout(), g, items)
		},
	}
	return c
}

// newIMAPImportOwnAddressesSetCmd builds `herold imapimport
// own-addresses set <principal-email-or-id> <account-id> [address...]`.
func newIMAPImportOwnAddressesSetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set <principal-email-or-id> <account-id> [address...]",
		Short: "replace an account's configured own-address list",
		Long: `Sets the account's admin-configured own-address list: addresses the
upstream mailbox is known to accept mail at that herold cannot derive
from the account's owning Identity, such as info@ or vorstand@ on a
shared organisational mailbox (re #396, third round). Pass no addresses
to clear the list. This is in addition to, not instead of, the addresses
herold learns automatically from Delivered-To/X-Original-To headers
(see "own-addresses list").`,
		Args: cobra.MinimumNArgs(2),
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
			accountID := args[1]
			addrs := args[2:]
			body := map[string]any{"own_addresses": addrs}
			var out map[string]any
			if err := client.do(cmd.Context(), "PATCH", "/api/v1/principals/"+pid+"/imap-imports/"+accountID, body, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	return c
}
