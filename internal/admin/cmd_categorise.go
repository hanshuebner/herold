package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// newCategoriseCmd registers the `herold categorise ...` admin
// sub-command tree. Phase 3 Wave 3.4 ships `recategorise`, which kicks
// off the existing internal/categorise/recategorise.go job through the
// REST surface (POST /api/v1/principals/{pid}/recategorise) and then
// polls /api/v1/jobs/{id} until the job completes. --async returns the
// job id immediately for callers that prefer to drive their own poll.
func newCategoriseCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "categorise",
		Short: "LLM categorisation actions (recategorise)",
	}

	recatCmd := &cobra.Command{
		Use:   "recategorise <email-or-id>",
		Short: "re-run the LLM categoriser on a principal's recent inbox messages",
		Args:  cobra.ExactArgs(1),
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
			limit, _ := cmd.Flags().GetInt("limit")
			path := "/api/v1/principals/" + pid + "/recategorise"
			if limit > 0 {
				path += "?limit=" + strconv.Itoa(limit)
			}
			var resp struct {
				Enqueued bool   `json:"enqueued"`
				JobID    string `json:"jobId"`
			}
			if err := client.do(cmd.Context(), "POST", path, nil, &resp); err != nil {
				return wrapPendingRESTError(err)
			}
			if !resp.Enqueued || resp.JobID == "" {
				return errors.New("recategorise: server did not enqueue a job")
			}
			async, _ := cmd.Flags().GetBool("async")
			if async {
				return writeResult(cmd.OutOrStdout(), g, map[string]any{
					"jobId":    resp.JobID,
					"enqueued": true,
				})
			}
			interval, _ := cmd.Flags().GetDuration("poll-interval")
			if interval <= 0 {
				interval = 500 * time.Millisecond
			}
			final, err := pollCategoriseJob(cmd.Context(), client, resp.JobID, interval,
				func(done, total int) {
					writeLine(cmd.OutOrStdout(), g,
						fmt.Sprintf("recategorise: %d/%d", done, total))
				})
			if err != nil {
				return err
			}
			return writeResult(cmd.OutOrStdout(), g, final)
		},
	}
	recatCmd.Flags().Int("limit", 0, "max messages to recategorise (server caps at 10000)")
	recatCmd.Flags().Bool("async", false,
		"return the job id immediately instead of polling to completion")
	recatCmd.Flags().Duration("poll-interval", 500*time.Millisecond,
		"poll cadence for /api/v1/jobs/{id}")
	c.AddCommand(recatCmd)
	return c
}

// pollCategoriseJob blocks until the job at /api/v1/jobs/{id} reaches
// state in {done, failed} or ctx is cancelled. progress is invoked on
// every snapshot whose Done changes; final is the last snapshot returned.
func pollCategoriseJob(ctx context.Context, client *Client, jobID string, interval time.Duration, progress func(done, total int)) (map[string]any, error) {
	lastDone := -1
	for {
		var snap map[string]any
		if err := client.do(ctx, "GET", "/api/v1/jobs/"+jobID, nil, &snap); err != nil {
			return nil, fmt.Errorf("poll job %s: %w", jobID, err)
		}
		state, _ := snap["state"].(string)
		done := intFromAny(snap["done"])
		total := intFromAny(snap["total"])
		if progress != nil && done != lastDone {
			progress(done, total)
			lastDone = done
		}
		switch state {
		case "done":
			return snap, nil
		case "failed":
			msg, _ := snap["err"].(string)
			if msg == "" {
				msg = "job failed"
			}
			return snap, fmt.Errorf("recategorise: %s", msg)
		}
		select {
		case <-ctx.Done():
			return snap, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// intFromAny coerces a JSON number (decoded as float64) or numeric
// string into an int. Returns 0 on type mismatch.
func intFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		n, err := strconv.Atoi(x)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}
