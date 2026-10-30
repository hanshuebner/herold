package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
)

// realModelFixtures are synthetic messages (no production sender, host
// or text) for the opt-in real-model acceptance run: a document-download
// lure modelled on the shape of #551, and two ham shapes modelled on the
// false positives #538 (service-sent account verification, link on the
// sender's own domain) and #544 (plain personal inquiry, no links).
var realModelFixtures = []struct {
	name       string
	fromDomain string
	dmarc      mailauth.AuthStatus
	raw        string
	wantSpam   bool
}{
	{
		name:       "lure_551_shape",
		fromDomain: "formacao-exemplo.example",
		dmarc:      mailauth.AuthPass,
		wantSpam:   true,
		raw: "From: Departamento de Formacao <geral@formacao-exemplo.example>\r\n" +
			"To: me@example.com\r\nSubject: Documento partilhado consigo\r\n" +
			"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\nMessage-ID: <lure@formacao-exemplo.example>\r\n" +
			"MIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n" +
			"<html><body><p>Ola,</p>" +
			"<p>Foi partilhado consigo um documento com a referencia 48213. O ficheiro fica disponivel durante 48 horas.</p>" +
			"<p><a href=\"https://files.docs-share.example/d/48213?t=abc\" style=\"padding:12px;background:#0a66c2\">Transferir documento</a></p>" +
			"<p>Cumprimentos,<br>Departamento de Formacao</p></body></html>\r\n",
	},
	{
		name:       "ham_538_shape_verification",
		fromDomain: "photo-service.example",
		dmarc:      mailauth.AuthPass,
		wantSpam:   false,
		raw: "From: Photo Service <no-reply@photo-service.example>\r\n" +
			"To: me@example.com\r\nSubject: Verify your email address\r\n" +
			"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\nMessage-ID: <v@photo-service.example>\r\n" +
			"MIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n" +
			"<html><body><p>Welcome to Photo Service. Your verification code is <b>482913</b>.</p>" +
			"<p><a href=\"https://app.photo-service.example/verify?code=482913\">Verify email</a></p>" +
			"<p>If you did not request this, you can safely ignore this message.</p></body></html>\r\n",
	},
	{
		name:       "ham_544_shape_inquiry",
		fromDomain: "",
		dmarc:      mailauth.AuthNone,
		wantSpam:   false,
		raw: "From: Maria Beispiel <maria.beispiel@webmail.example>\r\n" +
			"To: info@example.com\r\nSubject: Frage zur Mitgliedschaft\r\n" +
			"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\nMessage-ID: <q@webmail.example>\r\n" +
			"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" +
			"Guten Tag,\r\n\r\nich interessiere mich fuer eine Mitgliedschaft im Verein. " +
			"Koennen Sie mir bitte mitteilen, wie hoch der Jahresbeitrag ist und wann das naechste Treffen stattfindet?\r\n\r\n" +
			"Viele Gruesse\r\nMaria Beispiel\r\n",
	},
}

// TestRealModel_LureAndFalsePositiveFixtures drives the fixtures through
// the compiled plugin against a real OpenAI-compatible endpoint. It runs
// only when HEROLD_SPAMLLM_REAL_ENDPOINT and HEROLD_SPAMLLM_REAL_MODEL are
// set (HEROLD_SPAMLLM_REAL_KEY_ENV names the environment variable holding
// the API key, HEROLD_SPAMLLM_REAL_RESPONSE_FORMAT overrides the
// response_format option) and logs each verdict and its signals. The lure
// must come back spam with "phishing" among the spam signals, the ham
// shapes must come back ham.
func TestRealModel_LureAndFalsePositiveFixtures(t *testing.T) {
	endpoint := os.Getenv("HEROLD_SPAMLLM_REAL_ENDPOINT")
	model := os.Getenv("HEROLD_SPAMLLM_REAL_MODEL")
	if endpoint == "" || model == "" {
		t.Skip("HEROLD_SPAMLLM_REAL_ENDPOINT / HEROLD_SPAMLLM_REAL_MODEL not set; no real model reachable")
	}
	opts := map[string]any{"endpoint": endpoint, "model": model, "spam_threshold": 0.5}
	if k := os.Getenv("HEROLD_SPAMLLM_REAL_KEY_ENV"); k != "" {
		opts["api_key_env"] = k
	}
	if f := os.Getenv("HEROLD_SPAMLLM_REAL_RESPONSE_FORMAT"); f != "" {
		opts["response_format"] = f
	}
	p := spawnPlugin(t, buildPlugin(t))
	defer p.close()
	p.initialize(t)
	if err := p.configure(t, opts); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for _, fx := range realModelFixtures {
		t.Run(fx.name, func(t *testing.T) {
			msg, err := mailparse.Parse(bytes.NewReader([]byte(fx.raw)), mailparse.ParseOptions{})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var auth *mailauth.AuthResults
			if fx.fromDomain != "" {
				auth = &mailauth.AuthResults{
					SPF:   mailauth.SPFResult{Status: mailauth.AuthPass},
					DKIM:  []mailauth.DKIMResult{{Status: mailauth.AuthPass}},
					DMARC: mailauth.DMARCResult{Status: fx.dmarc, HeaderFrom: fx.fromDomain},
				}
			}
			req := spam.BuildRequest(msg, auth)
			req.OwnAddresses = []string{"me@example.com", "info@example.com"}
			req.OwnAddressesComplete = true
			wire, err := req.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			var params map[string]any
			if err := json.Unmarshal(wire, &params); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			res, err := p.mailClassify(ctx, params)
			if err != nil {
				t.Fatalf("mail.classify: %v", err)
			}
			verdict, _ := res["verdict"].(string)
			t.Logf("model=%s verdict=%s score=%v mismatch=%v spam_signals=%v ham_signals=%v reason=%v",
				model, verdict, res["score"], req.LinkHostMismatch, res["spam_signals"], res["ham_signals"], res["reason"])
			if fx.wantSpam {
				if verdict != "spam" {
					t.Errorf("verdict = %q, want spam", verdict)
				}
				sigs, _ := res["spam_signals"].([]any)
				found := false
				for _, s := range sigs {
					if str, _ := s.(string); strings.EqualFold(str, "phishing") {
						found = true
					}
				}
				if !found {
					t.Errorf("spam_signals %v lack phishing", sigs)
				}
			} else if verdict != "ham" {
				t.Errorf("verdict = %q, want ham", verdict)
			}
		})
	}
}
