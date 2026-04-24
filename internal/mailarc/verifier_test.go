package mailarc_test

import (
	"context"
	"net"
	"testing"

	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
)

type fakeResolver struct{ inner *fakedns.Resolver }

func (f fakeResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	v, err := f.inner.LookupTXT(ctx, name)
	if err != nil {
		return nil, mailauth.ErrNoRecords
	}
	return v, nil
}
func (f fakeResolver) MXLookup(ctx context.Context, _ string) ([]*net.MX, error) {
	return nil, mailauth.ErrNoRecords
}
func (f fakeResolver) IPLookup(ctx context.Context, _ string) ([]net.IP, error) {
	return nil, mailauth.ErrNoRecords
}

func newVerifier() *mailarc.Verifier {
	return mailarc.New(fakeResolver{inner: fakedns.New()})
}

// singleHopCVNone is a single-instance ARC set where cv=none — the
// value RFC 8617 §5.1.1 requires for the first seal in a chain.
const singleHopCVNone = "ARC-Seal: i=1; a=rsa-sha256; cv=none; d=example.com; s=s1;\r\n" +
	" b=AAAA\r\n" +
	"ARC-Message-Signature: i=1; a=rsa-sha256; c=relaxed/relaxed; d=example.com;\r\n" +
	" s=s1; h=from:to:subject; bh=AAAA; b=BBBB\r\n" +
	"ARC-Authentication-Results: i=1; mx.example.com; spf=pass\r\n" +
	"From: alice@example.com\r\n" +
	"To: bob@example.net\r\n" +
	"Subject: hello\r\n" +
	"\r\n" +
	"body\r\n"

// twoHopPass extends the chain with i=2 cv=pass — meaning the second
// intermediary verified the first hop and sealed its own set.
const twoHopPass = "ARC-Seal: i=1; a=rsa-sha256; cv=none; d=example.com; s=s1; b=AAAA\r\n" +
	"ARC-Message-Signature: i=1; a=rsa-sha256; c=relaxed/relaxed; d=example.com; s=s1; h=from:to:subject; bh=AAAA; b=BBBB\r\n" +
	"ARC-Authentication-Results: i=1; mx.example.com; spf=pass\r\n" +
	"ARC-Seal: i=2; a=rsa-sha256; cv=pass; d=example.net; s=s2; b=CCCC\r\n" +
	"ARC-Message-Signature: i=2; a=rsa-sha256; c=relaxed/relaxed; d=example.net; s=s2; h=from:to:subject; bh=AAAA; b=DDDD\r\n" +
	"ARC-Authentication-Results: i=2; mx.example.net; arc=pass\r\n" +
	"From: alice@example.com\r\nTo: bob@example.net\r\nSubject: hello\r\n\r\nbody\r\n"

// cvFailMessage carries cv=fail on the last seal.
const cvFailMessage = "ARC-Seal: i=1; a=rsa-sha256; cv=none; d=example.com; s=s1; b=AAAA\r\n" +
	"ARC-Message-Signature: i=1; a=rsa-sha256; d=example.com; s=s1; h=from; bh=AAAA; b=BBBB\r\n" +
	"ARC-Authentication-Results: i=1; mx.example.com; spf=pass\r\n" +
	"ARC-Seal: i=2; a=rsa-sha256; cv=fail; d=example.net; s=s2; b=CCCC\r\n" +
	"ARC-Message-Signature: i=2; a=rsa-sha256; d=example.net; s=s2; h=from; bh=AAAA; b=DDDD\r\n" +
	"ARC-Authentication-Results: i=2; mx.example.net; arc=fail\r\n" +
	"From: alice@example.com\r\n\r\nbody\r\n"

func TestVerify_NoHeaders(t *testing.T) {
	v := newVerifier()
	r, err := v.Verify(context.Background(), []byte("From: a@b\r\n\r\nhi\r\n"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthNone {
		t.Fatalf("status = %v want none", r.Status)
	}
	if r.Chain != 0 {
		t.Errorf("chain = %d want 0", r.Chain)
	}
}

func TestVerify_SingleHopCVNone(t *testing.T) {
	v := newVerifier()
	r, err := v.Verify(context.Background(), []byte(singleHopCVNone))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthPass {
		t.Fatalf("status = %v want pass (reason=%q)", r.Status, r.Reason)
	}
	if r.Chain != 1 {
		t.Errorf("chain = %d want 1", r.Chain)
	}
}

func TestVerify_TwoHopPass(t *testing.T) {
	v := newVerifier()
	r, err := v.Verify(context.Background(), []byte(twoHopPass))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthPass {
		t.Fatalf("status = %v want pass (reason=%q)", r.Status, r.Reason)
	}
	if r.Chain != 2 {
		t.Errorf("chain = %d want 2", r.Chain)
	}
}

func TestVerify_CVFail(t *testing.T) {
	v := newVerifier()
	r, err := v.Verify(context.Background(), []byte(cvFailMessage))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail", r.Status)
	}
}

func TestVerify_InstanceGap(t *testing.T) {
	msg := "ARC-Seal: i=1; cv=none; d=e; s=s1; b=A\r\n" +
		"ARC-Message-Signature: i=1; d=e; s=s1; h=from; bh=A; b=B\r\n" +
		"ARC-Authentication-Results: i=1; mx; spf=pass\r\n" +
		"ARC-Seal: i=3; cv=pass; d=e; s=s3; b=C\r\n" +
		"ARC-Message-Signature: i=3; d=e; s=s3; h=from; bh=A; b=D\r\n" +
		"ARC-Authentication-Results: i=3; mx; arc=pass\r\n" +
		"From: a@b\r\n\r\nhi\r\n"
	r, err := newVerifier().Verify(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail (gap at i=2)", r.Status)
	}
}

func TestVerify_CVNoneAtI2Fails(t *testing.T) {
	msg := "ARC-Seal: i=1; cv=none; d=e; s=s1; b=A\r\n" +
		"ARC-Message-Signature: i=1; d=e; s=s1; h=from; bh=A; b=B\r\n" +
		"ARC-Authentication-Results: i=1; mx; spf=pass\r\n" +
		"ARC-Seal: i=2; cv=none; d=e; s=s2; b=C\r\n" +
		"ARC-Message-Signature: i=2; d=e; s=s2; h=from; bh=A; b=D\r\n" +
		"ARC-Authentication-Results: i=2; mx; arc=pass\r\n" +
		"From: a@b\r\n\r\nhi\r\n"
	r, err := newVerifier().Verify(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail (cv=none at i>1)", r.Status)
	}
}

func TestVerify_MissingMessageSignature(t *testing.T) {
	msg := "ARC-Seal: i=1; cv=none; d=e; s=s1; b=A\r\n" +
		"ARC-Authentication-Results: i=1; mx; spf=pass\r\n" +
		"From: a@b\r\n\r\nhi\r\n"
	r, err := newVerifier().Verify(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail (incomplete set)", r.Status)
	}
}
