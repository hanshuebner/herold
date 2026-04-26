package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/plugin"
)

// newPluginCmd assembles the `herold admin plugin` subtree. Phase 3
// Wave 3.5c ships the `test` subcommand for the directory.resolve_rcpt
// smoke test (REQ-DIR-RCPT-11); list / status are deferred to a later
// wave.
func newPluginCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "plugin",
		Short: "plugin lifecycle and smoke-test utilities",
	}
	c.AddCommand(newPluginTestCmd())
	return c
}

func newPluginTestCmd() *cobra.Command {
	var rcpt, mailFrom, sourceIP string
	c := &cobra.Command{
		Use:   "test <name>",
		Short: "drive a smoke-test against the named plugin (REQ-PLUG-53)",
		Long: `Spawns the named plugin, performs the initialize / configure handshake,
and exercises the type-specific smoke test:

  - directory plugin (with supports = ["resolve_rcpt"]) and --rcpt set:
    invokes directory.resolve_rcpt with a canned envelope and prints
    action / code / route_tag / latency (REQ-DIR-RCPT-11).

Other plugin types are not yet wired into this CLI; the long-form list
will land alongside the per-type smoke tests they require.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			var spec *plugin.Spec
			for _, p := range cfg.Plugin {
				if p.Name != name {
					continue
				}
				options := make(map[string]any, len(p.Options))
				for k, v := range p.Options {
					options[k] = v
				}
				spec = &plugin.Spec{
					Name:      p.Name,
					Path:      p.Path,
					Type:      plugin.PluginType(p.Type),
					Lifecycle: plugin.Lifecycle(p.Lifecycle),
					Options:   options,
				}
				break
			}
			if spec == nil {
				return fmt.Errorf("plugin test: %q not declared in any [[plugin]] block", name)
			}
			if rcpt == "" {
				return fmt.Errorf("plugin test: --rcpt <addr> required for the directory.resolve_rcpt smoke test (other plugin-type smoke tests not yet wired)")
			}
			if spec.Type != plugin.TypeDirectory {
				return fmt.Errorf("plugin test: --rcpt is only valid for type=\"directory\" plugins (%q is %q)", name, spec.Type)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
			mgr := plugin.NewManager(plugin.ManagerOptions{
				Logger:        logger,
				Clock:         clock.NewReal(),
				ServerVersion: "admin-plugin-test",
			})
			defer func() {
				ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel2()
				_ = mgr.Shutdown(ctx2)
			}()
			p, err := mgr.Start(ctx, *spec)
			if err != nil {
				return fmt.Errorf("plugin test: start %q: %w", name, err)
			}
			// Wait briefly for the plugin to reach StateHealthy.
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				if p.State() == plugin.StateHealthy {
					break
				}
				if p.State() == plugin.StateDisabled || p.State() == plugin.StateExited {
					return fmt.Errorf("plugin test: %q reached state %s before healthy", name, p.State())
				}
				time.Sleep(50 * time.Millisecond)
			}
			if p.State() != plugin.StateHealthy {
				return fmt.Errorf("plugin test: %q did not reach healthy state in 15s (current=%s)", name, p.State())
			}
			mf := p.Manifest()
			if mf == nil || !mf.HasSupport(plugin.SupportsResolveRcpt) {
				return fmt.Errorf("plugin test: %q does not declare supports: [\"resolve_rcpt\"]", name)
			}
			req := directory.ResolveRcptRequest{
				Recipient: rcpt,
				Envelope: directory.ResolveRcptEnvelope{
					MailFrom: emptyDefault(mailFrom, "smoke-test@admin.local"),
					SourceIP: emptyDefault(sourceIP, "127.0.0.1"),
					Listener: "inbound",
				},
				Context: directory.ResolveRcptContext{
					PluginName: name,
					RequestID:  "admin-cli",
				},
			}
			start := time.Now()
			resp, err := mgr.InvokeResolveRcpt(ctx, name, req)
			latency := time.Since(start)
			if err != nil {
				if errors.Is(err, directory.ErrResolveRcptTimeout) {
					return fmt.Errorf("plugin test: %q timed out after %s: %w", name, latency, err)
				}
				return fmt.Errorf("plugin test: %q failed after %s: %w", name, latency, err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "plugin:    %s\n", name)
			fmt.Fprintf(out, "method:    directory.resolve_rcpt\n")
			fmt.Fprintf(out, "action:    %s\n", strings.TrimSpace(resp.Action))
			if resp.Code != "" {
				fmt.Fprintf(out, "code:      %s\n", resp.Code)
			} else {
				fmt.Fprintln(out, "code:      -")
			}
			if resp.RouteTag != "" {
				fmt.Fprintf(out, "route_tag: %s\n", resp.RouteTag)
			}
			if resp.PrincipalID != nil {
				fmt.Fprintf(out, "principal: %d\n", *resp.PrincipalID)
			}
			if resp.Reason != "" {
				fmt.Fprintf(out, "reason:    %s\n", resp.Reason)
			}
			fmt.Fprintf(out, "latency:   %s\n", latency.Round(time.Millisecond))
			return nil
		},
	}
	c.Flags().StringVar(&rcpt, "rcpt", "", "recipient address to test (REQ-DIR-RCPT-11)")
	c.Flags().StringVar(&mailFrom, "mail-from", "", "MAIL FROM to put in the canned envelope (default smoke-test@admin.local)")
	c.Flags().StringVar(&sourceIP, "source-ip", "", "remote IP to put in the canned envelope (default 127.0.0.1)")
	return c
}

func emptyDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
