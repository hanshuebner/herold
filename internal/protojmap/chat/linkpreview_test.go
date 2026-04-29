package chat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/linkpreview"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/chat"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// setupLinkPreviewFixture mirrors setupFixture but registers the chat
// handlers with a linkpreview.Fetcher pointing at the supplied test
// server. The fetcher uses NewWithClient so it bypasses the SSRF guard
// (httptest binds 127.0.0.1 which the production guard would refuse).
func setupLinkPreviewFixture(t *testing.T, upstreamClient *http.Client) *fixture {
	t.Helper()
	srv, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}},
	})

	ctx := context.Background()
	alice, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal alice: %v", err)
	}
	bob, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
		DisplayName:    "Bob",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal bob: %v", err)
	}
	carol, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "carol@example.test",
		DisplayName:    "Carol",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal carol: %v", err)
	}

	plaintext := "hk_test_alice_lp"
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: alice.ID,
		Hash:        hash,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(srv.Store, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	previewer := linkpreview.NewWithClient(upstreamClient, linkpreview.Options{
		Logger: srv.Logger,
	})
	chat.RegisterWithFTSAndLinkPreview(jmapServ.Registry(), srv.Store, nil, previewer,
		srv.Logger, srv.Clock, chat.DefaultLimits())

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{
		srv: srv, pid: alice.ID, otherPID: bob.ID, thirdPID: carol.ID,
		client: client, baseURL: base, apiKey: plaintext, jmapServ: jmapServ,
	}
}

// TestMessage_Set_AttachesLinkPreviewFromBodyText asserts the
// happy-path of the URL preview pipeline: a Message/set whose body.text
// contains a URL pointing at a page advertising OG metadata round-trips
// with a populated linkPreviews array on the wire form.
func TestMessage_Set_AttachesLinkPreviewFromBodyText(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
<head>
  <title>Plain Title</title>
  <meta property="og:title" content="Hello, World"/>
  <meta property="og:description" content="A friendly greeting page."/>
  <meta property="og:image" content="/img/cover.png"/>
  <meta property="og:site_name" content="Greeting Co."/>
</head>
<body>...</body>
</html>`))
	}))
	defer upstream.Close()

	f := setupLinkPreviewFixture(t, upstream.Client())
	cid, _ := createSpace(t, f, "team")

	body := "check this out: " + upstream.URL + "/post/1 -- nice eh?"
	_, raw := f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": cid,
				"body":           map[string]any{"text": body, "format": "text"},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal create: %v: %s", err, raw)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("create failed: %+v / %+v", setResp.Created, setResp.NotCreated)
	}
	created := setResp.Created["m1"]
	previews, _ := created["linkPreviews"].([]any)
	if len(previews) != 1 {
		t.Fatalf("linkPreviews = %+v, want exactly 1", previews)
	}
	preview, _ := previews[0].(map[string]any)
	if got, _ := preview["title"].(string); got != "Hello, World" {
		t.Errorf("title = %q, want Hello, World", got)
	}
	if got, _ := preview["description"].(string); got != "A friendly greeting page." {
		t.Errorf("description = %q", got)
	}
	if img, _ := preview["imageUrl"].(string); !strings.HasSuffix(img, "/img/cover.png") {
		t.Errorf("imageUrl = %q, want absolute /img/cover.png", img)
	}
	if site, _ := preview["siteName"].(string); site != "Greeting Co." {
		t.Errorf("siteName = %q", site)
	}
}

// TestMessage_Set_NoURLs_NoLinkPreviewsField asserts that the
// linkPreviews wire field is OMITTED on a message with no URLs in
// body.text. The omitempty json tag means the JSON object simply
// doesn't contain the key when the slice is empty / nil.
func TestMessage_Set_NoURLs_NoLinkPreviewsField(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected upstream fetch: %s %s", r.Method, r.URL)
	}))
	defer upstream.Close()

	f := setupLinkPreviewFixture(t, upstream.Client())
	cid, _ := createSpace(t, f, "team")

	_, raw := f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": cid,
				"body":           map[string]any{"text": "no urls here", "format": "text"},
			},
		},
	})
	var setResp struct {
		Created    map[string]json.RawMessage `json:"created"`
		NotCreated map[string]any             `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	created, ok := setResp.Created["m1"]
	if !ok {
		t.Fatalf("create failed: %+v", setResp.NotCreated)
	}
	// linkPreviews is omitempty in the wire shape; assert the literal
	// JSON does not carry the key. (A nil slice still encodes as
	// `null` without omitempty, but the typed field has it.)
	if strings.Contains(string(created), `"linkPreviews"`) {
		t.Errorf("expected linkPreviews to be omitted when there are no URLs; got %s", created)
	}
}
