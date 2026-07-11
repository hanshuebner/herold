package contacts_test

// Tests for REQ-CTS-01..05: Contact photo blob wiring.
// Each test runs on both backends via the testharness (SQLite in this
// file; the storepg _test.go leg wires these via storetest.Run which
// exercises IsContactPhotoBlobReferenced on both backends). The
// JMAP-level tests here cover the full handler path.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/contacts"
)

// uploadBlob posts bytes to the JMAP upload endpoint and returns the
// blobId (BLAKE3 hash). Uses the first fixture's base URL and client.
func uploadBlob(t *testing.T, f *fixture, content []byte, contentType string) string {
	t.Helper()
	ctx := context.Background()
	accountID := string(protojmap.AccountIDForPrincipal(f.pid))
	url := f.baseURL + "/jmap/upload/" + accountID
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("uploadBlob: new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Content-Type", contentType)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("uploadBlob: do: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("uploadBlob: read: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("uploadBlob: status = %d, body = %s", resp.StatusCode, body)
	}
	var up struct {
		BlobID string `json:"blobId"`
	}
	if err := json.Unmarshal(body, &up); err != nil {
		t.Fatalf("uploadBlob: unmarshal: %v: %s", err, body)
	}
	if up.BlobID == "" {
		t.Fatalf("uploadBlob: empty blobId in response: %s", body)
	}
	return up.BlobID
}

// downloadBlob fetches a blob via the JMAP download endpoint and
// returns the response body. Fails the test on network error; the
// caller checks the status code.
func downloadBlob(t *testing.T, f *fixture, blobID string) (statusCode int, body []byte) {
	t.Helper()
	accountID := string(protojmap.AccountIDForPrincipal(f.pid))
	url := fmt.Sprintf("%s/jmap/download/%s/%s/image%%2Fjpeg/photo.jpg",
		f.baseURL, accountID, blobID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("downloadBlob: new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("downloadBlob: do: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("downloadBlob: read: %v", err)
	}
	return resp.StatusCode, b
}

// setupFixtureWithLimits creates a fixture with custom AccountLimits
// useful for testing size and type constraints.
func setupFixtureWithLimits(t *testing.T, limits contacts.AccountLimits) *fixture {
	t.Helper()
	return setupFixtureLimits(t, limits)
}

// -- REQ-CTS-01: blobId validation ------------------------------------

// TestContactPhoto_ValidBlobID_CreateSucceeds verifies that a Contact
// with a valid owned image blobId is created and the photo can be
// downloaded by blobId.
func TestContactPhoto_ValidBlobID_CreateSucceeds(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "Photos")

	photoData := []byte("fake-jpeg-content-for-test")
	blobID := uploadBlob(t, f, photoData, "image/jpeg")

	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Photo Test"},
				"media": map[string]any{
					"photo1": map[string]any{
						"@type":     "MediaResource",
						"kind":      "photo",
						"blobId":    blobID,
						"mediaType": "image/jpeg",
					},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, raw)
	}
	if len(setResp.NotCreated) > 0 {
		t.Fatalf("notCreated: %+v", setResp.NotCreated)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("created = %d entries, want 1", len(setResp.Created))
	}

	// The photo must be downloadable via the JMAP download path (REQ-CTS-04).
	code, body := downloadBlob(t, f, blobID)
	if code != http.StatusOK {
		t.Fatalf("download status = %d, want 200; body = %s", code, body)
	}
	if !bytes.Equal(body, photoData) {
		t.Fatalf("downloaded bytes differ from uploaded; got %d bytes", len(body))
	}

	// The blob ref_count must be > 0 (GC root active, REQ-CTS-02).
	_, refCount, err := f.srv.Store.Meta().GetBlobRef(context.Background(), blobID)
	if err != nil {
		t.Fatalf("GetBlobRef: %v", err)
	}
	if refCount <= 0 {
		t.Fatalf("ref_count = %d after create, want > 0", refCount)
	}
}

// TestContactPhoto_UnknownBlobID_FailsWithSetError verifies that a
// foreign or unknown blobId is rejected with an invalidProperties
// SetError and does not create a contact (REQ-CTS-01).
func TestContactPhoto_UnknownBlobID_FailsWithSetError(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "Photos")

	// Use a syntactically valid but non-existent hash.
	fakeBlobID := strings.Repeat("a", 64)

	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Unknown Blob"},
				"media": map[string]any{
					"photo1": map[string]any{
						"@type":     "MediaResource",
						"kind":      "photo",
						"blobId":    fakeBlobID,
						"mediaType": "image/jpeg",
					},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]setErrorObj    `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, raw)
	}
	if len(setResp.Created) > 0 {
		t.Fatalf("expected no creation but got %d entries", len(setResp.Created))
	}
	se, ok := setResp.NotCreated["c1"]
	if !ok {
		t.Fatalf("c1 not in notCreated: %s", raw)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("SetError type = %q, want invalidProperties", se.Type)
	}
}

// setErrorObj is the wire form of a JMAP SetError in response bodies.
type setErrorObj struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Properties  []string `json:"properties"`
}

// TestContactPhoto_NonImageMediaType_Rejected verifies that a blobId
// with a non-image media type is rejected with a SetError (REQ-CTS-05).
func TestContactPhoto_NonImageMediaType_Rejected(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "Photos")

	blobID := uploadBlob(t, f, []byte("pdf-data"), "application/pdf")

	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "PDF Photo"},
				"media": map[string]any{
					"photo1": map[string]any{
						"@type":     "MediaResource",
						"kind":      "photo",
						"blobId":    blobID,
						"mediaType": "application/pdf",
					},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]setErrorObj    `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, raw)
	}
	if len(setResp.Created) > 0 {
		t.Fatalf("expected no creation but got: %+v", setResp.Created)
	}
	se, ok := setResp.NotCreated["c1"]
	if !ok {
		t.Fatalf("c1 not in notCreated: %s", raw)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("SetError type = %q, want invalidProperties", se.Type)
	}
}

// TestContactPhoto_OversizedBlob_Rejected verifies that a blob
// exceeding MaxPhotoBlobSize is rejected with a SetError (REQ-CTS-05).
func TestContactPhoto_OversizedBlob_Rejected(t *testing.T) {
	limits := contacts.DefaultLimits()
	limits.MaxPhotoBlobSize = 10 // 10 bytes — tiny limit for this test
	f := setupFixtureWithLimits(t, limits)
	bookID := makeBook(t, f, "Photos")

	// Upload a blob that exceeds the limit.
	blobID := uploadBlob(t, f, bytes.Repeat([]byte("x"), 20), "image/jpeg")

	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Big Photo"},
				"media": map[string]any{
					"photo1": map[string]any{
						"@type":     "MediaResource",
						"kind":      "photo",
						"blobId":    blobID,
						"mediaType": "image/jpeg",
					},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]setErrorObj    `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, raw)
	}
	if len(setResp.Created) > 0 {
		t.Fatalf("expected no creation but got: %+v", setResp.Created)
	}
	se, ok := setResp.NotCreated["c1"]
	if !ok {
		t.Fatalf("c1 not in notCreated: %s", raw)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("SetError type = %q, want invalidProperties", se.Type)
	}
}

// TestContactPhoto_GC_LiveContactRetainsBlob verifies that a blob
// referenced by a live contact is not GC-eligible (ref_count > 0) and
// that destroying the contact releases the blob (REQ-CTS-02, REQ-CTS-03).
func TestContactPhoto_GC_LiveContactRetainsBlob(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "GC")

	blobID := uploadBlob(t, f, []byte("gc-test-photo"), "image/jpeg")

	// Create contact with photo.
	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "GC Test"},
				"media": map[string]any{
					"photo1": map[string]any{
						"@type":  "MediaResource",
						"kind":   "photo",
						"blobId": blobID,
					},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("set unmarshal: %v", err)
	}
	if len(setResp.NotCreated) > 0 {
		t.Fatalf("notCreated: %+v", setResp.NotCreated)
	}
	contactID := setResp.Created["c1"]["id"].(string)

	// Blob must be rooted.
	ctx := context.Background()
	_, refCount, err := f.srv.Store.Meta().GetBlobRef(ctx, blobID)
	if err != nil {
		t.Fatalf("GetBlobRef after create: %v", err)
	}
	if refCount <= 0 {
		t.Fatalf("ref_count = %d after create, want > 0", refCount)
	}
	live, err := f.srv.Store.Meta().IsContactPhotoBlobReferenced(ctx, blobID)
	if err != nil {
		t.Fatalf("IsContactPhotoBlobReferenced after create: %v", err)
	}
	if !live {
		t.Fatal("IsContactPhotoBlobReferenced = false after create, want true")
	}

	// Destroy the contact.
	_, raw = f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"destroy":   []string{contactID},
	})
	var destroyResp struct {
		Destroyed    []string       `json:"destroyed"`
		NotDestroyed map[string]any `json:"notDestroyed"`
	}
	if err := json.Unmarshal(raw, &destroyResp); err != nil {
		t.Fatalf("destroy unmarshal: %v", err)
	}
	if len(destroyResp.NotDestroyed) > 0 {
		t.Fatalf("notDestroyed: %+v", destroyResp.NotDestroyed)
	}
	if len(destroyResp.Destroyed) != 1 {
		t.Fatalf("destroyed = %v, want [%s]", destroyResp.Destroyed, contactID)
	}

	// Blob must now be GC-eligible.
	_, refCount, err = f.srv.Store.Meta().GetBlobRef(ctx, blobID)
	if err != nil {
		t.Fatalf("GetBlobRef after destroy: %v", err)
	}
	if refCount != 0 {
		t.Fatalf("ref_count = %d after destroy, want 0", refCount)
	}
	live, err = f.srv.Store.Meta().IsContactPhotoBlobReferenced(ctx, blobID)
	if err != nil {
		t.Fatalf("IsContactPhotoBlobReferenced after destroy: %v", err)
	}
	if live {
		t.Fatal("IsContactPhotoBlobReferenced = true after destroy, want false")
	}
}

// TestContactPhoto_GC_SharedBlob_RetainedWhileSecondContactLives
// verifies that a blob shared by two contacts is retained while the
// second contact lives (no-eager-delete guarantee, REQ-CTS-03).
func TestContactPhoto_GC_SharedBlob_RetainedWhileSecondContactLives(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "SharedGC")

	blobID := uploadBlob(t, f, []byte("shared-photo-data"), "image/jpeg")

	createPhoto := func(clientKey, name string) string {
		t.Helper()
		_, raw := f.invoke(t, "Contact/set", map[string]any{
			"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
			"create": map[string]any{
				clientKey: map[string]any{
					"version":       "1.0",
					"addressBookId": bookID,
					"name":          map[string]any{"full": name},
					"media": map[string]any{
						"photo1": map[string]any{
							"@type":  "MediaResource",
							"kind":   "photo",
							"blobId": blobID,
						},
					},
				},
			},
		})
		var resp struct {
			Created    map[string]map[string]any `json:"created"`
			NotCreated map[string]any            `json:"notCreated"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("create %s unmarshal: %v", clientKey, err)
		}
		if len(resp.NotCreated) > 0 {
			t.Fatalf("create %s notCreated: %+v", clientKey, resp.NotCreated)
		}
		return resp.Created[clientKey]["id"].(string)
	}

	id1 := createPhoto("c1", "Shared Contact 1")
	id2 := createPhoto("c2", "Shared Contact 2")

	ctx := context.Background()
	_, refCount, err := f.srv.Store.Meta().GetBlobRef(ctx, blobID)
	if err != nil {
		t.Fatalf("GetBlobRef: %v", err)
	}
	if refCount < 2 {
		t.Fatalf("ref_count = %d after two creates, want >= 2", refCount)
	}

	// Destroy contact 1: blob must still be rooted by contact 2.
	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"destroy":   []string{id1},
	})
	var destroyResp struct {
		Destroyed    []string       `json:"destroyed"`
		NotDestroyed map[string]any `json:"notDestroyed"`
	}
	if err := json.Unmarshal(raw, &destroyResp); err != nil {
		t.Fatalf("destroy c1 unmarshal: %v", err)
	}
	if len(destroyResp.NotDestroyed) > 0 {
		t.Fatalf("c1 notDestroyed: %+v", destroyResp.NotDestroyed)
	}

	_, refCount, err = f.srv.Store.Meta().GetBlobRef(ctx, blobID)
	if err != nil {
		t.Fatalf("GetBlobRef after c1 destroy: %v", err)
	}
	if refCount <= 0 {
		t.Fatalf("ref_count = %d after c1 destroy (c2 still alive), want > 0", refCount)
	}
	live, err := f.srv.Store.Meta().IsContactPhotoBlobReferenced(ctx, blobID)
	if err != nil {
		t.Fatalf("IsContactPhotoBlobReferenced after c1 destroy: %v", err)
	}
	if !live {
		t.Fatal("IsContactPhotoBlobReferenced = false after c1 destroy (c2 still alive), want true")
	}

	// Destroy contact 2: now the blob is GC-eligible.
	_, raw = f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"destroy":   []string{id2},
	})
	if err := json.Unmarshal(raw, &destroyResp); err != nil {
		t.Fatalf("destroy c2 unmarshal: %v", err)
	}
	if len(destroyResp.NotDestroyed) > 0 {
		t.Fatalf("c2 notDestroyed: %+v", destroyResp.NotDestroyed)
	}

	_, refCount, err = f.srv.Store.Meta().GetBlobRef(ctx, blobID)
	if err != nil {
		t.Fatalf("GetBlobRef after c2 destroy: %v", err)
	}
	if refCount != 0 {
		t.Fatalf("ref_count = %d after both destroyed, want 0", refCount)
	}
}

// TestContactPhoto_Update_RemovesOldRef verifies that updating a
// contact to remove its photo releases the blob's GC root (REQ-CTS-03).
func TestContactPhoto_Update_RemovesOldRef(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "UpdatePhoto")

	blobID := uploadBlob(t, f, []byte("old-photo"), "image/jpeg")

	// Create with photo.
	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Update Photo"},
				"media": map[string]any{
					"photo1": map[string]any{
						"@type":  "MediaResource",
						"kind":   "photo",
						"blobId": blobID,
					},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("set unmarshal: %v", err)
	}
	if len(setResp.NotCreated) > 0 {
		t.Fatalf("notCreated: %+v", setResp.NotCreated)
	}
	contactID := setResp.Created["c1"]["id"].(string)

	// Update to remove the media entry.
	_, raw = f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			contactID: map[string]any{
				"media": nil, // JSON null removes the key
			},
		},
	})
	var updateResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &updateResp); err != nil {
		t.Fatalf("update unmarshal: %v", err)
	}
	if len(updateResp.NotUpdated) > 0 {
		t.Fatalf("notUpdated: %+v", updateResp.NotUpdated)
	}

	// Blob must be GC-eligible now.
	ctx := context.Background()
	_, refCount, err := f.srv.Store.Meta().GetBlobRef(ctx, blobID)
	if err != nil {
		t.Fatalf("GetBlobRef after update: %v", err)
	}
	if refCount != 0 {
		t.Fatalf("ref_count = %d after photo removed, want 0", refCount)
	}
}

// TestContactPhoto_URIOnly_NoValidation verifies that a media entry
// with only a `uri` (no blobId) round-trips without any blob validation
// or ref-counting.
func TestContactPhoto_URIOnly_NoValidation(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "URI")

	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "URI Photo"},
				"media": map[string]any{
					"photo1": map[string]any{
						"@type": "MediaResource",
						"kind":  "photo",
						"uri":   "https://example.test/photo.jpg",
					},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(setResp.NotCreated) > 0 {
		t.Fatalf("notCreated: %+v", setResp.NotCreated)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("created = %d, want 1", len(setResp.Created))
	}
}
