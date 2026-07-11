package storetest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// putContactBlob uploads bytes to the blob store and returns the hash and size.
func putContactBlob(t *testing.T, s store.Store, content string) (hash string, size int64) {
	t.Helper()
	ref, err := s.Blobs().Put(ctxT(t), strings.NewReader(content))
	if err != nil {
		t.Fatalf("putContactBlob: %v", err)
	}
	return ref.Hash, ref.Size
}

// makeContactJSON builds a minimal JSContact JSON body with a media
// blobId entry. When blobID is empty the media map is omitted.
func makeContactJSON(uid, blobID, mediaType string) []byte {
	m := map[string]any{
		"@type":   "Card",
		"version": "1.0",
		"uid":     uid,
	}
	if blobID != "" {
		m["media"] = map[string]any{
			"photo1": map[string]any{
				"@type":     "MediaResource",
				"kind":      "photo",
				"blobId":    blobID,
				"mediaType": mediaType,
			},
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("makeContactJSON: %v", err))
	}
	return b
}

// testContactPhotoBlobGCLiveness exercises REQ-CTS-02 and REQ-CTS-03:
//
//   - a blob referenced by a live contact is retained (IsContactPhotoBlobReferenced = true,
//     GetBlobRef.ref_count > 0)
//   - after the contact is destroyed the blob becomes eligible (ref_count -> 0,
//     IsContactPhotoBlobReferenced = false)
//   - a blob referenced by a second contact is NOT released while that
//     contact lives (shared-reference no-eager-delete guarantee)
func testContactPhotoBlobGCLiveness(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "contact-photo-gc@example.com")

	abID, err := s.Meta().InsertAddressBook(ctx, store.AddressBook{
		PrincipalID:  p.ID,
		Name:         "default",
		IsSubscribed: true,
		IsDefault:    true,
	})
	if err != nil {
		t.Fatalf("InsertAddressBook: %v", err)
	}

	hash, size := putContactBlob(t, s, "fake-photo-bytes-gc-test")

	// Before any contact: not referenced, ref_count 0.
	live, err := s.Meta().IsContactPhotoBlobReferenced(ctx, hash)
	if err != nil {
		t.Fatalf("IsContactPhotoBlobReferenced (before): %v", err)
	}
	if live {
		t.Fatal("IsContactPhotoBlobReferenced = true before any contact, want false")
	}

	// Create contact1 referencing the blob.
	uid1 := "urn:uuid:contact-gc-1"
	c1 := store.Contact{
		PrincipalID:   p.ID,
		AddressBookID: abID,
		UID:           uid1,
		JSContactJSON: makeContactJSON(uid1, hash, "image/jpeg"),
		DisplayName:   "GC Test 1",
		SearchBlob:    "gc test 1",
	}
	cid1, err := s.Meta().InsertContact(ctx, c1)
	if err != nil {
		t.Fatalf("InsertContact c1: %v", err)
	}
	// IncRef to mirror what the Contact/set handler does.
	if err := s.Meta().IncRefBlob(ctx, hash, size); err != nil {
		t.Fatalf("IncRefBlob c1: %v", err)
	}

	// Now referenced.
	live, err = s.Meta().IsContactPhotoBlobReferenced(ctx, hash)
	if err != nil {
		t.Fatalf("IsContactPhotoBlobReferenced (after c1): %v", err)
	}
	if !live {
		t.Fatal("IsContactPhotoBlobReferenced = false after c1 insert, want true")
	}
	_, refCount, err := s.Meta().GetBlobRef(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlobRef after c1: %v", err)
	}
	if refCount <= 0 {
		t.Fatalf("GetBlobRef.ref_count = %d after c1, want > 0", refCount)
	}

	// Create contact2 referencing the SAME blob.
	uid2 := "urn:uuid:contact-gc-2"
	c2 := store.Contact{
		PrincipalID:   p.ID,
		AddressBookID: abID,
		UID:           uid2,
		JSContactJSON: makeContactJSON(uid2, hash, "image/jpeg"),
		DisplayName:   "GC Test 2",
		SearchBlob:    "gc test 2",
	}
	cid2, err := s.Meta().InsertContact(ctx, c2)
	if err != nil {
		t.Fatalf("InsertContact c2: %v", err)
	}
	if err := s.Meta().IncRefBlob(ctx, hash, size); err != nil {
		t.Fatalf("IncRefBlob c2: %v", err)
	}

	_, refCount, err = s.Meta().GetBlobRef(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlobRef after c2: %v", err)
	}
	if refCount < 2 {
		t.Fatalf("GetBlobRef.ref_count = %d after c2, want >= 2", refCount)
	}

	// Destroy contact1: blob must still be referenced by contact2.
	if err := s.Meta().DeleteContact(ctx, cid1); err != nil {
		t.Fatalf("DeleteContact c1: %v", err)
	}
	if err := s.Meta().DecRefBlob(ctx, hash); err != nil {
		t.Fatalf("DecRefBlob c1: %v", err)
	}

	live, err = s.Meta().IsContactPhotoBlobReferenced(ctx, hash)
	if err != nil {
		t.Fatalf("IsContactPhotoBlobReferenced (after c1 destroy): %v", err)
	}
	if !live {
		t.Fatal("IsContactPhotoBlobReferenced = false after c1 destroy (c2 still lives), want true")
	}
	_, refCount, err = s.Meta().GetBlobRef(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlobRef after c1 destroy: %v", err)
	}
	if refCount <= 0 {
		t.Fatalf("GetBlobRef.ref_count = %d after c1 destroy, want > 0", refCount)
	}

	// Destroy contact2: blob becomes GC-eligible.
	if err := s.Meta().DeleteContact(ctx, cid2); err != nil {
		t.Fatalf("DeleteContact c2: %v", err)
	}
	if err := s.Meta().DecRefBlob(ctx, hash); err != nil {
		t.Fatalf("DecRefBlob c2: %v", err)
	}

	live, err = s.Meta().IsContactPhotoBlobReferenced(ctx, hash)
	if err != nil {
		t.Fatalf("IsContactPhotoBlobReferenced (after c2 destroy): %v", err)
	}
	if live {
		t.Fatal("IsContactPhotoBlobReferenced = true after all contacts destroyed, want false")
	}
	_, refCount, err = s.Meta().GetBlobRef(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlobRef after all destroyed: %v", err)
	}
	if refCount != 0 {
		t.Fatalf("GetBlobRef.ref_count = %d after all contacts destroyed, want 0", refCount)
	}
}
