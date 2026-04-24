package admin

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/appconfig"
	"github.com/hanshuebner/herold/internal/clock"
)

func newAppConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "app-config",
		Short: "application-config dump / load (REQ-OPS-24)",
	}
	c.AddCommand(&cobra.Command{
		Use:   "dump [path]",
		Short: "export the application config to a TOML file (or stdout when no path)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			st, err := openStore(cmd.Context(), cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return err
			}
			defer st.Close()
			out := cmd.OutOrStdout()
			if len(args) == 1 && args[0] != "-" {
				f, err := os.Create(args[0])
				if err != nil {
					return fmt.Errorf("open dump output: %w", err)
				}
				defer f.Close()
				out = f
			}
			return appconfig.Export(cmd.Context(), st, out)
		},
	})

	loadCmd := &cobra.Command{
		Use:   "load <path>",
		Short: "import an application-config TOML into the current store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			mode, _ := cmd.Flags().GetString("mode")
			var m appconfig.ImportMode
			switch mode {
			case "", "merge":
				m = appconfig.ImportMerge
			case "replace":
				m = appconfig.ImportReplace
			default:
				return fmt.Errorf("invalid --mode %q (want merge or replace)", mode)
			}
			st, err := openStore(cmd.Context(), cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return err
			}
			defer st.Close()
			f, err := os.Open(args[0])
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("app-config file not found: %s", args[0])
				}
				return fmt.Errorf("open app-config file: %w", err)
			}
			defer f.Close()
			if err := appconfig.Import(cmd.Context(), st, f, appconfig.ImportOptions{Mode: m}); err != nil {
				return err
			}
			writeLine(cmd.OutOrStdout(), g, "app-config loaded from "+args[0])
			return nil
		},
	}
	loadCmd.Flags().String("mode", "merge", "conflict resolution: merge | replace")
	c.AddCommand(loadCmd)
	return c
}
