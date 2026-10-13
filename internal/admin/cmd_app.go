package admin

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/cliout"
)

// herold app ... mirrors the /api/v1/apps admin REST surface
// (REQ-APP-10).

func newAppCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "app",
		Short: "registered app management (thread actions offered by external apps)",
	}
	c.AddCommand(newAppListCmd())
	c.AddCommand(newAppAddCmd())
	c.AddCommand(newAppShowCmd())
	c.AddCommand(newAppSetCmd())
	c.AddCommand(newAppApproveCmd())
	c.AddCommand(newAppPostCmd("refresh", "refresh", "re-fetch the app manifest; a changed manifest awaits approval"))
	c.AddCommand(newAppPostCmd("rotate-secret", "rotate-secret", "generate a new signing secret; it is printed once"))
	c.AddCommand(newAppPostCmd("disable", "disable", "stop offering the app's actions"))
	c.AddCommand(newAppPostCmd("enable", "enable", "offer a disabled app's actions again"))
	c.AddCommand(newAppRemoveCmd())
	return c
}

// runApp performs one apps request and renders the result.
func runApp(cmd *cobra.Command, method, path string, body any, human func(io.Writer, map[string]any) error) error {
	g := globals(cmd.Context())
	client, err := clientFromGlobals(g)
	if err != nil {
		return err
	}
	var out map[string]any
	if err := client.do(cmd.Context(), method, path, body, &out); err != nil {
		return err
	}
	if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
		return writeResult(cmd.OutOrStdout(), g, out)
	}
	return human(cmd.OutOrStdout(), out)
}

func newAppListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list registered apps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runApp(cmd, "GET", "/api/v1/apps", nil, writeAppListHuman)
		},
	}
}

func newAppAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <origin>",
		Short: "register an app by origin; fetches its manifest and prints the signing secret once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApp(cmd, "POST", "/api/v1/apps", map[string]any{"origin": args[0]}, writeAppHuman)
		},
	}
}

func newAppShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "show one app, its manifests and its principals",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApp(cmd, "GET", "/api/v1/apps/"+args[0], nil, writeAppHuman)
		},
	}
}

func newAppSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <id>",
		Short: "set which principals the app is offered to",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("all-principals") {
				v, _ := cmd.Flags().GetBool("all-principals")
				body["all_principals"] = v
			}
			clearSet, _ := cmd.Flags().GetBool("no-principals")
			refs, _ := cmd.Flags().GetStringSlice("principal")
			if clearSet && len(refs) > 0 {
				return errors.New("app set: --principal and --no-principals are mutually exclusive")
			}
			if clearSet || len(refs) > 0 {
				list := make([]any, 0, len(refs))
				for _, r := range refs {
					if n, err := strconv.ParseUint(r, 10, 64); err == nil {
						list = append(list, n)
					} else {
						list = append(list, r)
					}
				}
				body["principals"] = list
			}
			if len(body) == 0 {
				return errors.New("app set: at least one of --all-principals|--principal|--no-principals is required")
			}
			return runApp(cmd, "PATCH", "/api/v1/apps/"+args[0], body, writeAppHuman)
		},
	}
	cmd.Flags().Bool("all-principals", false, "offer the app to every principal")
	cmd.Flags().StringSlice("principal", nil, "principal email or id to enable; repeatable, replaces the current set")
	cmd.Flags().Bool("no-principals", false, "clear the explicit principal set")
	return cmd
}

func newAppApproveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "approve the pending manifest whose digest you reviewed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			digest, _ := cmd.Flags().GetString("digest")
			if digest == "" {
				return errors.New("app approve: --digest (the pending_manifest_digest you reviewed) is required")
			}
			return runApp(cmd, "POST", "/api/v1/apps/"+args[0]+"/approve",
				map[string]any{"manifest_digest": digest}, writeAppHuman)
		},
	}
	cmd.Flags().String("digest", "", "pending_manifest_digest shown by `herold app show`")
	return cmd
}

func newAppPostCmd(use, action, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApp(cmd, "POST", "/api/v1/apps/"+args[0]+"/"+action, nil, writeAppHuman)
		},
	}
}

func newAppRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "remove a registered app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			if err := client.do(cmd.Context(), "DELETE", "/api/v1/apps/"+args[0], nil, nil); err != nil {
				return err
			}
			writeLine(cmd.OutOrStdout(), g, "app removed: "+args[0])
			return nil
		},
	}
}

// localizedEN returns the en entry of a localized map field.
func localizedEN(m map[string]any, key string) string {
	if v, ok := m[key].(map[string]any); ok {
		if s, ok := v["en"].(string); ok {
			return s
		}
	}
	return ""
}

func appPrincipalsText(out map[string]any) string {
	if b, _ := out["all_principals"].(bool); b {
		return "all"
	}
	list, _ := out["principals"].([]any)
	names := make([]string, 0, len(list))
	for _, p := range list {
		if m, ok := p.(map[string]any); ok {
			if e, _ := m["email"].(string); e != "" {
				names = append(names, e)
			} else {
				names = append(names, fval(m, "id"))
			}
		}
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

func writeAppListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no apps)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "APP-KEY", "ORIGIN", "STATE", "PENDING", "PRINCIPALS", "LAST-INVOKED")
	for _, it := range items {
		pending := ""
		if sval(it, "pending_manifest_digest") != "" {
			pending = "update"
		}
		t.Row(fval(it, "id"), sval(it, "app_key"), sval(it, "origin"), sval(it, "state"),
			pending, appPrincipalsText(it), tval(it, "last_invoked_at"))
	}
	return t.Flush()
}

func writeAppManifestHuman(w io.Writer, label string, m any) {
	man, ok := m.(map[string]any)
	if !ok {
		return
	}
	fmt.Fprintf(w, "\n%s:\n", label)
	exts, _ := man["extensions"].([]any)
	for _, e := range exts {
		ext, ok := e.(map[string]any)
		if !ok {
			continue
		}
		scopes, _ := ext["scopes"].([]any)
		sc := make([]string, 0, len(scopes))
		for _, s := range scopes {
			sc = append(sc, fmt.Sprint(s))
		}
		fmt.Fprintf(w, "  %s  %s  %q  icon=%s  scopes=[%s]  endpoint=%s\n",
			sval(ext, "point"), sval(ext, "id"), localizedEN(ext, "label"), sval(ext, "icon"),
			strings.Join(sc, ","), sval(ext, "endpoint"))
	}
}

func writeAppHuman(w io.Writer, out map[string]any) error {
	cliout.KV(w, [][2]string{
		{"id", fval(out, "id")},
		{"app_key", sval(out, "app_key")},
		{"name", localizedEN(out, "name")},
		{"origin", sval(out, "origin")},
		{"state", sval(out, "state")},
		{"principals", appPrincipalsText(out)},
		{"manifest_digest", sval(out, "manifest_digest")},
		{"pending_manifest_digest", sval(out, "pending_manifest_digest")},
		{"last_invoked_at", tval(out, "last_invoked_at")},
		{"created_at", tval(out, "created_at")},
		{"updated_at", tval(out, "updated_at")},
	})
	writeAppManifestHuman(w, "approved manifest", out["manifest"])
	writeAppManifestHuman(w, "pending manifest", out["pending_manifest"])
	if warnings, _ := out["warnings"].([]any); len(warnings) > 0 {
		fmt.Fprintln(w, "\nwarnings:")
		for _, x := range warnings {
			fmt.Fprintf(w, "  %v\n", x)
		}
	}
	if secret := sval(out, "secret"); secret != "" {
		fmt.Fprintf(w, "\nsecret: %s  (shown once; configure it in the app)\n", secret)
	}
	return nil
}
