package maildkim_test

// activity_test.go verifies that every log record emitted by maildkim carries
// a valid activity attribute (REQ-OPS-86a).

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
)

// TestVerify_TruncatedSignatures_ActivityTagged exercises the cap-hit path so
// the "truncated signatures above cap" warn record is emitted; asserts it
// carries a valid activity tag (internal per the implementor brief).
func TestVerify_TruncatedSignatures_ActivityTagged(t *testing.T) {
	// Build a message with two DKIM-Signature headers but cap the verifier at
	// one. The cap fires and the warn record is the one we want to inspect.
	const fakeSig = "DKIM-Signature: v=1; a=rsa-sha256; s=fake; d=spam.example;" +
		" h=From; bh=AAAA; b=BBBB\r\n"
	raw := []byte(fakeSig + fakeSig + "From: a@b\r\nTo: c@d\r\n\r\nhi\r\n")

	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		dns := fakedns.New()
		v := maildkim.New(
			fakeResolver{inner: dns},
			log,
			clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			maildkim.WithMaxVerifications(1),
		)
		_, _ = v.Verify(context.Background(), raw)
	})
}

// TestVerify_NormalPath_ActivityTagged confirms that a normal verify run emits
// no untagged records.
func TestVerify_NormalPath_ActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		dns := fakedns.New()
		dns.AddTXT("brisbane._domainkey.example.com", publicKeyBrisbane)
		v := maildkim.New(
			fakeResolver{inner: dns},
			log,
			clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		)
		_, _ = v.Verify(context.Background(), []byte(verifiedMailCRLF))
	})
}
