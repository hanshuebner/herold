package admin

// cmd_bugfetch.go — `herold bug-fetch`: pulls phone bug-report mails out
// of the maintainer's "Bug reports" label over JMAP and expands each one
// into a drop directory in the layout `herold bug-sink` writes
// (report.json, report.md, logs.txt, screenshot-N.png, private/, STATUS),
// so /bug-inbox processes a phone report exactly like a browser drop.
//
// The Android reporter (#407) sends the bundle as a mail to the user's own
// address with the files attached, either as one zip or as separate
// parts. This command authenticates with the maintainer's admin API key
// (a bearer key also authenticates JMAP), queries the label for unread
// messages, downloads the attachments through the blob endpoint, writes
// the drop, and sets $seen so the message is not fetched twice.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	// bugFetchMaxPartBytes caps one downloaded attachment and one
	// decompressed zip entry. A phone bundle is a few screenshots plus
	// text; anything larger is not a bug report.
	bugFetchMaxPartBytes = 50 << 20 // 50 MiB
	// bugFetchDefaultLimit bounds one run's Email/query page.
	bugFetchDefaultLimit = 50
	// bugFetchSubjectPrefix is what the phone reporter puts in front of
	// the report title.
	bugFetchSubjectPrefix = "herold bug:"

	jmapCapCore = "urn:ietf:params:jmap:core"
	jmapCapMail = "urn:ietf:params:jmap:mail"
)

// newBugFetchCmd returns the `herold bug-fetch` command.
func newBugFetchCmd() *cobra.Command {
	var (
		label  string
		out    string
		dryRun bool
		limit  int
	)
	c := &cobra.Command{
		Use:   "bug-fetch",
		Short: "fetch phone bug-report mails from a label over JMAP into drop directories",
		Long: "Queries the unread messages under --label in the account the API key " +
			"belongs to, downloads each message's attachments, and writes one drop " +
			"directory per message under --out in the layout `herold bug-sink` " +
			"produces (report.json, report.md, logs.txt, screenshot-N.png, private/, " +
			"STATUS=new). The message is marked read ($seen) once its drop is on disk.\n\n" +
			"The bundle may arrive as one zip attachment or as separate parts; both " +
			"expand to the same layout. A message without report.json gets one " +
			"synthesised from its subject and body so /bug-inbox can still file it.\n\n" +
			"The server URL and API key come from --server-url / --api-key, " +
			"$HEROLD_API_KEY, or ~/.herold/credentials.toml. --dry-run lists the " +
			"messages that would be fetched without downloading, writing, or marking.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			baseURL, apiKey, err := bugFetchCredentials(g)
			if err != nil {
				return err
			}
			client := &jmapFetchClient{
				base:   baseURL,
				apiKey: apiKey,
				http:   &http.Client{Timeout: 120 * time.Second},
			}
			return runBugFetch(cmd.Context(), cmd.OutOrStdout(), client, bugFetchOptions{
				Label:  label,
				OutDir: out,
				DryRun: dryRun,
				Limit:  limit,
			})
		},
	}
	c.Flags().StringVar(&label, "label", "Bug reports", "mailbox (label) holding the phone bug-report mails")
	c.Flags().StringVar(&out, "out", defaultBugSinkDir(), "directory to write drop directories into")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "list the unread messages that would be fetched; write and mark nothing")
	c.Flags().IntVar(&limit, "limit", bugFetchDefaultLimit, "maximum number of messages to fetch in one run")
	return c
}

// bugFetchCredentials resolves the JMAP origin and bearer key from the
// global flags, the environment, and the credentials file, in that
// order. JMAP is served on the same origin as the admin REST API, so the
// stored server_url is the right base.
func bugFetchCredentials(g *globalOptions) (baseURL, apiKey string, err error) {
	baseURL = g.serverURL
	if baseURL == "" {
		baseURL, _ = loadCredentialsServerURL()
	}
	if baseURL == "" {
		return "", "", errors.New("bug-fetch: no server URL (set --server-url or server_url in ~/.herold/credentials.toml)")
	}
	apiKey = g.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("HEROLD_API_KEY")
	}
	if apiKey == "" {
		apiKey, _ = loadCredentials()
	}
	if apiKey == "" {
		return "", "", errors.New("bug-fetch: no API key (set --api-key, $HEROLD_API_KEY, or api_key in ~/.herold/credentials.toml)")
	}
	return strings.TrimRight(baseURL, "/"), apiKey, nil
}

// bugFetchOptions carries the command's flags into runBugFetch.
type bugFetchOptions struct {
	Label  string
	OutDir string
	DryRun bool
	Limit  int
}

// runBugFetch is the command body, separated from flag parsing so tests
// drive it against a fake or in-process JMAP server.
func runBugFetch(ctx context.Context, w io.Writer, client *jmapFetchClient, opts bugFetchOptions) error {
	if opts.Limit <= 0 {
		opts.Limit = bugFetchDefaultLimit
	}
	sess, err := client.session(ctx)
	if err != nil {
		return err
	}

	mailboxID, err := client.mailboxIDByName(ctx, sess, opts.Label)
	if err != nil {
		return err
	}
	if mailboxID == "" {
		fmt.Fprintf(w, "bug-fetch: no mailbox named %q; nothing to fetch\n", opts.Label)
		return nil
	}

	ids, err := client.unreadEmailIDs(ctx, sess, mailboxID, opts.Limit)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Fprintf(w, "bug-fetch: no unread messages in %q\n", opts.Label)
		return nil
	}

	emails, err := client.getEmails(ctx, sess, ids)
	if err != nil {
		return err
	}

	if opts.DryRun {
		for _, e := range emails {
			names := make([]string, 0, len(e.Attachments))
			for _, a := range e.Attachments {
				names = append(names, a.displayName())
			}
			fmt.Fprintf(w, "bug-fetch: would fetch %s (%s) %q attachments=[%s]\n",
				e.ID, e.ReceivedAt, e.Subject, strings.Join(names, ", "))
		}
		fmt.Fprintf(w, "bug-fetch: dry run; %d message(s) listed, nothing written or marked\n", len(emails))
		return nil
	}

	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return fmt.Errorf("bug-fetch: create --out %s: %w", opts.OutDir, err)
	}

	var failed int
	for _, e := range emails {
		mail, err := client.materialise(ctx, sess, e)
		if err != nil {
			failed++
			fmt.Fprintf(w, "bug-fetch: %s: %v\n", e.ID, err)
			continue
		}
		drop := expandBugMail(mail)
		for _, n := range drop.Notes {
			fmt.Fprintf(w, "bug-fetch: %s: %s\n", e.ID, n)
		}
		dir := filepath.Join(opts.OutDir, bugDropID(e.ID))
		written, err := writeBugDrop(dir, drop)
		if err != nil {
			failed++
			fmt.Fprintf(w, "bug-fetch: %s: %v\n", e.ID, err)
			continue
		}
		if err := client.markSeen(ctx, sess, e.ID); err != nil {
			failed++
			fmt.Fprintf(w, "bug-fetch: %s: drop written to %s but marking read failed: %v\n", e.ID, dir, err)
			continue
		}
		if written {
			fmt.Fprintf(w, "bug-fetch: wrote %s\n", dir)
		} else {
			fmt.Fprintf(w, "bug-fetch: %s already present, marked read\n", dir)
		}
	}
	if failed > 0 {
		return fmt.Errorf("bug-fetch: %d of %d message(s) failed", failed, len(emails))
	}
	return nil
}

// ---- JMAP client ---------------------------------------------------------

// jmapFetchClient is the minimal JMAP client bug-fetch needs: session,
// method calls, and blob download, all bearer-authenticated.
type jmapFetchClient struct {
	base   string
	apiKey string
	http   *http.Client
}

// jmapFetchSession is the subset of the session descriptor the client
// uses.
type jmapFetchSession struct {
	APIURL      string
	DownloadURL string
	AccountID   string
}

// jmapFetchEmail is one Email/get result row with the properties
// bug-fetch asks for.
type jmapFetchEmail struct {
	ID          string          `json:"id"`
	Subject     string          `json:"subject"`
	ReceivedAt  string          `json:"receivedAt"`
	Attachments []jmapFetchPart `json:"attachments"`
	TextBody    []jmapFetchPart `json:"textBody"`
	BodyValues  map[string]struct {
		Value string `json:"value"`
	} `json:"bodyValues"`
}

// jmapFetchPart is the subset of EmailBodyPart bug-fetch reads.
type jmapFetchPart struct {
	PartID *string `json:"partId"`
	BlobID *string `json:"blobId"`
	Name   *string `json:"name"`
	Type   string  `json:"type"`
	Size   int64   `json:"size"`
}

func (p jmapFetchPart) displayName() string {
	if p.Name != nil && *p.Name != "" {
		return *p.Name
	}
	return "(unnamed " + p.Type + ")"
}

func (c *jmapFetchClient) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return c.http.Do(req)
}

// session fetches /.well-known/jmap and picks the mail account.
func (c *jmapFetchClient) session(ctx context.Context) (*jmapFetchSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/.well-known/jmap", nil)
	if err != nil {
		return nil, fmt.Errorf("bug-fetch: session request: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("bug-fetch: GET /.well-known/jmap: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bug-fetch: GET /.well-known/jmap: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var desc struct {
		PrimaryAccounts map[string]string `json:"primaryAccounts"`
		APIURL          string            `json:"apiUrl"`
		DownloadURL     string            `json:"downloadUrl"`
	}
	if err := json.Unmarshal(raw, &desc); err != nil {
		return nil, fmt.Errorf("bug-fetch: decode session descriptor: %w", err)
	}
	accountID := desc.PrimaryAccounts[jmapCapMail]
	if accountID == "" {
		return nil, errors.New("bug-fetch: session descriptor has no primary mail account")
	}
	if desc.APIURL == "" || desc.DownloadURL == "" {
		return nil, errors.New("bug-fetch: session descriptor lacks apiUrl or downloadUrl")
	}
	return &jmapFetchSession{
		APIURL:      c.resolve(desc.APIURL),
		DownloadURL: c.resolve(desc.DownloadURL),
		AccountID:   accountID,
	}, nil
}

// resolve turns a session URL into an absolute one against the base
// origin, so a descriptor carrying relative paths still works.
func (c *jmapFetchClient) resolve(u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return c.base + "/" + strings.TrimLeft(u, "/")
}

// call posts one method call and returns its response arguments. A
// response whose name is not the method (an "error" invocation) becomes
// a Go error carrying the JMAP error type.
func (c *jmapFetchClient) call(ctx context.Context, sess *jmapFetchSession, method string, args any) (json.RawMessage, error) {
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("bug-fetch: %s: encode args: %w", method, err)
	}
	envelope := map[string]any{
		"using":       []string{jmapCapCore, jmapCapMail},
		"methodCalls": []any{[]any{method, json.RawMessage(argsRaw), "c0"}},
	}
	body, _ := json.Marshal(envelope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sess.APIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bug-fetch: %s: request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("bug-fetch: %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bug-fetch: %s: status %d: %s", method, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bug-fetch: %s: decode response: %w", method, err)
	}
	if len(out.MethodResponses) != 1 || len(out.MethodResponses[0]) < 2 {
		return nil, fmt.Errorf("bug-fetch: %s: expected one method response, got %s", method, strings.TrimSpace(string(raw)))
	}
	var name string
	if err := json.Unmarshal(out.MethodResponses[0][0], &name); err != nil {
		return nil, fmt.Errorf("bug-fetch: %s: decode response name: %w", method, err)
	}
	if name != method {
		var jerr struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(out.MethodResponses[0][1], &jerr)
		return nil, fmt.Errorf("bug-fetch: %s: server error %q %s", method, jerr.Type, jerr.Description)
	}
	return out.MethodResponses[0][1], nil
}

// mailboxIDByName returns the id of the mailbox called name (case-
// insensitive, as Mailbox/query's name filter matches), or "" when the
// account has none. An absent label means the phone has not sent a
// report yet, so the caller treats it as "nothing to fetch".
func (c *jmapFetchClient) mailboxIDByName(ctx context.Context, sess *jmapFetchSession, name string) (string, error) {
	raw, err := c.call(ctx, sess, "Mailbox/query", map[string]any{
		"accountId": sess.AccountID,
		"filter":    map[string]any{"name": name},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("bug-fetch: Mailbox/query: decode: %w", err)
	}
	if len(out.IDs) == 0 {
		return "", nil
	}
	return out.IDs[0], nil
}

// unreadEmailIDs lists the unread messages in mailboxID, oldest first.
func (c *jmapFetchClient) unreadEmailIDs(ctx context.Context, sess *jmapFetchSession, mailboxID string, limit int) ([]string, error) {
	raw, err := c.call(ctx, sess, "Email/query", map[string]any{
		"accountId": sess.AccountID,
		"filter": map[string]any{
			"inMailbox":  mailboxID,
			"notKeyword": "$seen",
		},
		"sort":  []map[string]any{{"property": "receivedAt", "isAscending": true}},
		"limit": limit,
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bug-fetch: Email/query: decode: %w", err)
	}
	return out.IDs, nil
}

// getEmails fetches subject, receivedAt, attachments, and the text body
// for ids, preserving the query order.
func (c *jmapFetchClient) getEmails(ctx context.Context, sess *jmapFetchSession, ids []string) ([]jmapFetchEmail, error) {
	raw, err := c.call(ctx, sess, "Email/get", map[string]any{
		"accountId":           sess.AccountID,
		"ids":                 ids,
		"properties":          []string{"id", "subject", "receivedAt", "attachments", "textBody", "bodyValues"},
		"fetchTextBodyValues": true,
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		List []jmapFetchEmail `json:"list"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bug-fetch: Email/get: decode: %w", err)
	}
	byID := make(map[string]jmapFetchEmail, len(out.List))
	for _, e := range out.List {
		byID[e.ID] = e
	}
	ordered := make([]jmapFetchEmail, 0, len(ids))
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			ordered = append(ordered, e)
		}
	}
	return ordered, nil
}

// download fetches one blob through the session's downloadUrl template.
// {type} and {name} are single path segments, so a slash inside the
// content type is percent-encoded rather than left as a separator.
func (c *jmapFetchClient) download(ctx context.Context, sess *jmapFetchSession, blobID, ctype, name string) ([]byte, error) {
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	if name == "" {
		name = "part"
	}
	u := sess.DownloadURL
	u = strings.ReplaceAll(u, "{accountId}", url.PathEscape(sess.AccountID))
	u = strings.ReplaceAll(u, "{blobId}", url.PathEscape(blobID))
	u = strings.ReplaceAll(u, "{type}", url.PathEscape(ctype))
	u = strings.ReplaceAll(u, "{name}", url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("download %s: request: %w", name, err)
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("download %s: status %d: %s", name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, bugFetchMaxPartBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: read: %w", name, err)
	}
	if len(data) > bugFetchMaxPartBytes {
		return nil, fmt.Errorf("download %s: larger than %d bytes", name, bugFetchMaxPartBytes)
	}
	return data, nil
}

// materialise turns an Email/get row into a bugMail with every
// attachment's bytes downloaded.
func (c *jmapFetchClient) materialise(ctx context.Context, sess *jmapFetchSession, e jmapFetchEmail) (bugMail, error) {
	mail := bugMail{ID: e.ID, Subject: e.Subject, ReceivedAt: e.ReceivedAt}
	for _, p := range e.TextBody {
		if p.PartID == nil {
			continue
		}
		if v, ok := e.BodyValues[*p.PartID]; ok {
			mail.TextBody = v.Value
			break
		}
	}
	for _, p := range e.Attachments {
		if p.BlobID == nil || *p.BlobID == "" {
			continue
		}
		name := ""
		if p.Name != nil {
			name = *p.Name
		}
		data, err := c.download(ctx, sess, *p.BlobID, p.Type, name)
		if err != nil {
			return bugMail{}, err
		}
		mail.Attachments = append(mail.Attachments, bugMailPart{Name: name, Type: p.Type, Data: data})
	}
	return mail, nil
}

// markSeen sets $seen on emailID and fails when the server reports the
// update as not applied.
func (c *jmapFetchClient) markSeen(ctx context.Context, sess *jmapFetchSession, emailID string) error {
	raw, err := c.call(ctx, sess, "Email/set", map[string]any{
		"accountId": sess.AccountID,
		"update": map[string]any{
			emailID: map[string]any{"keywords/$seen": true},
		},
	})
	if err != nil {
		return err
	}
	var out struct {
		Updated    map[string]json.RawMessage `json:"updated"`
		NotUpdated map[string]json.RawMessage `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("bug-fetch: Email/set: decode: %w", err)
	}
	if _, ok := out.Updated[emailID]; ok {
		return nil
	}
	if reason, ok := out.NotUpdated[emailID]; ok {
		return fmt.Errorf("bug-fetch: Email/set: notUpdated: %s", string(reason))
	}
	return errors.New("bug-fetch: Email/set: server neither updated nor rejected the message")
}

// ---- bundle expansion ----------------------------------------------------

// bugMail is a downloaded bug-report mail: the envelope fields the
// expansion uses plus every attachment's bytes.
type bugMail struct {
	ID          string
	Subject     string
	ReceivedAt  string
	TextBody    string
	Attachments []bugMailPart
}

// bugMailPart is one attachment: its filename, content type, and bytes.
type bugMailPart struct {
	Name string
	Type string
	Data []byte
}

// bugDrop is an expanded drop: file contents keyed by drop-relative path
// ("report.json", "screenshot-1.png", "private/private.json"), plus notes
// about parts that were skipped or synthesised.
type bugDrop struct {
	Files map[string][]byte
	Notes []string
}

var bugScreenshotName = regexp.MustCompile(`^screenshot-[0-9]+\.png$`)

// expandBugMail maps a bug-report mail onto the drop layout. Each
// attachment is placed by name: report.json, report.md, logs.txt, and
// screenshot-N.png keep their names; any other PNG becomes the next free
// screenshot-N.png; private.json and anything under a private/ directory
// go to private/; a zip attachment is unpacked and its entries placed by
// the same rules. A missing report.md comes from the mail body, a missing
// logs.txt is written empty, and a missing report.json is synthesised
// from the subject and body so the drop still carries the meta.kind
// /bug-inbox reads. Only file basenames are ever used, so an archive
// entry cannot escape the drop directory.
func expandBugMail(mail bugMail) bugDrop {
	drop := bugDrop{Files: map[string][]byte{}}
	for _, att := range mail.Attachments {
		if isZipPart(att) {
			entries, err := unzipBugBundle(att.Data)
			if err != nil {
				drop.Notes = append(drop.Notes, fmt.Sprintf("skipped zip attachment %q: %v", att.Name, err))
				continue
			}
			for _, entry := range entries {
				drop.place(entry.Name, entry.Type, entry.Data)
			}
			continue
		}
		drop.place(att.Name, att.Type, att.Data)
	}

	if _, ok := drop.Files["report.md"]; !ok {
		body := strings.TrimSpace(mail.TextBody)
		if body == "" {
			body = "# " + bugTitleFromSubject(mail.Subject) + "\n"
		}
		drop.Files["report.md"] = []byte(strings.TrimRight(body, "\n") + "\n")
	}
	if _, ok := drop.Files["logs.txt"]; !ok {
		drop.Files["logs.txt"] = []byte{}
	}
	if _, ok := drop.Files["report.json"]; !ok {
		drop.Files["report.json"] = synthesiseBugReportJSON(mail, drop.screenshotCount())
		drop.Notes = append(drop.Notes, "no report.json attached; synthesised one from the subject and body")
	}
	return drop
}

// place files one part by name according to the layout rules.
func (d *bugDrop) place(name, ctype string, data []byte) {
	clean := path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	base := path.Base(clean)
	dir := path.Dir(clean)
	inPrivate := base == "private.json" || strings.Contains("/"+strings.Trim(dir, "/")+"/", "/private/")

	switch {
	case inPrivate:
		if base == "" || base == "." || base == "/" {
			base = "private.json"
		}
		d.Files["private/"+base] = data
	case base == "report.json", base == "report.md", base == "logs.txt":
		d.Files[base] = data
	case bugScreenshotName.MatchString(base):
		d.Files[base] = data
	case strings.EqualFold(ctype, "image/png") || strings.HasSuffix(strings.ToLower(base), ".png"):
		d.Files[fmt.Sprintf("screenshot-%d.png", d.screenshotCount()+1)] = data
	default:
		d.Notes = append(d.Notes, fmt.Sprintf("ignored part %q (%s)", name, ctype))
	}
}

// screenshotCount counts the screenshot-N.png files placed so far.
func (d *bugDrop) screenshotCount() int {
	n := 0
	for p := range d.Files {
		if bugScreenshotName.MatchString(p) {
			n++
		}
	}
	return n
}

// isZipPart reports whether the attachment is a zip archive, by content
// type or filename.
func isZipPart(p bugMailPart) bool {
	ct := strings.ToLower(p.Type)
	if ct == "application/zip" || ct == "application/x-zip-compressed" {
		return true
	}
	return strings.HasSuffix(strings.ToLower(p.Name), ".zip")
}

// unzipBugBundle reads every regular file out of a zip archive, capping
// each decompressed entry at bugFetchMaxPartBytes.
func unzipBugBundle(data []byte) ([]bugMailPart, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var out []bugMailPart
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", f.Name, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, bugFetchMaxPartBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		if len(content) > bugFetchMaxPartBytes {
			return nil, fmt.Errorf("entry %s larger than %d bytes", f.Name, bugFetchMaxPartBytes)
		}
		out = append(out, bugMailPart{Name: f.Name, Data: content})
	}
	return out, nil
}

// bugTitleFromSubject strips the reporter's subject prefix.
func bugTitleFromSubject(subject string) string {
	s := strings.TrimSpace(subject)
	if len(s) >= len(bugFetchSubjectPrefix) && strings.EqualFold(s[:len(bugFetchSubjectPrefix)], bugFetchSubjectPrefix) {
		s = strings.TrimSpace(s[len(bugFetchSubjectPrefix):])
	}
	if s == "" {
		return "(no subject)"
	}
	return s
}

// synthesiseBugReportJSON builds a minimal report.json for a mail that
// carried none, in the browser bundle's public-meta shape.
func synthesiseBugReportJSON(mail bugMail, screenshots int) []byte {
	sketch := bugTitleFromSubject(mail.Subject)
	if body := strings.TrimSpace(mail.TextBody); body != "" {
		sketch += "\n\n" + body
	}
	meta := map[string]any{
		"protocol":        "herold-bug-mail/1",
		"createdAt":       mail.ReceivedAt,
		"kind":            "bug",
		"sketch":          sketch,
		"app":             map[string]string{"id": "herold-android", "name": "Herold Android"},
		"screenshotCount": screenshots,
		"source":          map[string]string{"emailId": mail.ID, "subject": mail.Subject},
	}
	raw, _ := json.MarshalIndent(meta, "", "  ")
	return append(raw, '\n')
}

// bugDropID turns a JMAP Email id into a directory name: letters,
// digits, '.', '_', and '-' pass through, everything else becomes '_'.
func bugDropID(emailID string) string {
	var b strings.Builder
	for _, r := range emailID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	id := strings.Trim(b.String(), ".")
	if id == "" {
		id = "mail"
	}
	return "mail-" + id
}

// writeBugDrop writes drop under dir (0700, files 0600, private/ 0700,
// STATUS=new). An existing dir is left untouched and reported as not
// written, so a re-run after a failed $seen update does not clobber a
// drop /bug-inbox may already have filed.
func writeBugDrop(dir string, drop bugDrop) (written bool, err error) {
	if _, statErr := os.Stat(dir); statErr == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "private"), 0o700); err != nil {
		return false, fmt.Errorf("create drop dir: %w", err)
	}
	paths := make([]string, 0, len(drop.Files))
	for p := range drop.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		target := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.WriteFile(target, drop.Files[p], 0o600); err != nil {
			return false, fmt.Errorf("write %s: %w", p, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "STATUS"), []byte("new"), 0o600); err != nil {
		return false, fmt.Errorf("write STATUS: %w", err)
	}
	return true, nil
}
