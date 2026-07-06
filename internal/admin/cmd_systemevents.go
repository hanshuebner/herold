package admin

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

// newSystemEventsCmd returns the "herold system-events" subcommand for reading
// the system events ring-buffer stream (REQ-ADM-304, re #142).
func newSystemEventsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "system-events",
		Short: "read the system events ring-buffer (mail-flow operational telemetry)",
	}
	c.AddCommand(newSystemEventsListCmd())
	return c
}

func newSystemEventsListCmd() *cobra.Command {
	var (
		action   string
		actorID  string
		since    string
		until    string
		limit    int
		beforeID uint64
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list system events newest-first with optional filters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			q := url.Values{}
			if action != "" {
				q.Set("action", action)
			}
			if actorID != "" {
				q.Set("actor_id", actorID)
			}
			if since != "" {
				q.Set("since", since)
			}
			if until != "" {
				q.Set("until", until)
			}
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			if beforeID > 0 {
				q.Set("before_id", fmt.Sprintf("%d", beforeID))
			}
			path := "/api/v1/admin/system-events"
			if len(q) > 0 {
				path = path + "?" + q.Encode()
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", path, nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	cmd.Flags().StringVar(&action, "action", "", "filter by action (exact match)")
	cmd.Flags().StringVar(&actorID, "actor-id", "", "filter by actor ID (exact match)")
	cmd.Flags().StringVar(&since, "since", "", "lower bound on event time (RFC3339)")
	cmd.Flags().StringVar(&until, "until", "", "upper bound on event time (RFC3339, exclusive)")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (default 100, max 1000)")
	cmd.Flags().Uint64Var(&beforeID, "before-id", 0, "keyset pagination cursor: return rows with ID < before-id")
	return cmd
}
