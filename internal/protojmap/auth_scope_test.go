package protojmap_test

// auth_scope_test.go asserts REQ-AUTH-SCOPE-02 on the JMAP Bearer
// path (issue #418): a Bearer hk_... key's stored scope set gates
// every JMAP surface, not just the "hk_" prefix + stored-hash match.
// Before the fix, AuthenticateBearerToken / requireAuth never
// consulted ScopeJSON at all, so a key minted with `--scope
// bug-reports` (#416) or the operator-default `[mail.send]` scope had
// full JMAP access to the principal's mail.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// createAPIKeyWithScope mints a fresh API key for pid carrying exactly
// scopes (not the InsertAPIKey empty-ScopeJSON backfill to ["admin"]),
// so tests can exercise a credential scoped to less than full access.
func createAPIKeyWithScope(ctx context.Context, st store.Store, pid store.PrincipalID, name string, scopes []auth.Scope) (string, store.APIKey, error) {
	plaintext := fmt.Sprintf("hk_protojmap_scope_test_%s_%d", name, pid)
	scopeJSON, err := json.Marshal(scopes)
	if err != nil {
		return "", store.APIKey{}, err
	}
	row, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: pid,
		Hash:        protojmap.HashAPIKeyForTest(plaintext),
		Name:        name,
		ScopeJSON:   string(scopeJSON),
		CreatedAt:   time.Now(),
	})
	if err != nil {
		return "", store.APIKey{}, err
	}
	return plaintext, row, nil
}

func assertProblemStatus(t *testing.T, res *http.Response, raw []byte, wantStatus int) {
	t.Helper()
	if res.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", res.StatusCode, wantStatus, raw)
	}
	if wantStatus == http.StatusForbidden {
		if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("Content-Type = %q, want application/problem+json", ct)
		}
		var doc struct {
			Type   string `json:"type"`
			Status int    `json:"status"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode problem+json: %v; body = %s", err, raw)
		}
		if doc.Status != http.StatusForbidden {
			t.Fatalf("problem status field = %d, want 403", doc.Status)
		}
	}
}

// TestJMAPScope_BugReportsOnlyKey_RefusedEverywhere: a key minted with
// `herold api-key create --scope bug-reports` (issue #416) authenticates
// (valid hk_ prefix, matching hash, authenticatable principal) but must
// be refused with 403 by every JMAP surface, since it carries no mail
// scope at all.
func TestJMAPScope_BugReportsOnlyKey_RefusedEverywhere(t *testing.T) {
	f := newFixture(t)
	key, _, err := createAPIKeyWithScope(context.Background(), f.store, f.pid, "bugreports", []auth.Scope{auth.ScopeBugReports})
	if err != nil {
		t.Fatalf("createAPIKeyWithScope: %v", err)
	}

	res, raw := f.doRequest("GET", "/.well-known/jmap", key, nil)
	assertProblemStatus(t, res, raw, http.StatusForbidden)

	body, _ := json.Marshal(protojmap.Request{
		Using:       []protojmap.CapabilityID{protojmap.CapabilityCore},
		MethodCalls: []protojmap.Invocation{{Name: "Core/echo", Args: json.RawMessage(`{}`), CallID: "c0"}},
	})
	res2, raw2 := f.doRequest("POST", "/jmap", key, body)
	assertProblemStatus(t, res2, raw2, http.StatusForbidden)
}

// TestJMAPScope_MailSendOnlyKey_RefusedFromReadEndpoints: the
// protoadmin operator default (`herold api-key create` with no
// --scope) mints `[mail.send]`. That key can submit mail but must not
// read it: the session endpoint, POST /jmap, download, and EventSource
// all require mail.receive.
func TestJMAPScope_MailSendOnlyKey_RefusedFromReadEndpoints(t *testing.T) {
	f := newFixture(t)
	key, _, err := createAPIKeyWithScope(context.Background(), f.store, f.pid, "mailsend", []auth.Scope{auth.ScopeMailSend})
	if err != nil {
		t.Fatalf("createAPIKeyWithScope: %v", err)
	}

	res, raw := f.doRequest("GET", "/.well-known/jmap", key, nil)
	assertProblemStatus(t, res, raw, http.StatusForbidden)

	body, _ := json.Marshal(protojmap.Request{
		Using:       []protojmap.CapabilityID{protojmap.CapabilityCore},
		MethodCalls: []protojmap.Invocation{{Name: "Core/echo", Args: json.RawMessage(`{}`), CallID: "c0"}},
	})
	res2, raw2 := f.doRequest("POST", "/jmap", key, body)
	assertProblemStatus(t, res2, raw2, http.StatusForbidden)

	req, err := http.NewRequest("GET", f.httpd.URL+"/jmap/eventsource?types=Email&closeafter=state&ping=300", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res3, err := f.httpd.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("eventsource do: %v", err)
	}
	defer res3.Body.Close()
	if res3.StatusCode != http.StatusForbidden {
		t.Fatalf("eventsource status = %d, want 403", res3.StatusCode)
	}
}

// TestJMAPScope_MailReceiveOnlyKey_ReadsButCannotUpload: a
// mail.receive-only key can reach the read surfaces but is refused at
// upload, which requires mail.send.
func TestJMAPScope_MailReceiveOnlyKey_ReadsButCannotUpload(t *testing.T) {
	f := newFixture(t)
	key, _, err := createAPIKeyWithScope(context.Background(), f.store, f.pid, "mailreceive", []auth.Scope{auth.ScopeMailReceive})
	if err != nil {
		t.Fatalf("createAPIKeyWithScope: %v", err)
	}

	res, raw := f.doRequest("GET", "/.well-known/jmap", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d, want 200; body = %s", res.StatusCode, raw)
	}

	accountID := protojmap.AccountIDForPrincipal(f.pid)
	req, err := http.NewRequest("POST", f.httpd.URL+"/jmap/upload/"+accountID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+key)
	res2, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("upload do: %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusForbidden {
		t.Fatalf("upload status = %d, want 403", res2.StatusCode)
	}
}

// TestJMAPScope_EmailSubmissionSet_RequiresMailSend: even once a
// request has cleared the transport-level mail.receive gate on POST
// /jmap, the EmailSubmission/set method call itself requires
// mail.send -- a mail.receive-only key can batch other calls in the
// same request but not this one (REQ-PROTO-42: EmailSubmission
// dispatches into the outbound queue).
func TestJMAPScope_EmailSubmissionSet_RequiresMailSend(t *testing.T) {
	f := newFixture(t)
	const cap = protojmap.CapabilityMail
	fh := &fakeSubmissionHandler{}
	f.srv.Registry().Register(cap, fh)

	key, _, err := createAPIKeyWithScope(context.Background(), f.store, f.pid, "mailreceive-set", []auth.Scope{auth.ScopeMailReceive})
	if err != nil {
		t.Fatalf("createAPIKeyWithScope: %v", err)
	}

	body, _ := json.Marshal(protojmap.Request{
		Using:       []protojmap.CapabilityID{cap},
		MethodCalls: []protojmap.Invocation{{Name: "EmailSubmission/set", Args: json.RawMessage(`{}`), CallID: "c0"}},
	})
	res, raw := f.doRequest("POST", "/jmap", key, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (per-call error, not transport 403); body = %s", res.StatusCode, raw)
	}
	var env protojmap.Response
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode: %v; body = %s", err, raw)
	}
	if len(env.MethodResponses) != 1 || env.MethodResponses[0].Name != "error" {
		t.Fatalf("methodResponses = %+v, want a single 'error' entry", env.MethodResponses)
	}
	var mErr protojmap.MethodError
	if err := json.Unmarshal(env.MethodResponses[0].Args, &mErr); err != nil {
		t.Fatalf("decode method error: %v", err)
	}
	if mErr.Type != "forbidden" {
		t.Fatalf("error type = %q, want forbidden", mErr.Type)
	}
	if fh.calls != 0 {
		t.Fatalf("handler.Execute called %d times, want 0 (scope gate must short-circuit before dispatch)", fh.calls)
	}
}

// fakeSubmissionHandler stands in for the real EmailSubmission/set
// handler; it records whether it was ever invoked so the scope-gate
// test can assert the call never reached it.
type fakeSubmissionHandler struct{ calls int }

func (h *fakeSubmissionHandler) Method() string { return "EmailSubmission/set" }
func (h *fakeSubmissionHandler) Execute(_ context.Context, _ json.RawMessage) (any, *protojmap.MethodError) {
	h.calls++
	return json.RawMessage(`{}`), nil
}

// TestJMAPScope_AdminKey_BypassesEveryGate: an operator key created
// with `--allow-admin-scope` (ScopeAdmin only, no explicit mail
// scopes) must still pass every JMAP gate, including the
// EmailSubmission/set finer gate.
func TestJMAPScope_AdminKey_BypassesEveryGate(t *testing.T) {
	f := newFixture(t)
	const cap = protojmap.CapabilityMail
	fh := &fakeSubmissionHandler{}
	f.srv.Registry().Register(cap, fh)

	key, _, err := createAPIKeyWithScope(context.Background(), f.store, f.pid, "admin", []auth.Scope{auth.ScopeAdmin})
	if err != nil {
		t.Fatalf("createAPIKeyWithScope: %v", err)
	}

	res, raw := f.doRequest("GET", "/.well-known/jmap", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d, want 200; body = %s", res.StatusCode, raw)
	}

	body, _ := json.Marshal(protojmap.Request{
		Using:       []protojmap.CapabilityID{cap},
		MethodCalls: []protojmap.Invocation{{Name: "EmailSubmission/set", Args: json.RawMessage(`{}`), CallID: "c0"}},
	})
	res2, raw2 := f.doRequest("POST", "/jmap", key, body)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body = %s", res2.StatusCode, raw2)
	}
	if fh.calls != 1 {
		t.Fatalf("handler.Execute called %d times, want 1 (admin scope must bypass the finer gate)", fh.calls)
	}
}

// TestJMAPScope_DeviceToken_PassesGate asserts directory.IssueDeviceToken
// mints a scope set (auth.AllEndUserScopes) that clears every JMAP gate
// this fix adds -- an already-issued device token on a production
// store (scope_json = the full end-user scope list, e.g. carried by an
// installed Android build) keeps authenticating after this change.
func TestJMAPScope_DeviceToken_PassesGate(t *testing.T) {
	f := newFixture(t)
	plaintext, key, err := f.dir.IssueDeviceToken(context.Background(), "alice@example.com", "correct-horse-battery-staple-1", "", "test device")
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}
	scope := protojmap.ParseAPIKeyScope(key.ScopeJSON)
	if !scope.Has(auth.ScopeMailReceive) || !scope.Has(auth.ScopeMailSend) {
		t.Fatalf("device token scope = %v, want mail.receive and mail.send", scope.Slice())
	}

	res, raw := f.doRequest("GET", "/.well-known/jmap", plaintext, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.StatusCode, raw)
	}
}

// TestParseAPIKeyScope_LegacyEmptyValue documents the fallback for a
// genuinely pre-scope-column row (empty scope_json): it reads back as
// [mail.send] -- refused by every JMAP read/EventSource/download/
// upload gate that requires mail.receive, but still passes protosend's
// HTTP send API gate. In practice storesqlite/storepg's InsertAPIKey
// backfills an empty ScopeJSON to ["admin"] at insert time, so this
// path only fires for a row that predates the scope column entirely.
func TestParseAPIKeyScope_LegacyEmptyValue(t *testing.T) {
	scope := protojmap.ParseAPIKeyScope("")
	if !scope.Has(auth.ScopeMailSend) {
		t.Fatalf("empty scope_json = %v, want mail.send", scope.Slice())
	}
	if scope.Has(auth.ScopeMailReceive) {
		t.Fatalf("empty scope_json = %v, must not carry mail.receive", scope.Slice())
	}
}

func TestParseAPIKeyScope_Malformed(t *testing.T) {
	scope := protojmap.ParseAPIKeyScope("{not json")
	if !scope.Has(auth.ScopeMailSend) || scope.Has(auth.ScopeMailReceive) {
		t.Fatalf("malformed scope_json = %v, want [mail.send] only", scope.Slice())
	}
}
