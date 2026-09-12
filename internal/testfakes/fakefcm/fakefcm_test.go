package fakefcm_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/testfakes/fakefcm"
)

func post(t *testing.T, url string, body any, bearer string) (*http.Response, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestSend_RecordsAndReturns200(t *testing.T) {
	srv := fakefcm.New(t, fakefcm.Options{ProjectID: "test-project"})

	resp, out := post(t, srv.SendURL(), map[string]any{
		"message": map[string]any{
			"token": "tok-1",
			"data":  map[string]string{"payload": "hello"},
			"android": map[string]any{
				"priority": "HIGH",
			},
		},
	}, "fake-bearer")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if _, ok := out["name"]; !ok {
		t.Errorf("response missing \"name\" field: %v", out)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("recorded %d messages; want 1", len(msgs))
	}
	m := msgs[0]
	if m.Token != "tok-1" || m.Data["payload"] != "hello" || m.AndroidPriority != "HIGH" {
		t.Errorf("recorded message = %+v; want token=tok-1 data.payload=hello priority=HIGH", m)
	}
	if m.AuthHeader != "Bearer fake-bearer" {
		t.Errorf("AuthHeader = %q; want \"Bearer fake-bearer\"", m.AuthHeader)
	}
}

func TestSend_UnregisteredToken_Returns404(t *testing.T) {
	srv := fakefcm.New(t, fakefcm.Options{})
	srv.SetUnregistered("stale-tok", true)

	resp, out := post(t, srv.SendURL(), map[string]any{
		"message": map[string]any{"token": "stale-tok"},
	}, "tok")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", resp.StatusCode)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || errObj["status"] != "UNREGISTERED" {
		t.Errorf("response = %v; want error.status = UNREGISTERED", out)
	}

	// The send is still recorded even when it results in UNREGISTERED,
	// matching FCM's real behaviour of returning the error per-request.
	if len(srv.Messages()) != 1 {
		t.Fatalf("recorded %d messages; want 1", len(srv.Messages()))
	}

	srv.SetUnregistered("stale-tok", false)
	resp2, _ := post(t, srv.SendURL(), map[string]any{
		"message": map[string]any{"token": "stale-tok"},
	}, "tok")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status after clearing = %d; want 200", resp2.StatusCode)
	}
}

func TestMessagesEndpoint_GetAndDelete(t *testing.T) {
	srv := fakefcm.New(t, fakefcm.Options{})
	post(t, srv.SendURL(), map[string]any{"message": map[string]any{"token": "a"}}, "b")

	resp, err := http.Get("http://" + srv.Addr() + "/messages")
	if err != nil {
		t.Fatalf("GET /messages: %v", err)
	}
	var list []fakefcm.Message
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(list) != 1 || list[0].Token != "a" {
		t.Fatalf("list = %+v; want one message with token=a", list)
	}

	req, _ := http.NewRequest(http.MethodDelete, "http://"+srv.Addr()+"/messages", nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /messages: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d; want 204", delResp.StatusCode)
	}
	if len(srv.Messages()) != 0 {
		t.Fatalf("messages after DELETE = %d; want 0", len(srv.Messages()))
	}
}
