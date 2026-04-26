package admin

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newAuditCmd registers the `herold audit ...` admin sub-command tree.
// Phase 3 Wave 3.4 ships `list`. REQ-ADM-19 lists "since, actor, action,
// resource" filters; the wired REST surface today exposes `since`,
// `until`, `action`, `principal_id`, `limit`, `after_id`. The CLI maps
// `--actor` -> client-side email-to-pid resolution -> REST
// `principal_id`. `--resource` is not wired in the REST surface yet
// and is currently ignored (with a warning).
func newAuditCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "audit",
		Short: "audit log inspection (list)",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "list audit log entries with optional --since/--actor/--action/--limit/--resource filters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			vals := url.Values{}
			if raw, _ := cmd.Flags().GetString("since"); raw != "" {
				ts, err := parseSince(raw, time.Now())
				if err != nil {
					return fmt.Errorf("audit list: --since: %w", err)
				}
				vals.Set("since", ts.UTC().Format(time.RFC3339))
			}
			if raw, _ := cmd.Flags().GetString("actor"); raw != "" {
				pid, err := resolvePrincipalID(cmd.Context(), client, raw)
				if err != nil {
					return fmt.Errorf("audit list: --actor: %w", err)
				}
				vals.Set("principal_id", pid)
			}
			if raw, _ := cmd.Flags().GetString("action"); raw != "" {
				vals.Set("action", raw)
			}
			if n, _ := cmd.Flags().GetInt("limit"); n > 0 {
				vals.Set("limit", strconv.Itoa(n))
			}
			if raw, _ := cmd.Flags().GetString("resource"); raw != "" {
				// REST surface today does not expose resource as a filter
				// (REQ-ADM-19 names it; the gap is tracked in the
				// admin-cli triage doc). Warn rather than silently drop.
				fmt.Fprintln(cmd.ErrOrStderr(),
					"warning: --resource is not wired in the REST surface yet; ignored")
			}
			path := "/api/v1/audit"
			if q := vals.Encode(); q != "" {
				path += "?" + q
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", path, nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	listCmd.Flags().String("since", "",
		"earliest entry time (RFC3339 absolute or relative duration: 1h, 30m, 24h)")
	listCmd.Flags().String("actor", "",
		"actor (principal email or numeric id); resolved to principal_id client-side")
	listCmd.Flags().String("action", "", "action verb (e.g. principal.delete)")
	listCmd.Flags().Int("limit", 0, "page size (server caps at 1000)")
	listCmd.Flags().String("resource", "",
		"resource filter (REQ-ADM-19); not yet wired in the REST surface — currently ignored with a warning")
	c.AddCommand(listCmd)
	return c
}

// parseSince accepts either an RFC3339 timestamp or a relative duration
// (e.g. 1h, 30m, 24h) anchored at now. Returns the resolved UTC time.
func parseSince(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("not RFC3339 and not a duration: %w", err)
	}
	if d < 0 {
		return time.Time{}, fmt.Errorf("negative duration not accepted")
	}
	return now.Add(-d), nil
}
