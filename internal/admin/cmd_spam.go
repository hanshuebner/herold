package admin

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newSpamCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "spam",
		Short: "spam classifier policy inspection and update",
	}
	c.AddCommand(&cobra.Command{
		Use:   "policy-show",
		Short: "print the active spam policy (plugin, threshold, model)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/spam/policy", nil, &out); err != nil {
				return err
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	setCmd := &cobra.Command{
		Use:   "policy-set",
		Short: "update the active spam policy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			plugin, _ := cmd.Flags().GetString("plugin")
			threshold, _ := cmd.Flags().GetFloat64("threshold")
			model, _ := cmd.Flags().GetString("model")
			promptFile, _ := cmd.Flags().GetString("system-prompt-file")
			if plugin == "" {
				return errors.New("spam policy-set: --plugin is required")
			}
			if threshold < 0 || threshold > 1 {
				return errors.New("spam policy-set: --threshold must be in [0.0, 1.0]")
			}
			body := map[string]any{
				"plugin_name": plugin,
				"threshold":   threshold,
			}
			if model != "" {
				body["model"] = model
			}
			if promptFile != "" {
				raw, err := os.ReadFile(promptFile)
				if err != nil {
					return fmt.Errorf("read system-prompt-file %q: %w", promptFile, err)
				}
				body["system_prompt_override"] = string(raw)
			}
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "PUT", "/api/v1/spam/policy", body, &out); err != nil {
				return err
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	setCmd.Flags().String("plugin", "", "plugin name (required)")
	setCmd.Flags().Float64("threshold", 0.7, "spam-score threshold in [0.0, 1.0]")
	setCmd.Flags().String("model", "", "optional classifier model identifier")
	setCmd.Flags().String("system-prompt-file", "", "path to a file containing a system-prompt override")
	c.AddCommand(setCmd)
	return c
}
