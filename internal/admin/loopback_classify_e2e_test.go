package admin

// loopback_classify_e2e_test.go is the acceptance test for issue #559:
// mail one principal sends to a principal on the same server (or to
// itself) travels the queue's loopback path, and its ingest must record
// an authenticated local submission. Before the fix IngestBytes ran
// DKIM/SPF/DMARC on the unsigned message from 127.0.0.1, the classifier
// was told the sender failed authentication, and the decisive-signal
// override (dmarc_fail + unauthenticated_sender, re #452) resolved the
// model's ham verdict to spam, filing the message in Junk.
//
// The test boots the real admin.StartServer with a real classifierfixture
// child process whose HEROLD_TEST_CLASSIFY_SIGNALS_ON_UNAUTH knob makes it
// report exactly those two signals whenever the request does not present
// an authenticated sender, submits through both the real /jmap
// EmailSubmission/set and the real HTTP send API, and reads the result
// back from the store. The same fixture and server also receive an
// external SMTP message that fails DMARC, which must still reach Junk.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// loopbackSeedDraft stores a draft message owned by pid addressed to
// "to" and returns its JMAP email id.
func loopbackSeedDraft(t *testing.T, st store.Store, pid store.PrincipalID, from, to, subject string) string {
	t.Helper()
	ctx := context.Background()
	mb, err := st.Meta().GetMailboxByName(ctx, pid, "Drafts")
	if err != nil {
		mb, err = st.Meta().InsertMailbox(ctx, store.Mailbox{
			PrincipalID: pid, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
		})
		if err != nil {
			t.Fatalf("insert Drafts: %v", err)
		}
	}
	body := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-ID: <" + subject + "@test.local>\r\n" +
		"\r\n" +
		"loopback classify e2e body.\r\n"
	ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		Blob:     ref,
		Size:     int64(len(body)),
		Envelope: store.Envelope{Subject: subject, From: from, To: to},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert draft: %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	if err != nil {
		t.Fatalf("list drafts: %v", err)
	}
	for _, m := range msgs {
		if m.UID == uid {
			return strconv.FormatUint(uint64(m.ID), 10)
		}
	}
	t.Fatalf("seeded draft uid %d not found", uid)
	return ""
}

// loopbackSendRaw submits one message through POST /api/v1/mail/send-raw.
func loopbackSendRaw(t *testing.T, publicAddr, apiKey, from, to, subject string) {
	t.Helper()
	raw := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Sun, 08 Nov 2026 10:00:00 +0000\r\n" +
		"Message-ID: <" + subject + "@test.local>\r\n" +
		"\r\n" +
		"loopback classify e2e body.\r\n"
	body, _ := json.Marshal(map[string]any{
		"destinations": []string{to},
		"rawMessage":   base64.StdEncoding.EncodeToString([]byte(raw)),
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/api/v1/mail/send-raw", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST send-raw: %v", err)
	}
	rb, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send-raw status=%d body=%s", resp.StatusCode, rb)
	}
}

// loopbackFindBySubject polls mailbox name of pid until a message with
// the subject appears, returning it. It fails when the server exits or
// after a generous deadline.
func loopbackFindBySubject(t *testing.T, h *classifyMatrixHarness, pid store.PrincipalID, mailbox, subject string, wait time.Duration) (store.Message, bool) {
	t.Helper()
	st := h.openVerifyStore(t)
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		select {
		case <-h.done:
			t.Fatalf("server exited while waiting for %q in %s", subject, mailbox)
		default:
		}
		if mb, err := st.Meta().GetMailboxByName(ctx, pid, mailbox); err == nil {
			msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
			if err == nil {
				for _, m := range msgs {
					if m.Envelope.Subject == subject {
						return m, true
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return store.Message{}, false
}

func runLoopbackClassifyE2E(t *testing.T, backend string) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fixturePath := buildClassifierFixture(t)
	t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", "ham")
	t.Setenv("HEROLD_TEST_CLASSIFY_SCORE", "0.05")
	t.Setenv("HEROLD_TEST_CLASSIFY_CATEGORY", "promotions")
	t.Setenv("HEROLD_TEST_CLASSIFY_SIGNALS_ON_UNAUTH", "1")

	var pgDSN string
	if backend == "postgres" {
		pgDSN = os.Getenv("HEROLD_PG_DSN")
	}
	h := startClassifyMatrixServer(t, backend, pgDSN, classifierPluginTOML(fixturePath, "classifier"), 0)
	alice := "alice@" + h.domain
	bob := "bob@" + h.domain

	st := h.openVerifyStore(t)
	ctx := context.Background()
	dir := directory.New(st.Meta(), discardLogger(), clock.NewReal(), nil)
	bobPID, err := dir.CreatePrincipal(ctx, bob, "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	apiKey := insertE2EAPIKey(t, st, h.pid)
	accountID := jmapAccountID(t, h.publicAddr, apiKey)

	type target struct {
		name string
		to   string
		pid  store.PrincipalID
	}
	targets := []target{
		{"to_other_principal", bob, bobPID},
		{"to_self", alice, h.pid},
	}
	for _, via := range []string{"jmap", "sendapi"} {
		for _, tg := range targets {
			t.Run(via+"/"+tg.name, func(t *testing.T) {
				subject := fmt.Sprintf("loopback-%s-%s", via, tg.name)
				switch via {
				case "jmap":
					emailID := loopbackSeedDraft(t, st, h.pid, alice, tg.to, subject)
					submitViaJMAP(t, h.publicAddr, apiKey, accountID, "default", emailID)
				case "sendapi":
					loopbackSendRaw(t, h.publicAddr, apiKey, alice, tg.to, subject)
				}
				msg, ok := loopbackFindBySubject(t, h, tg.pid, "INBOX", subject, 20*time.Second)
				if !ok {
					if _, inJunk := loopbackFindBySubject(t, h, tg.pid, "Junk", subject, 2*time.Second); inJunk {
						t.Fatalf("message %q was filed to Junk; loopback mail must land in the Inbox", subject)
					}
					t.Fatalf("message %q reached neither INBOX nor Junk", subject)
				}
				if kw := categoryKeyword(msg); kw != "$category-promotions" {
					t.Errorf("category keyword = %q, want $category-promotions (category classification must still run)", kw)
				}
				// The transparency record is written after the message row
				// becomes visible; poll until it lands.
				var rec store.LLMClassificationRecord
				var err error
				for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
					if rec, err = st.Meta().GetLLMClassification(ctx, msg.ID); err == nil {
						break
					}
				}
				if err != nil {
					t.Fatalf("GetLLMClassification: %v", err)
				}
				if rec.SpamVerdict == nil || *rec.SpamVerdict != "ham" {
					t.Errorf("SpamVerdict = %v, want ham", rec.SpamVerdict)
				}
			})
		}
	}
	_ = st.Close()

	// External inbound SMTP that fails DMARC must still land in Junk.
	t.Run("external_dmarc_fail_still_junk", func(t *testing.T) {
		deliverClassifyMatrixMessage(t, h, "", "loopback-external")
		if _, ok := loopbackFindBySubject(t, h, h.pid, "Junk", "matrix test", 20*time.Second); !ok {
			t.Fatalf("external message failing DMARC did not reach Junk")
		}
	})

	// A forged local-submission Authentication-Results header on inbound
	// SMTP mail grants nothing: the second external message lands in Junk
	// too (alice's Junk then holds two "matrix test" messages).
	t.Run("forged_local_auth_header_still_junk", func(t *testing.T) {
		deliverClassifyMatrixMessage(t, h, "Authentication-Results: mx.test; auth=pass smtp.auth=local\r\n", "loopback-forged")
		st := h.openVerifyStore(t)
		defer func() { _ = st.Close() }()
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			mb, err := st.Meta().GetMailboxByName(ctx, h.pid, "Junk")
			if err != nil {
				continue
			}
			msgs, _ := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
			if len(msgs) >= 2 {
				return
			}
		}
		t.Fatalf("message with a forged local-auth header did not reach Junk")
	})

	// External mail that fails DMARC and is Sieve-redirected to a local
	// principal travels the loopback as well, but is not an authenticated
	// submission: it must still land in Junk, never in the Inbox.
	t.Run("external_sieve_redirect_to_local_still_junk", func(t *testing.T) {
		st := h.openVerifyStore(t)
		if err := st.Meta().SetSieveScript(ctx, h.pid, "redirect \""+bob+"\";\n"); err != nil {
			t.Fatalf("SetSieveScript: %v", err)
		}
		_ = st.Close()
		deliverClassifyMatrixMessage(t, h, "", "loopback-redirect")
		if _, ok := loopbackFindBySubject(t, h, bobPID, "Junk", "matrix test", 20*time.Second); !ok {
			t.Fatalf("redirected external DMARC-fail message did not reach bob's Junk")
		}
		if _, ok := loopbackFindBySubject(t, h, bobPID, "INBOX", "matrix test", time.Second); ok {
			t.Fatalf("redirected external DMARC-fail message was laundered into bob's INBOX")
		}
	})
}

func TestLoopbackClassify_E2E_SQLite(t *testing.T) {
	runLoopbackClassifyE2E(t, "sqlite")
}

func TestLoopbackClassify_E2E_Postgres(t *testing.T) {
	if os.Getenv("HEROLD_PG_DSN") == "" {
		t.Skip("HEROLD_PG_DSN not set")
	}
	runLoopbackClassifyE2E(t, "postgres")
}
