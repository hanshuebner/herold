package admin

// cmd_bugfetch.go — `herold bug-fetch`: pulls bug-report bundles off the
// server's POST /api/v1/bug-reports queue (issue #416) and expands each
// one into a drop directory in the layout `herold bug-sink` writes
// (report.json, report.md, logs.txt, screenshot-N.png, optional
// private.json, meta.json, STATUS), so /bug-inbox processes a phone
// report exactly like a browser drop.
//
// Authentication is a bug-reports-scoped API key
// (`herold api-key create --scope bug-reports`), read from
// ~/.herold/bug-reports.toml (server_url, api_key) or from
// $HEROLD_BUG_REPORTS_KEY plus --server-url. This is a separate
// credential from the admin credentials.toml the rest of the CLI uses:
// the maintainer's Mac carries a key that can only list, download, and
// delete bug reports, nothing else on the server (REQ-AUTH-SCOPE-04).

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

const (
	// bugFetchAPIKeyEnv is the environment variable carrying the
	// bug-reports-scoped API key, overriding bug-reports.toml's api_key.
	bugFetchAPIKeyEnv = "HEROLD_BUG_REPORTS_KEY"
	// bugFetchDefaultLimit bounds one run's report count.
	bugFetchDefaultLimit = 50
)

// newBugFetchCmd returns the `herold bug-fetch` command.
func newBugFetchCmd() *cobra.Command {
	var (
		out    string
		dryRun bool
		keep   bool
		limit  int
	)
	c := &cobra.Command{
		Use:   "bug-fetch",
		Short: "fetch bug reports from POST /api/v1/bug-reports into drop directories",
		Long: "Lists the reports queued via POST /api/v1/bug-reports (the Android in-app " +
			"reporter, issue #407), downloads each one, and writes it as a drop directory " +
			"under --out in the layout `herold bug-sink` produces (report.json, report.md, " +
			"logs.txt, screenshot-N.png, optional private.json, meta.json, STATUS=new), then " +
			"deletes it on the server so it is not fetched twice -- unless --keep is given.\n\n" +
			"Credentials come from ~/.herold/bug-reports.toml (server_url, api_key), or from " +
			"$HEROLD_BUG_REPORTS_KEY plus --server-url. This is a bug-reports-scoped API key " +
			"(`herold api-key create --scope bug-reports`), not the admin credentials.toml key.\n\n" +
			"--dry-run lists the reports that would be fetched without downloading, writing, " +
			"or deleting anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			baseURL, apiKey, err := bugFetchCredentials(g.serverURL)
			if err != nil {
				return err
			}
			client := &bugReportsClient{
				base:   baseURL,
				apiKey: apiKey,
				http:   &http.Client{Timeout: 120 * time.Second},
			}
			return runBugFetch(cmd.Context(), cmd.OutOrStdout(), client, bugFetchOptions{
				OutDir: out,
				DryRun: dryRun,
				Keep:   keep,
				Limit:  limit,
			})
		},
	}
	c.Flags().StringVar(&out, "out", defaultBugSinkDir(), "directory to write drop directories into")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "list the reports that would be fetched; write, download, and delete nothing")
	c.Flags().BoolVar(&keep, "keep", false, "do not delete a report on the server after writing its drop")
	c.Flags().IntVar(&limit, "limit", bugFetchDefaultLimit, "maximum number of reports to fetch in one run")
	return c
}

// bugReportsCredentialsFile is ~/.herold/bug-reports.toml.
type bugReportsCredentialsFile struct {
	ServerURL string `toml:"server_url"`
	APIKey    string `toml:"api_key"`
}

// bugReportsCredentialsPathOverride is a test seam: SetBugReportsCredentialsPath
// redirects defaultBugReportsCredentialsPath away from the real
// $HOME/.herold/bug-reports.toml so tests never touch the developer's
// actual home directory.
var bugReportsCredentialsPathOverride atomic.Pointer[string]

// SetBugReportsCredentialsPath overrides the location bug-fetch reads
// bug-reports.toml from. Pass an empty string to revert to the default.
// Test seam; not for production callers.
func SetBugReportsCredentialsPath(p string) {
	if p == "" {
		bugReportsCredentialsPathOverride.Store(nil)
		return
	}
	bugReportsCredentialsPathOverride.Store(&p)
}

// defaultBugReportsCredentialsPath resolves ~/.herold/bug-reports.toml.
func defaultBugReportsCredentialsPath() string {
	if ptr := bugReportsCredentialsPathOverride.Load(); ptr != nil && *ptr != "" {
		return *ptr
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".herold", "bug-reports.toml")
}

// loadBugReportsCredentials reads ~/.herold/bug-reports.toml, returning
// the zero value when the file is absent or malformed (the caller falls
// back to the flag / env var).
func loadBugReportsCredentials() bugReportsCredentialsFile {
	p := defaultBugReportsCredentialsPath()
	if p == "" {
		return bugReportsCredentialsFile{}
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return bugReportsCredentialsFile{}
	}
	var f bugReportsCredentialsFile
	_ = toml.Unmarshal(raw, &f)
	return f
}

// bugFetchCredentials resolves the server origin and bearer key:
// --server-url (serverURLFlag, the root command's shared --server-url
// persistent flag) overrides bug-reports.toml's server_url;
// $HEROLD_BUG_REPORTS_KEY overrides bug-reports.toml's api_key.
func bugFetchCredentials(serverURLFlag string) (baseURL, apiKey string, err error) {
	file := loadBugReportsCredentials()
	baseURL = serverURLFlag
	if baseURL == "" {
		baseURL = file.ServerURL
	}
	if baseURL == "" {
		return "", "", errors.New("bug-fetch: no server URL (set --server-url or server_url in ~/.herold/bug-reports.toml)")
	}
	apiKey = os.Getenv(bugFetchAPIKeyEnv)
	if apiKey == "" {
		apiKey = file.APIKey
	}
	if apiKey == "" {
		return "", "", fmt.Errorf("bug-fetch: no API key (set $%s or api_key in ~/.herold/bug-reports.toml)", bugFetchAPIKeyEnv)
	}
	return strings.TrimRight(baseURL, "/"), apiKey, nil
}

// bugFetchOptions carries the command's flags into runBugFetch.
type bugFetchOptions struct {
	OutDir string
	DryRun bool
	Keep   bool
	Limit  int
}

// bugReportSummary is one row of GET /api/v1/bug-reports.
type bugReportSummary struct {
	ID                 string `json:"id"`
	ReceivedAt         string `json:"received_at"`
	Email              string `json:"email"`
	Title              string `json:"title"`
	Route              string `json:"route"`
	DescriptionEntered bool   `json:"description_entered"`
	ScreenshotCount    int    `json:"screenshot_count"`
}

// bugFetchServer is the list/download/delete surface runBugFetch needs.
// *bugReportsClient is the production implementation; tests substitute a
// fake to exercise runBugFetch's control flow without a network round
// trip.
type bugFetchServer interface {
	list(ctx context.Context) ([]bugReportSummary, error)
	download(ctx context.Context, id string) ([]byte, error)
	delete(ctx context.Context, id string) error
}

var _ bugFetchServer = (*bugReportsClient)(nil)

// runBugFetch is the command body, separated from flag parsing so tests
// drive it against a fake or in-process server.
func runBugFetch(ctx context.Context, w io.Writer, client bugFetchServer, opts bugFetchOptions) error {
	if opts.Limit <= 0 {
		opts.Limit = bugFetchDefaultLimit
	}
	items, err := client.list(ctx)
	if err != nil {
		return fmt.Errorf("bug-fetch: %w", err)
	}
	if len(items) > opts.Limit {
		items = items[:opts.Limit]
	}
	if len(items) == 0 {
		fmt.Fprintln(w, "no reports")
		return nil
	}

	if opts.DryRun {
		for _, it := range items {
			fmt.Fprintf(w, "bug-fetch: would fetch %s (%s) %q screenshots=%d\n",
				it.ID, it.ReceivedAt, it.Title, it.ScreenshotCount)
		}
		fmt.Fprintf(w, "bug-fetch: dry run; %d report(s) listed, nothing written or deleted\n", len(items))
		return nil
	}

	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return fmt.Errorf("bug-fetch: create --out %s: %w", opts.OutDir, err)
	}

	var failed int
	for _, it := range items {
		zipData, err := client.download(ctx, it.ID)
		if err != nil {
			failed++
			fmt.Fprintf(w, "bug-fetch: %s: %v\n", it.ID, err)
			continue
		}
		dir := filepath.Join(opts.OutDir, it.ID)
		if err := writeBugReportDrop(dir, zipData); err != nil {
			failed++
			fmt.Fprintf(w, "bug-fetch: %s: %v\n", it.ID, err)
			continue
		}
		if !opts.Keep {
			if err := client.delete(ctx, it.ID); err != nil {
				failed++
				fmt.Fprintf(w, "bug-fetch: %s: drop written to %s but delete failed: %v\n", it.ID, dir, err)
				continue
			}
		}
		fmt.Fprintf(w, "bug-fetch: wrote %s\n", dir)
	}
	if failed > 0 {
		return fmt.Errorf("bug-fetch: %d of %d report(s) failed", failed, len(items))
	}
	return nil
}

// writeBugReportDrop extracts zipData's entries into dir (0700), each
// file 0600, and stamps STATUS=new. Only the entry's basename is ever
// used as the target filename, so a maliciously-crafted archive entry
// cannot escape dir.
func writeBugReportDrop(dir string, zipData []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return fmt.Errorf("open response as zip: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create drop dir: %w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Base(f.Name)
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", f.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "STATUS"), []byte("new"), 0o600); err != nil {
		return fmt.Errorf("write STATUS: %w", err)
	}
	return nil
}

// ---- bug-reports REST client ---------------------------------------------

// bugReportsClient is the minimal HTTP client bug-fetch needs against
// POST/GET/DELETE /api/v1/bug-reports, bearer-authenticated with the
// bug-reports-scoped key.
type bugReportsClient struct {
	base   string
	apiKey string
	http   *http.Client
}

func (c *bugReportsClient) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return c.http.Do(req)
}

// list returns every queued report, newest first (as the server orders
// them).
func (c *bugReportsClient) list(ctx context.Context) ([]bugReportSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/bug-reports", nil)
	if err != nil {
		return nil, fmt.Errorf("list: request: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Items []bugReportSummary `json:"items"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("list: decode response: %w", err)
	}
	return out.Items, nil
}

// download fetches id's drop as a zip archive.
func (c *bugReportsClient) download(ctx context.Context, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/bug-reports/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("download: request: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("download: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("download: read: %w", err)
	}
	return data, nil
}

// delete removes id on the server.
func (c *bugReportsClient) delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/api/v1/bug-reports/"+id, nil)
	if err != nil {
		return fmt.Errorf("delete: request: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("delete: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}
