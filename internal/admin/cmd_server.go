package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hanshuebner/herold/internal/sysconfig"

	toml "github.com/pelletier/go-toml/v2"
)

// DefaultPIDFile is the canonical PID location used by `server reload`
// to resolve the running pid when HEROLD_PID_FILE is unset.
const DefaultPIDFile = "/var/run/herold.pid"

func newServerCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "server",
		Short: "server lifecycle commands (start, reload, status, config-check)",
	}
	c.AddCommand(newServerStartCmd())
	c.AddCommand(newServerReloadCmd())
	c.AddCommand(newServerStatusCmd())
	c.AddCommand(newServerConfigCheckCmd())
	return c
}

func newServerStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "start the Herold server and run until SIGTERM/SIGINT",
		Long:  "Binds all configured listeners, opens the store, starts plugins and workers, and runs until the process receives SIGTERM or SIGINT.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			SetConfigPath(g.configPath)
			// Register our own signal handler so ctx cancels on SIGTERM
			// or SIGINT while SIGHUP is left for StartServer to reload.
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()
			// Write the PID file (best-effort).
			pidPath := pidFilePath()
			if pidPath != "" {
				if err := writePIDFile(pidPath); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "herold: warn: pid file %s: %v\n", pidPath, err)
				} else {
					defer os.Remove(pidPath)
				}
			}
			return StartServer(ctx, cfg, StartOpts{LogVerbose: g.logVerbose})
		},
	}
}

func newServerReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "send SIGHUP to the running server",
		Long:  "Resolves the PID of the running server (from $HEROLD_PID_FILE or /var/run/herold.pid) and sends SIGHUP. The server re-reads its config and applies live-updatable changes.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pidPath := pidFilePath()
			if pidPath == "" {
				return errors.New("reload: no PID file path (set $HEROLD_PID_FILE or place /var/run/herold.pid)")
			}
			raw, err := os.ReadFile(pidPath)
			if err != nil {
				return fmt.Errorf("reload: read PID file %s: %w", pidPath, err)
			}
			pidStr := strings.TrimSpace(string(raw))
			pid, err := strconv.Atoi(pidStr)
			if err != nil || pid <= 0 {
				return fmt.Errorf("reload: malformed PID file %s: %q", pidPath, pidStr)
			}
			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("reload: find process %d: %w", pid, err)
			}
			if err := proc.Signal(syscall.SIGHUP); err != nil {
				return fmt.Errorf("reload: send SIGHUP to %d: %w", pid, err)
			}
			g := globals(cmd.Context())
			writeLine(cmd.OutOrStdout(), g, fmt.Sprintf("SIGHUP sent to pid %d", pid))
			return nil
		},
	}
}

func newServerStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "print the running server's status (via admin REST)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/server/status", nil, &out); err != nil {
				return err
			}
			if g.jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "status: %v\n", out["status"])
			fmt.Fprintf(cmd.OutOrStdout(), "time:   %v\n", out["time"])
			fmt.Fprintf(cmd.OutOrStdout(), "ok:     %v\n", out["ok"])
			return nil
		},
	}
	return c
}

func newServerConfigCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config-check [path]",
		Short: "parse the system config without starting the server",
		Long:  "Parses the TOML, applies defaults, and validates cross-field invariants. Exits 0 on success, 2 on validation failure with an actionable message.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			path := g.configPath
			if len(args) == 1 {
				path = args[0]
			}
			if _, err := sysconfig.Load(path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return withExit(fmt.Errorf("config file not found at %s (override with --system-config or $HEROLD_SYSTEM_CONFIG)", path), ExitConfigInvalid)
				}
				return withExit(err, ExitConfigInvalid)
			}
			writeLine(cmd.OutOrStdout(), g, "config OK: "+path)
			return nil
		},
	}
}

// pidFilePath returns $HEROLD_PID_FILE or the system default.
func pidFilePath() string {
	if p := os.Getenv("HEROLD_PID_FILE"); p != "" {
		return p
	}
	return DefaultPIDFile
}

func writePIDFile(path string) error {
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

// clientFromGlobals builds an admin-client configured from the global
// flags, with a helpful error if neither a URL nor a credential file is
// available.
func clientFromGlobals(g *globalOptions) (*Client, error) {
	base := g.serverURL
	if base == "" {
		if bURL, ok := loadCredentialsServerURL(); ok {
			base = bURL
		}
	}
	if base == "" {
		return nil, errors.New("no admin REST URL (set --server-url or add server_url to ~/.herold/credentials.toml)")
	}
	return NewClient(ClientOptions{
		BaseURL: base,
		APIKey:  g.apiKey,
	})
}

func loadCredentialsServerURL() (string, bool) {
	p := DefaultCredentialsPath()
	if p == "" {
		return "", false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var f credentialsFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return "", false
	}
	return f.ServerURL, f.ServerURL != ""
}

// writeResult writes body to w in one of two formats:
//
//   - JSON (pretty-indented) when g.jsonOut is true OR when w is not a
//     terminal (output is being piped or redirected). Stable shape for
//     scripts.
//   - human-readable key/value lines when w is a terminal and --json
//     was not passed. Sorted keys, ISO8601 timestamps formatted as
//     local time, lists rendered as comma-joined values.
//
// Auto-detection on stdout means a script that does
// `herold principal show alice@... | jq` always sees JSON without
// needing --json on every invocation; a human running the same command
// at a terminal sees the readable form by default.
func writeResult(w io.Writer, g *globalOptions, body any) error {
	if g.jsonOut || !isTerminal(w) {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(body)
	}
	return writeHumanResult(w, body)
}

// isTerminal reports whether w writes to a terminal. Returns false when
// the assertion to *os.File fails (e.g. test buffers, pipes), which
// causes writeResult to default to JSON for non-terminal writers.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// writeHumanResult renders body in a key/value format. Maps render as
// aligned `key: value` lines with sorted keys; slices of maps render as
// numbered records. Leaf values are formatted by writeHumanValue.
func writeHumanResult(w io.Writer, body any) error {
	switch v := body.(type) {
	case map[string]any:
		return writeHumanMap(w, v, "")
	case []any:
		for i, item := range v {
			fmt.Fprintf(w, "[%d]\n", i+1)
			if m, ok := item.(map[string]any); ok {
				if err := writeHumanMap(w, m, "  "); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(w, "  %s\n", writeHumanValue(item))
			}
		}
		return nil
	default:
		_, err := fmt.Fprintln(w, writeHumanValue(body))
		return err
	}
}

// writeHumanMap writes a map as aligned key:value lines; nested maps
// render with deeper indent. Keys are sorted for deterministic output.
func writeHumanMap(w io.Writer, m map[string]any, indent string) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	maxKey := 0
	for _, k := range keys {
		if len(k) > maxKey {
			maxKey = len(k)
		}
	}
	for _, k := range keys {
		v := m[k]
		switch nested := v.(type) {
		case map[string]any:
			fmt.Fprintf(w, "%s%s:\n", indent, k)
			if err := writeHumanMap(w, nested, indent+"  "); err != nil {
				return err
			}
		case []any:
			if len(nested) == 0 {
				fmt.Fprintf(w, "%s%-*s  (none)\n", indent, maxKey, k)
				continue
			}
			fmt.Fprintf(w, "%s%-*s  %s\n", indent, maxKey, k, writeHumanValue(nested))
		default:
			fmt.Fprintf(w, "%s%-*s  %s\n", indent, maxKey, k, writeHumanValue(nested))
		}
	}
	return nil
}

// writeHumanValue formats a JSON-decoded scalar (or short list) for
// human display: bools as yes/no, RFC3339 timestamps as a local-time
// short form, lists as comma-joined values.
func writeHumanValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "(unset)"
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t.Local().Format("2006-01-02 15:04:05 MST")
		}
		if x == "" {
			return "(empty)"
		}
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, writeHumanValue(e))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", v)
	}
}
