package mailarc_test

import (
	"context"
	"testing"
)

// FuzzVerify feeds arbitrary bytes into the ARC verifier. The verifier
// MUST NOT panic; malformed input surfaces as AuthFail with a reason.
func FuzzVerify(f *testing.F) {
	seeds := []string{
		"",
		"From: a@b\r\n\r\nhi\r\n",
		singleHopCVNone,
		twoHopPass,
		cvFailMessage,
		"ARC-Seal: i=0; cv=none\r\n\r\nbody\r\n",
		"ARC-Seal: not-a-tag-list\r\n\r\nbody\r\n",
		"ARC-Seal: i=1;\r\nARC-Message-Signature: i=1;\r\n\r\nbody\r\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	v := newVerifier()
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		_, err := v.Verify(context.Background(), raw)
		if err != nil {
			t.Fatalf("unexpected internal error on fuzz input: %v", err)
		}
	})
}
