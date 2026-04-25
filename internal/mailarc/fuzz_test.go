package mailarc_test

import (
	"context"
	"testing"
)

// FuzzVerify feeds arbitrary bytes into the ARC verifier. The verifier
// MUST NOT panic; malformed input surfaces as AuthFail with a reason.
func FuzzVerify(f *testing.F) {
	// The structural-only chain seeds are kept so the fuzzer continues
	// to exercise the parse/structure path. Crypto-verify on these
	// seeds returns AuthTempError (no key in the empty fakedns) which
	// is itself a structurally-valid Verify outcome, so the fuzzer's
	// "must not panic" invariant holds.
	const singleHopSeed = "ARC-Seal: i=1; a=rsa-sha256; cv=none; d=example.com; s=s1; b=AAAA\r\n" +
		"ARC-Message-Signature: i=1; a=rsa-sha256; c=relaxed/relaxed; d=example.com; s=s1; h=from:to:subject; bh=AAAA; b=BBBB\r\n" +
		"ARC-Authentication-Results: i=1; mx.example.com; spf=pass\r\n" +
		"From: a@b\r\n\r\nbody\r\n"
	const twoHopSeed = "ARC-Seal: i=1; a=rsa-sha256; cv=none; d=example.com; s=s1; b=AAAA\r\n" +
		"ARC-Message-Signature: i=1; a=rsa-sha256; c=relaxed/relaxed; d=example.com; s=s1; h=from; bh=AAAA; b=BBBB\r\n" +
		"ARC-Authentication-Results: i=1; mx.example.com; spf=pass\r\n" +
		"ARC-Seal: i=2; a=rsa-sha256; cv=pass; d=example.net; s=s2; b=CCCC\r\n" +
		"ARC-Message-Signature: i=2; a=rsa-sha256; c=relaxed/relaxed; d=example.net; s=s2; h=from; bh=AAAA; b=DDDD\r\n" +
		"ARC-Authentication-Results: i=2; mx.example.net; arc=pass\r\n" +
		"From: a@b\r\n\r\nbody\r\n"
	seeds := []string{
		"",
		"From: a@b\r\n\r\nhi\r\n",
		singleHopSeed,
		twoHopSeed,
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
