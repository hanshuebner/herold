package extimg

import (
	"strings"
	"testing"
)

// TestRewriteForPlaceholder_RealishHTML mirrors the body shape used
// by the running corpus: classic-computing.de avatar img inside a
// fully-fledged DOCTYPE/html/head/body wrapper. Confirms my
// RewriteForPlaceholder rewrites the src on a tag with multiple
// other attributes.
func TestRewriteForPlaceholder_RealishHTML(t *testing.T) {
	in := `<!DOCTYPE html><html style="padding:0"><head><meta http-equiv="Content-Type" content="text/html"></head><body><img src="https://forum.classic-computing.de/_data/x.webp" width="48" height="48" alt="" class="avatar" style="font-family: Arial">end</body></html>`
	got, err := RewriteForPlaceholder([]byte(in))
	if err != nil {
		t.Fatalf("RewriteForPlaceholder: %v", err)
	}
	s := string(got)
	if strings.Contains(s, "classic-computing.de") {
		t.Errorf("external host survived: %s", s)
	}
	if !strings.Contains(s, PlaceholderDataURI) {
		t.Errorf("placeholder absent: %s", s)
	}
}
