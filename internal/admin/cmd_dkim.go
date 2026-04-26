package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// newDKIMCmd registers the `herold dkim ...` admin sub-command tree.
// Phase 3 Wave 3.4-DKIM ships generate and show; the REST surface
// is POST/GET /api/v1/domains/{name}/dkim (REQ-ADM-11, REQ-OPS-60).
func newDKIMCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "dkim",
		Short: "DKIM key management (generate, show)",
	}
	c.AddCommand(newDKIMGenerateCmd())
	c.AddCommand(newDKIMShowCmd())
	return c
}

// newDKIMGenerateCmd returns the `herold dkim generate <domain>` command.
// It POSTs to /api/v1/domains/<domain>/dkim (rotate-or-generate semantics)
// and prints the new selector, algorithm, and DNS TXT body. The TXT body is
// ready to paste into a zone file when no DNS plugin is configured.
func newDKIMGenerateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "generate <domain>",
		Short: "generate (or rotate) the DKIM signing key for a domain",
		Long: `Generate a fresh DKIM signing key for <domain>.

If an active key already exists it is transitioned to retiring and a new key is
installed as active (rotate semantics per REQ-OPS-62). When no key exists the
first key is generated (REQ-OPS-60).

The DNS TXT record body printed under "txt_record" must be published at:
  <selector>._domainkey.<domain>

Copy-paste it into your DNS zone file, or let the DNS plugin publish it
automatically if one is configured.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			domain := strings.ToLower(strings.TrimSpace(args[0]))
			var out map[string]any
			if err := client.do(cmd.Context(), "POST",
				"/api/v1/domains/"+domain+"/dkim", nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeDKIMKeyHuman(cmd.OutOrStdout(), g, out)
		},
	}
}

// newDKIMShowCmd returns the `herold dkim show <domain>` command.
// It GETs /api/v1/domains/<domain>/dkim and prints a tabular view of all
// keys: selector, algorithm, active status, and the DNS TXT body.
func newDKIMShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <domain>",
		Short: "show DKIM keys for a domain (selector, algorithm, TXT record)",
		Long: `List all DKIM keys for <domain> in tabular form.

For each key the following columns are printed:
  selector   — the DKIM s= tag value used in DKIM-Signature headers
  algo       — signing algorithm (ed25519-sha256 or rsa-sha256)
  status     — active | retiring | retired
  txt_record — the DNS TXT body (v=DKIM1; k=...; p=...) to publish at
               <selector>._domainkey.<domain>

REQ-ADM-310: the TXT body is the operator-visible artefact for DNS setup.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			domain := strings.ToLower(strings.TrimSpace(args[0]))
			var out map[string]any
			if err := client.do(cmd.Context(), "GET",
				"/api/v1/domains/"+domain+"/dkim", nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			if g.jsonOut {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeDKIMListHuman(cmd.OutOrStdout(), g, out)
		},
	}
}

// writeDKIMKeyHuman prints a single DKIM key entry (POST response) as
// human-readable text. When quiet mode is on only the TXT record body is
// printed so scripts can capture it directly.
func writeDKIMKeyHuman(w io.Writer, g *globalOptions, out map[string]any) error {
	if g.quiet {
		if txt, _ := out["txt_record"].(string); txt != "" {
			fmt.Fprintln(w, txt)
		}
		return nil
	}
	fmt.Fprintf(w, "selector:   %s\n", strVal(out, "selector"))
	fmt.Fprintf(w, "algorithm:  %s\n", strVal(out, "algorithm"))
	fmt.Fprintf(w, "active:     %v\n", out["is_active"])
	fmt.Fprintf(w, "created_at: %s\n", strVal(out, "created_at"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "DNS TXT record (publish at <selector>._domainkey.<domain>):")
	fmt.Fprintln(w, strVal(out, "txt_record"))
	return nil
}

// writeDKIMListHuman prints the DKIM key list (GET response) as a tabular
// view with the TXT body on continuation lines indented under each row.
func writeDKIMListHuman(w io.Writer, g *globalOptions, out map[string]any) error {
	if g.quiet {
		return nil
	}
	// Extract the items array.
	raw, _ := json.Marshal(out)
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("dkim show: decode response: %w", err)
	}
	if len(page.Items) == 0 {
		fmt.Fprintln(w, "no DKIM keys found for this domain")
		return nil
	}
	for _, item := range page.Items {
		status := "retiring"
		if active, _ := item["is_active"].(bool); active {
			status = "active"
		}
		fmt.Fprintf(w, "selector: %-30s  algo: %-15s  status: %s\n",
			strVal(item, "selector"),
			strVal(item, "algorithm"),
			status)
		txt := strVal(item, "txt_record")
		if txt != "" {
			fmt.Fprintf(w, "  txt: %s\n", txt)
		}
	}
	return nil
}

// strVal reads a string value from a map, returning "" for missing / non-string.
func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
