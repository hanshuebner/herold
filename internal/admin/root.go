package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// ExitCode classifies terminal errors for the CLI. os.Exit uses these
// values so scripts can branch on well-known conditions.
type ExitCode int

const (
	// ExitOK is the zero code returned on success.
	ExitOK ExitCode = 0
	// ExitUsage signals an argument / flag error (cobra default).
	ExitUsage ExitCode = 2
	// ExitConfigInvalid signals a config-check failure (REQ-OPS-06).
	ExitConfigInvalid ExitCode = 2
	// ExitBootstrapAlreadyDone is returned by `herold bootstrap` when at
	// least one principal already exists in the store.
	ExitBootstrapAlreadyDone ExitCode = 10
)

// exitCoded is the internal error type that carries a requested os.Exit
// code. Execute() unwraps it; other errors produce exit 1.
type exitCoded struct {
	err  error
	code ExitCode
}

func (e *exitCoded) Error() string { return e.err.Error() }
func (e *exitCoded) Unwrap() error { return e.err }

// withExit wraps err with an explicit exit code. Use ExitOK only with a
// nil underlying error.
func withExit(err error, code ExitCode) error {
	if err == nil {
		return nil
	}
	return &exitCoded{err: err, code: code}
}

// globalOptions holds the persistent flags parsed on the root command.
// Subcommands read through them via the command's Context().
type globalOptions struct {
	configPath string
	serverURL  string
	apiKey     string
	quiet      bool
	jsonOut    bool
}

type globalOptsKey struct{}

// withGlobals returns a derived context carrying the global options.
func withGlobals(ctx context.Context, g *globalOptions) context.Context {
	return context.WithValue(ctx, globalOptsKey{}, g)
}

// globals returns the global options attached to ctx, or a zero value.
func globals(ctx context.Context) *globalOptions {
	v, ok := ctx.Value(globalOptsKey{}).(*globalOptions)
	if !ok || v == nil {
		return &globalOptions{}
	}
	return v
}

// defaultConfigPath returns the config path after consulting the
// HEROLD_SYSTEM_CONFIG env var, falling back to the REQ-OPS-02 default.
func defaultConfigPath() string {
	if p := os.Getenv("HEROLD_SYSTEM_CONFIG"); p != "" {
		return p
	}
	return "/etc/herold/system.toml"
}

// NewRootCmd constructs the cobra command tree. The returned command can
// be Execute()'d directly by main; tests build their own and drive
// SetArgs/SetOut/SetErr.
func NewRootCmd() *cobra.Command {
	g := &globalOptions{}
	root := &cobra.Command{
		Use:           "herold",
		Short:         "Herold mail server",
		Long:          "herold is the single binary that runs the mail server and hosts the admin CLI.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SetContext(withGlobals(cmd.Context(), g))
			return nil
		},
	}
	root.PersistentFlags().StringVar(&g.configPath, "system-config", defaultConfigPath(),
		"path to system config TOML (also $HEROLD_SYSTEM_CONFIG)")
	root.PersistentFlags().StringVar(&g.serverURL, "server-url", "",
		"admin REST base URL (e.g. https://127.0.0.1:8080)")
	root.PersistentFlags().StringVar(&g.apiKey, "api-key", "",
		"admin REST API key (also $HEROLD_API_KEY or ~/.herold/credentials.toml)")
	root.PersistentFlags().BoolVar(&g.quiet, "quiet", false, "suppress non-error output")
	root.PersistentFlags().BoolVar(&g.jsonOut, "json", false, "machine-readable JSON output where supported")

	// Subcommand groups.
	root.AddCommand(newServerCmd())
	root.AddCommand(newBootstrapCmd())
	root.AddCommand(newPrincipalCmd())
	root.AddCommand(newDomainCmd())
	root.AddCommand(newOIDCCmd())
	root.AddCommand(newAPIKeyCmd())
	root.AddCommand(newAppConfigCmd())
	return root
}

// Execute runs the root cobra command against os.Args and honours the
// os.Exit code embedded in exitCoded errors.
func Execute(ctx context.Context) int {
	cmd := NewRootCmd()
	cmd.SetContext(ctx)
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "herold:", err.Error())
		var ec *exitCoded
		if errors.As(err, &ec) {
			return int(ec.code)
		}
		return 1
	}
	return 0
}

// requireConfig loads the system config referenced by --system-config. A
// missing file is turned into a clear operator-facing message.
func requireConfig(g *globalOptions) (*sysconfig.Config, error) {
	if g.configPath == "" {
		return nil, errors.New("no --system-config path (override with --system-config or $HEROLD_SYSTEM_CONFIG)")
	}
	cfg, err := sysconfig.Load(g.configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file not found at %s (override with --system-config or $HEROLD_SYSTEM_CONFIG)", g.configPath)
		}
		return nil, err
	}
	return cfg, nil
}

// writeLine writes ln + newline to w, skipping when quiet mode is on.
func writeLine(w io.Writer, g *globalOptions, ln string) {
	if g.quiet {
		return
	}
	fmt.Fprintln(w, ln)
}
