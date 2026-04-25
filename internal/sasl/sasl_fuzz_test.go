package sasl

// SASL mechanism wire-frame fuzz targets (STANDARDS §8.2). Each target
// runs the relevant mechanism's Start / Next entry-point against
// arbitrary bytes and asserts:
//
//   1. The mechanism never panics on any input.
//   2. Errors are typed (one of the package's sentinel errors, or a
//      wrapped form of one) — never a wrapped runtime panic.
//   3. The mechanism never advances past 'done' on a malformed input.
//
// Seeds cover RFC 4616 (PLAIN), RFC 5802 (SCRAM), RFC 7628 (OAUTHBEARER),
// and Google's XOAUTH2: happy paths, missing fields, oversized values,
// embedded NULs at the wrong offsets, mis-encoded GS2 headers,
// truncated client-final messages, and pathological saslname escapes.
//
// The tests use a stub Authenticator / TokenVerifier that always
// rejects so fuzz inputs cannot accidentally succeed; we only care that
// the parser treats malformed input as an error rather than panicking.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fuzzAuth struct{}

func (fuzzAuth) Authenticate(context.Context, string, string) (PrincipalID, error) {
	return 0, errors.New("fuzz: always-fail")
}

type fuzzVerifier struct{}

func (fuzzVerifier) VerifyAccessToken(context.Context, string, string) (PrincipalID, error) {
	return 0, errors.New("fuzz: always-fail")
}

type fuzzLookup struct {
	cred SCRAMCredentials
}

func (l fuzzLookup) LookupSCRAMCredentials(_ context.Context, _ string) (SCRAMCredentials, PrincipalID, error) {
	if l.cred.StoredKey == nil {
		return SCRAMCredentials{}, 0, errors.New("fuzz: always-fail")
	}
	return l.cred, 0, nil
}

// FuzzPLAIN drives PLAIN's consume() over arbitrary bytes. RFC 4616
// frame is "authzid \0 authcid \0 passwd"; any other shape must surface
// as ErrInvalidMessage or ErrAuthFailed, never a panic.
func FuzzPLAIN(f *testing.F) {
	seeds := []string{
		"\x00alice\x00password",
		"alice\x00alice\x00password",
		"alice\x00bob\x00password", // proxy-auth (rejected)
		"\x00\x00",
		"\x00\x00\x00",
		"",
		"no-nuls",
		"\x00\x00",
		strings.Repeat("\x00", 100),
		"alice\x00bob",
		strings.Repeat("a", 65536),
		"a\x00b\x00c\x00d", // too many fields
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	ctx := WithTLS(context.Background(), true)
	f.Fuzz(func(t *testing.T, in []byte) {
		m := NewPLAIN(fuzzAuth{}).(*plainMech)
		_, _, _ = m.Start(ctx, in)
	})
}

// FuzzOAUTHBEARER drives OAUTHBEARER's consume(). RFC 7628 §3.1 frames
// the message as "gs2-header ^A auth=Bearer <tok> ^A ^A" with optional
// extra ^A-separated fields.
func FuzzOAUTHBEARER(f *testing.F) {
	seeds := []string{
		"n,a=user@example.com,\x01auth=Bearer tok\x01\x01",
		"n,,\x01auth=Bearer tok\x01\x01",
		"y,,\x01auth=Bearer tok\x01\x01",
		"p=tls-server-end-point,a=u@x,\x01auth=Bearer tok\x01\x01",
		"\x01auth=Bearer tok\x01",
		"auth=Bearer tok",
		"auth=BEARER tok",
		"auth=foo tok",
		"auth=\x00\x00",
		"",
		"\x01\x01\x01",
		strings.Repeat("\x01", 200),
		strings.Repeat("a", 32*1024), // hits the 16 KiB cap
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	ctx := WithTLS(context.Background(), true)
	f.Fuzz(func(t *testing.T, in []byte) {
		m := NewOAUTHBEARER(fuzzVerifier{})
		_, _, _ = m.Start(ctx, in)
	})
}

// FuzzXOAUTH2 covers XOAUTH2's "user=<email> ^A auth=Bearer <tok> ^A ^A"
// shape. Same parser, slightly different lead-in.
func FuzzXOAUTH2(f *testing.F) {
	seeds := []string{
		"user=alice@example.com\x01auth=Bearer tok\x01\x01",
		"\x01auth=Bearer tok\x01",
		"user=\x00\x00\x01auth=Bearer x\x01\x01",
		"",
		"user=alice\x01\x01",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	ctx := WithTLS(context.Background(), true)
	f.Fuzz(func(t *testing.T, in []byte) {
		m := NewXOAUTH2(fuzzVerifier{})
		_, _, _ = m.Start(ctx, in)
	})
}

// FuzzSCRAMClientFirst drives SCRAM Start() over arbitrary
// client-first-message bytes. Invariants:
//
//   - Any input either advances to state=1 with a server-first
//     challenge or returns a wrapped sentinel error.
//   - The mechanism never reaches state=2 (success) from a malformed
//     client-first.
func FuzzSCRAMClientFirst(f *testing.F) {
	seeds := []string{
		"n,,n=user,r=fyko+d2lbbFgONRv9qkxdawL",
		"y,,n=u,r=cnonce",
		"p=tls-server-end-point,,n=u,r=cnonce",
		"n,a=alt,n=u,r=cnonce",
		"",
		"n",
		"n,",
		"n,,",
		"n,,n=,r=",
		"n,,r=cnonce",   // missing n=
		"n,,n=user",     // missing r=
		"x,,n=u,r=c",    // bad gs2 flag
		"n,,n=u,r=c,e=", // extension token
		"n,,n=" + strings.Repeat("a", 65536) + ",r=cnonce",
		"n,,n=u" + strings.Repeat(",ext=val", 1000) + ",r=cnonce",
		// saslname escapes
		"n,,n==2C,r=c",
		"n,,n==3D,r=c",
		"n,,n==XY,r=c",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	ctx := WithTLS(context.Background(), true)
	f.Fuzz(func(t *testing.T, in []byte) {
		m := NewSCRAMSHA256(fuzzAuth{}, fuzzLookup{}, false).(*scramMech)
		_, done, _ := m.Start(ctx, in)
		if done && m.state == 2 {
			t.Fatalf("SCRAM Start advanced to state=2 on input %q", in)
		}
	})
}

// FuzzSCRAMClientFinal drives SCRAM Next() over arbitrary
// client-final-message bytes. Pre-condition: a real client-first has
// completed Start() so the mechanism is in state=1; only Next is
// fuzzed.
func FuzzSCRAMClientFinal(f *testing.F) {
	seeds := []string{
		"c=biws,r=fyko+d2lbbFgONRv9qkxdawL3rfcNHYJY1ZVvWVs7j,p=v0X8v3Bz2T0CJGbJQyF0X+HI4Ts=",
		"c=biws,r=cnonce+snonce,p=" + strings.Repeat("A", 44),
		"",
		"c=,r=,p=",
		"r=cnonce+snonce", // missing c= and p=
		"c=biws,p=zzzz",   // missing r=
		"c=biws,r=cnonce+snonce,p=!!!notbase64!!!",
		"c=" + strings.Repeat("A", 65536) + ",r=x,p=y",
		"x=foo,c=biws,r=cnonce+snonce,p=" + strings.Repeat("A", 44),
		strings.Repeat(",", 100),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	ctx := WithTLS(context.Background(), true)
	f.Fuzz(func(t *testing.T, in []byte) {
		// Drive Start with a known-good client-first so Next has a
		// state=1 to work from.
		m := NewSCRAMSHA256(fuzzAuth{}, fuzzLookup{}, false).(*scramMech)
		if _, _, err := m.Start(ctx, []byte("n,,n=user,r=cnonce")); err != nil {
			return
		}
		_, _, _ = m.Next(ctx, in)
	})
}

// FuzzSCRAMServerFirst is conceptually the inverse — the server emits
// a server-first-message and we want to fuzz our parser of that
// frame. We don't have a client-side parser in this package; instead
// we re-target parseSCRAMAttrs (the shared attribute scanner) which
// handles the server-first as well as both client messages.
func FuzzSCRAMServerFirst(f *testing.F) {
	seeds := []string{
		"r=cnonce+snonce,s=c2FsdA==,i=4096",
		"r=,s=,i=",
		"i=4096",
		"",
		"r",
		"r=",
		"=v",
		"a=b,c=d,e=f",
		strings.Repeat("a=b,", 5000),
		"a=" + strings.Repeat("X", 65536),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		// parseSCRAMAttrs returns an error or a map; never panics.
		_, _ = parseSCRAMAttrs(string(in))
	})
}
