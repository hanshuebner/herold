package admin

// fakeclassify_llminspect_e2e_test.go is the acceptance test for issue
// #364: a mail delivered with "+promo" in the subject through the
// composed server (admin.StartServer, a real SMTP listener, a real
// out-of-process classifier plugin child process -- STANDARDS section 8,
// no mocks at the process boundary) lands with the $category-promotions
// keyword, and the real Email/llmInspect JMAP method -- driven over HTTP
// against the running server's public listener, not a direct store read
// -- returns a non-empty transparency record for it. This is the same
// deterministic plugin scripts/dev-instance.sh wires into every dev
// instance (internal/testfakes/fakeclassify, cmd/heroldfakeclassify), run
// here as a real child process rather than scripted via env vars like
// internal/plugin/testdata/classifierfixture.
//
// Runs on both store backends: SQLite always, Postgres when HEROLD_PG_DSN
// is set (STANDARDS section 8's "every integration test runs on both
// backends"), reusing the harness classify_acceptance_matrix_e2e_test.go
// already builds for the mail.classify acceptance matrix.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
)

// buildFakeClassifyPlugin compiles cmd/heroldfakeclassify (which wraps
// internal/testfakes/fakeclassify) into t.TempDir() and returns its path.
func buildFakeClassifyPlugin(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "heroldfakeclassify")
	cmd := exec.Command("go", "build", "-o", out, "github.com/hanshuebner/herold/cmd/heroldfakeclassify")
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build heroldfakeclassify: %v\n%s", err, outb)
	}
	return out
}

// deliverFakeClassifyMessage sends one raw-SMTP message with the given
// subject to alice@domain over a relay-in (unauthenticated) listener.
func deliverFakeClassifyMessage(t *testing.T, smtpAddr, domain, subject, msgID string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", smtpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial smtp: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	send := func(line string) {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte(line + "\r\n"))
	}
	expect := func(want int) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("read smtp reply: %v", err)
			}
			l = strings.TrimRight(l, "\r\n")
			if len(l) < 4 {
				t.Fatalf("short smtp line: %q", l)
			}
			if l[3] == ' ' {
				var code int
				fmt.Sscanf(l[:3], "%d", &code)
				if code != want {
					t.Fatalf("expected %d, got %d: %s", want, code, l)
				}
				return
			}
		}
	}
	expect(220) // greeting
	send("EHLO sender.external")
	expect(250)
	send("MAIL FROM:<bob@external.example>")
	expect(250)
	send("RCPT TO:<alice@" + domain + ">")
	expect(250)
	send("DATA")
	expect(354)
	rawMsg := "From: bob@external.example\r\n" +
		"To: alice@" + domain + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-ID: <" + msgID + "@external.example>\r\n" +
		"\r\n" +
		"Check out this week's deals.\r\n" +
		".\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, _ = conn.Write([]byte(rawMsg))
	expect(250) // DATA accepted
	send("QUIT")
}

// insertE2EAPIKey creates alice's JMAP-usable API key directly in the
// store (mirroring extidentity_e2e_test.go's seedExtIdentityStore) so the
// test can drive the real /jmap HTTP endpoint without a session-cookie
// login flow.
func insertE2EAPIKey(t *testing.T, st store.Store, pid store.PrincipalID) string {
	t.Helper()
	const apiKeyPlain = protoadmin.APIKeyPrefix + "fakeclassify_e2e_key_0000000000000001"
	if _, err := st.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: pid,
		Hash:        protoadmin.HashAPIKey(apiKeyPlain),
		Name:        "fakeclassify-e2e",
		ScopeJSON:   `["admin","mail.send","end-user"]`,
	}); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return apiKeyPlain
}

// callLLMInspect issues a real POST /jmap Email/llmInspect call and
// returns the decoded "list" array from the method's first response.
func callLLMInspect(t *testing.T, publicAddr, apiKey, accountID, emailID string) []map[string]any {
	t.Helper()
	args := map[string]any{
		"accountId": accountID,
		"ids":       []string{emailID},
	}
	argsBytes, _ := json.Marshal(args)
	envelope := map[string]any{
		"using": []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
			"https://netzhansa.com/jmap/llm-transparency",
		},
		"methodCalls": []any{
			[]any{"Email/llmInspect", json.RawMessage(argsBytes), "t0"},
		},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/jmap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /jmap Email/llmInspect: %v", err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /jmap Email/llmInspect: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses []json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode jmap response: %v body=%s", err, raw)
	}
	if len(out.MethodResponses) == 0 {
		t.Fatalf("no method responses: %s", raw)
	}
	var tuple []json.RawMessage
	if err := json.Unmarshal(out.MethodResponses[0], &tuple); err != nil || len(tuple) < 2 {
		t.Fatalf("decode method response tuple: %v body=%s", err, raw)
	}
	var methodName string
	_ = json.Unmarshal(tuple[0], &methodName)
	if methodName != "Email/llmInspect" {
		t.Fatalf("expected Email/llmInspect response, got %q: %s", methodName, tuple[1])
	}
	var result struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(tuple[1], &result); err != nil {
		t.Fatalf("decode Email/llmInspect result: %v body=%s", err, tuple[1])
	}
	return result.List
}

// TestFakeClassify_E2E_PromoSubjectCategorisedAndInspectable is the #364
// acceptance test: a message with "+promo" in the subject, delivered
// through the composed server against a real heroldfakeclassify child
// process, lands with the $category-promotions keyword and produces a
// non-empty Email/llmInspect record reachable over the real /jmap HTTP
// endpoint.
func TestFakeClassify_E2E_PromoSubjectCategorisedAndInspectable(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fakeBin := buildFakeClassifyPlugin(t)
	for _, backend := range classifyMatrixBackends() {
		t.Run(backend, func(t *testing.T) {
			var pgDSN string
			if backend == "postgres" {
				pgDSN = os.Getenv("HEROLD_PG_DSN")
			}
			h := startClassifyMatrixServer(t, backend, pgDSN, classifierPluginTOML(fakeBin, "classifier"), 0)

			// Seed alice's API key directly in the store so the test can
			// drive /jmap without a session-cookie login flow.
			st := h.openVerifyStore(t)
			apiKey := insertE2EAPIKey(t, st, h.pid)
			_ = st.Close()

			deliverFakeClassifyMessage(t, h.smtpAddr, h.domain, "Big sale +promo today", "fakeclassify-e2e-promo")

			msg := waitForMessageInMailbox(t, h, "INBOX")
			if kw := categoryKeyword(msg); kw != "$category-promotions" {
				t.Fatalf("category keyword = %q, want $category-promotions", kw)
			}

			accountID := jmapAccountID(t, h.publicAddr, apiKey)
			emailID := strconv.FormatUint(uint64(msg.ID), 10)

			list := callLLMInspect(t, h.publicAddr, apiKey, accountID, emailID)
			if len(list) == 0 {
				t.Fatalf("Email/llmInspect returned no entries for message %s", emailID)
			}
			entry := list[0]
			cat, _ := entry["category"].(map[string]any)
			if cat == nil {
				t.Fatalf("Email/llmInspect entry has no category sub-object: %+v", entry)
			}
			if assigned, _ := cat["assigned"].(string); assigned != "promotions" {
				t.Fatalf("category.assigned = %v, want %q", cat["assigned"], "promotions")
			}
			spam, _ := entry["spam"].(map[string]any)
			if spam == nil {
				t.Fatalf("Email/llmInspect entry has no spam sub-object: %+v", entry)
			}
			if verdict, _ := spam["verdict"].(string); verdict != "ham" {
				t.Fatalf("spam.verdict = %v, want %q", spam["verdict"], "ham")
			}
		})
	}
}
