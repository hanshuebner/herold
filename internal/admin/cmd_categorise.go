package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newCategoriseCmd registers the `herold categorise ...` admin
// sub-command tree. Wave 3.4 ships `recategorise`; Wave 3.4-CAT adds
// the `prompt` and `list-categories` sub-commands (REQ-FILT-210..212).
func newCategoriseCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "categorise",
		Short: "LLM categorisation actions (recategorise, prompt, list-categories)",
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

	// prompt sub-tree: prompt set / prompt show (REQ-FILT-211).
	promptCmd := &cobra.Command{
		Use:   "prompt",
		Short: "manage the per-principal LLM categorisation prompt",
	}

	promptSetCmd := &cobra.Command{
		Use:   "set <email-or-id>",
		Short: "replace the categorisation prompt; reads from stdin by default",
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
			// Read the prompt body from --file or stdin.
			var body []byte
			filePath, _ := cmd.Flags().GetString("file")
			if filePath != "" {
				body, err = os.ReadFile(filePath)
				if err != nil {
					return fmt.Errorf("prompt set: read file %q: %w", filePath, err)
				}
			} else {
				body, err = io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("prompt set: read stdin: %w", err)
				}
			}
			prompt := strings.TrimRight(string(body), "\n")
			if prompt == "" {
				return errors.New("prompt set: prompt body is empty")
			}
			// GET existing config, merge prompt, PUT back.
			var existing map[string]any
			if err := client.do(cmd.Context(), "GET",
				"/api/v1/principals/"+pid+"/categorisation",
				nil, &existing); err != nil {
				return wrapPendingRESTError(err)
			}
			existing["prompt"] = prompt
			var out map[string]any
			if err := client.do(cmd.Context(), "PUT",
				"/api/v1/principals/"+pid+"/categorisation",
				existing, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	promptSetCmd.Flags().String("file", "", "read prompt from file instead of stdin")
	promptCmd.AddCommand(promptSetCmd)

	promptCmd.AddCommand(&cobra.Command{
		Use:   "show <email-or-id>",
		Short: "print the current categorisation prompt for a principal",
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
			var cfg struct {
				Prompt string `json:"prompt"`
			}
			if err := client.do(cmd.Context(), "GET",
				"/api/v1/principals/"+pid+"/categorisation",
				nil, &cfg); err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, cfg.Prompt)
			return nil
		},
	})
	c.AddCommand(promptCmd)

	// list-categories: print CategorySet names, one per line (REQ-FILT-212).
	c.AddCommand(&cobra.Command{
		Use:   "list-categories <email-or-id>",
		Short: "print the enabled category names for a principal, one per line",
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
			var cfg struct {
				CategorySet []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"category_set"`
			}
			if err := client.do(cmd.Context(), "GET",
				"/api/v1/principals/"+pid+"/categorisation",
				nil, &cfg); err != nil {
				return wrapPendingRESTError(err)
			}
			for _, cat := range cfg.CategorySet {
				writeLine(cmd.OutOrStdout(), g, cat.Name)
			}
			return nil
		},
	})

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
