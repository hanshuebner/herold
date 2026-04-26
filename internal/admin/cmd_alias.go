package admin

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// newAliasCmd registers the `herold alias ...` admin sub-command tree.
// Phase 3 Wave 3.4 ships add / list / delete; the REST surface
// (`/api/v1/aliases`) is wired and admin-gated. Multi-target aliases
// are not yet supported by the REST DTO; callers create multiple alias
// rows with the same `<addr>` to fan out (REQ-ADM-10).
func newAliasCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "alias",
		Short: "alias management (add, list, delete)",
	}

	addCmd := &cobra.Command{
		Use:   "add <addr> <target>",
		Short: "create an alias from <addr> to <target>",
		Long: "create an alias row that maps <addr> (local@domain) to <target> (an email " +
			"that resolves to a principal). Single-target only; multi-target fanout " +
			"is not yet wired in the REST surface — for fanout, create multiple alias " +
			"rows with the same <addr>.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			local, domain, err := splitEmail(args[0])
			if err != nil {
				return fmt.Errorf("alias add: addr: %w", err)
			}
			pid, err := resolvePrincipalID(cmd.Context(), client, args[1])
			if err != nil {
				return fmt.Errorf("alias add: target: %w", err)
			}
			body := map[string]any{
				"local":               local,
				"domain":              domain,
				"target_principal_id": mustParseUint(pid),
			}
			var out map[string]any
			err = client.do(cmd.Context(), "POST", "/api/v1/aliases", body, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	c.AddCommand(addCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "list aliases (optionally domain-scoped via --domain)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			path := "/api/v1/aliases"
			if d, _ := cmd.Flags().GetString("domain"); d != "" {
				path += "?domain=" + d
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", path, nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	listCmd.Flags().String("domain", "", "restrict to aliases on this domain")
	c.AddCommand(listCmd)

	c.AddCommand(&cobra.Command{
		Use:   "delete <id>",
		Short: "delete an alias by numeric id (use 'alias list' to find the id)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "DELETE", "/api/v1/aliases/"+args[0], nil, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "alias deleted: "+args[0])
			return nil
		},
	})
	return c
}

// splitEmail returns ("local", "domain", nil) for "local@domain" or an
// error otherwise. We deliberately accept any non-empty local part and
// any non-empty domain — exhaustive RFC 5322 parsing is the server's
// problem.
func splitEmail(addr string) (string, string, error) {
	i := strings.LastIndex(addr, "@")
	if i <= 0 || i == len(addr)-1 {
		return "", "", errors.New("not an email (need local@domain)")
	}
	return addr[:i], addr[i+1:], nil
}

// mustParseUint parses a numeric string as uint64. Caller has already
// validated through resolvePrincipalID, so a parse failure here is a
// programmer error; we return zero rather than panicking so the REST
// surface returns a clean validation_failed instead of crashing the CLI.
func mustParseUint(s string) uint64 {
	var n uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + uint64(r-'0')
	}
	return n
}
