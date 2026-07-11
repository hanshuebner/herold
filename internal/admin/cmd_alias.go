package admin

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
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
		Long: "create an alias row that maps <addr> (local@domain) to <target>. <target> is " +
			"either an internal principal (a numeric principal id or a local principal's " +
			"canonical email) or an external email address the alias forwards inbound mail " +
			"to (re #181). Single-target only; multi-target fanout is not yet wired in the " +
			"REST surface — for fanout, create multiple alias rows with the same <addr>.",
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
			body := map[string]any{
				"local":  local,
				"domain": domain,
			}
			switch kind, val, terr := resolveAliasTarget(cmd.Context(), client, args[1]); {
			case terr != nil:
				return fmt.Errorf("alias add: target: %w", terr)
			case kind == aliasTargetPrincipal:
				body["target_principal_id"] = mustParseUint(val)
			default: // aliasTargetExternal
				body["target_address"] = val
			}
			var out map[string]any
			err = client.do(cmd.Context(), "POST", "/api/v1/aliases", body, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeAliasHuman(cmd.OutOrStdout(), out)
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
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeAliasListHuman(cmd.OutOrStdout(), out)
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

// aliasTargetKind distinguishes the two shapes `herold alias add`
// accepts for its <target> argument (re #181).
type aliasTargetKind int

const (
	// aliasTargetPrincipal means the returned value is a numeric
	// principal id (for "target_principal_id").
	aliasTargetPrincipal aliasTargetKind = iota
	// aliasTargetExternal means the returned value is a lower-cased
	// external addr-spec (for "target_address").
	aliasTargetExternal
)

// resolveAliasTarget classifies <target> for `herold alias add`: a
// numeric principal id or a local principal's canonical email resolves
// to aliasTargetPrincipal; any other well-formed email address is
// treated as an external forwarding target (aliasTargetExternal,
// re #181). Anything else is a validation error.
func resolveAliasTarget(ctx context.Context, client *Client, ref string) (aliasTargetKind, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, "", errors.New("target required")
	}
	if _, err := strconv.ParseUint(ref, 10, 64); err == nil {
		return aliasTargetPrincipal, ref, nil
	}
	// List principals (the admin REST does not expose a by-email lookup
	// endpoint in Phase 1; scanning the first page is enough for the
	// "small operator team" case the CLI is built for).
	var out struct {
		Items []struct {
			ID             uint64 `json:"id"`
			CanonicalEmail string `json:"canonical_email"`
		} `json:"items"`
	}
	if err := client.do(ctx, "GET", "/api/v1/principals?limit=1000", nil, &out); err != nil {
		return 0, "", fmt.Errorf("list principals: %w", err)
	}
	lower := strings.ToLower(ref)
	for _, p := range out.Items {
		if strings.EqualFold(p.CanonicalEmail, lower) {
			return aliasTargetPrincipal, strconv.FormatUint(p.ID, 10), nil
		}
	}
	// Not a local principal. Accept it as an external forwarding target
	// when it is a well-formed email address.
	addr, err := mail.ParseAddress(ref)
	if err != nil {
		return 0, "", fmt.Errorf("%q is neither a known principal (id or email) nor a valid external email address", ref)
	}
	return aliasTargetExternal, strings.ToLower(addr.Address), nil
}
