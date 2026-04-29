package admin

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

func newQueueCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "queue",
		Short: "outbound queue inspection and operator actions",
	}
	c.AddCommand(newQueueListCmd())
	c.AddCommand(newQueueShowCmd())
	c.AddCommand(newQueueRetryCmd())
	c.AddCommand(newQueueHoldCmd())
	c.AddCommand(newQueueReleaseCmd())
	c.AddCommand(newQueueDeleteCmd())
	c.AddCommand(newQueueStatsCmd())
	c.AddCommand(newQueueFlushCmd())
	return c
}

func newQueueListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list queue items, filtered by state / principal / cursor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			state, _ := cmd.Flags().GetString("state")
			principal, _ := cmd.Flags().GetString("principal")
			limit, _ := cmd.Flags().GetInt("limit")
			after, _ := cmd.Flags().GetString("after")
			vals := url.Values{}
			if state != "" {
				vals.Set("state", state)
			}
			if principal != "" {
				vals.Set("principal_id", principal)
			}
			if limit > 0 {
				vals.Set("limit", fmt.Sprintf("%d", limit))
			}
			if after != "" {
				vals.Set("after_id", after)
			}
			path := "/api/v1/queue"
			if q := vals.Encode(); q != "" {
				path += "?" + q
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", path, nil, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeQueueListHuman(cmd.OutOrStdout(), out)
		},
	}
	cmd.Flags().String("state", "", "queued|deferred|inflight|done|failed|held")
	cmd.Flags().String("principal", "", "principal id (numeric)")
	cmd.Flags().Int("limit", 0, "max rows (server caps at 1000)")
	cmd.Flags().String("after", "", "keyset cursor: only rows with id > this")
	return cmd
}

func newQueueShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "show one queue item by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/queue/"+args[0], nil, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeQueueItemHuman(cmd.OutOrStdout(), out)
		},
	}
}

func newQueueRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry <id>",
		Short: "reschedule the queue item for immediate retry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			if err := client.do(cmd.Context(), "POST", "/api/v1/queue/"+args[0]+"/retry", nil, nil); err != nil {
				return err
			}
			writeLine(cmd.OutOrStdout(), g, "queue item retried: "+args[0])
			return nil
		},
	}
}

func newQueueHoldCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hold <id>",
		Short: "move the queue item to held state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			if err := client.do(cmd.Context(), "POST", "/api/v1/queue/"+args[0]+"/hold", nil, nil); err != nil {
				return err
			}
			writeLine(cmd.OutOrStdout(), g, "queue item held: "+args[0])
			return nil
		},
	}
}

func newQueueReleaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "release <id>",
		Short: "move a held queue item back to queued",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			if err := client.do(cmd.Context(), "POST", "/api/v1/queue/"+args[0]+"/release", nil, nil); err != nil {
				return err
			}
			writeLine(cmd.OutOrStdout(), g, "queue item released: "+args[0])
			return nil
		},
	}
}

func newQueueDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "delete a queue item (operator force-delete)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			force, _ := cmd.Flags().GetBool("force")
			if !force {
				if err := confirmYes(cmd, fmt.Sprintf("delete queue item %s? type 'yes' to confirm: ", args[0])); err != nil {
					return err
				}
			}
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			if err := client.do(cmd.Context(), "DELETE", "/api/v1/queue/"+args[0], nil, nil); err != nil {
				return err
			}
			writeLine(cmd.OutOrStdout(), g, "queue item deleted: "+args[0])
			return nil
		},
	}
	cmd.Flags().Bool("force", false, "skip the interactive confirmation prompt")
	return cmd
}

func newQueueStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "print queue counts by lifecycle state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/queue/stats", nil, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeQueueStatsHuman(cmd.OutOrStdout(), out)
		},
	}
}

func newQueueFlushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flush",
		Short: "bump every row in --state to retry now (only state=deferred is allowed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			state, _ := cmd.Flags().GetString("state")
			force, _ := cmd.Flags().GetBool("force")
			if state == "" {
				return errors.New("queue flush: --state is required")
			}
			if !force {
				if err := confirmYes(cmd, fmt.Sprintf("flush all %s queue rows to retry now? type 'yes' to confirm: ", state)); err != nil {
					return err
				}
			}
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "POST", "/api/v1/queue/flush?state="+url.QueryEscape(state), nil, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeQueueFlushHuman(cmd.OutOrStdout(), out)
		},
	}
	cmd.Flags().String("state", "", "lifecycle state to flush (only 'deferred' is currently supported)")
	cmd.Flags().Bool("force", false, "skip the interactive confirmation prompt")
	return cmd
}

// confirmYes prompts the user on cmd's Stdin and requires the literal
// "yes" answer (case-insensitive). Anything else aborts the command.
func confirmYes(cmd *cobra.Command, prompt string) error {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	var ans string
	if _, err := fmt.Fscanln(cmd.InOrStdin(), &ans); err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(ans), "yes") {
		return errors.New("aborted by user")
	}
	return nil
}
