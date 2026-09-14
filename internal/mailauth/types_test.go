package mailauth

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAuthStatusString(t *testing.T) {
	cases := map[AuthStatus]string{
		AuthUnknown:   "unknown",
		AuthPass:      "pass",
		AuthFail:      "fail",
		AuthSoftFail:  "softfail",
		AuthNeutral:   "neutral",
		AuthNone:      "none",
		AuthPolicy:    "policy",
		AuthTempError: "temperror",
		AuthPermError: "permerror",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("AuthStatus(%d).String()=%q want %q", int(s), got, want)
		}
	}
}

func TestAuthStatusJSONRoundTrip(t *testing.T) {
	for _, s := range []AuthStatus{
		AuthPass, AuthFail, AuthSoftFail, AuthNeutral, AuthNone,
		AuthPolicy, AuthTempError, AuthPermError,
	} {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal %v: %v", s, err)
		}
		var got AuthStatus
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != s {
			t.Errorf("round-trip mismatch: %v -> %s -> %v", s, b, got)
		}
	}
}

func TestDMARCPolicyJSONRoundTrip(t *testing.T) {
	for _, p := range []DMARCPolicy{DMARCPolicyNone, DMARCPolicyQuarantine, DMARCPolicyReject} {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal %v: %v", p, err)
		}
		var got DMARCPolicy
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != p {
			t.Errorf("round-trip mismatch: %v -> %s -> %v", p, b, got)
		}
	}
}

func TestDMARCDispositionJSONRoundTrip(t *testing.T) {
	for _, d := range []DMARCDisposition{DispositionNone, DispositionQuarantine, DispositionReject} {
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal %v: %v", d, err)
		}
		var got DMARCDisposition
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != d {
			t.Errorf("round-trip mismatch: %v -> %s -> %v", d, b, got)
		}
	}
}

func TestAuthResultsJSONRoundTrip(t *testing.T) {
	want := AuthResults{
		DKIM: []DKIMResult{
			{
				Status:     AuthPass,
				Domain:     "example.com",
				Selector:   "s1",
				Algorithm:  "ed25519-sha256",
				Identifier: "@example.com",
			},
			{
				Status: AuthFail,
				Domain: "example.net",
				Reason: "body hash mismatch",
			},
		},
		SPF: SPFResult{
			Status:   AuthPass,
			From:     "sender@example.com",
			HELO:     "mail.example.com",
			ClientIP: "192.0.2.1",
		},
		DMARC: DMARCResult{
			Status:      AuthPass,
			Policy:      DMARCPolicyReject,
			Disposition: DispositionNone,
			SPFAligned:  true,
			DKIMAligned: true,
			HeaderFrom:  "example.com",
			OrgDomain:   "example.com",
		},
		ARC: ARCResult{
			Status: AuthNone,
		},
		Raw: "example.com; spf=pass smtp.mailfrom=example.com",
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got AuthResults
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch\nwant: %+v\n got: %+v", want, got)
	}
}

func TestParseAuthResults(t *testing.T) {
	raw := "mx.netzhansa.com; spf=pass smtp.mailfrom=billing@mail.anthropic.com; " +
		"dkim=pass header.d=mail.anthropic.com header.s=s1; " +
		"dmarc=pass header.from=anthropic.com; " +
		"x-herold-spam=ham (score=0.10)"
	got, ok := ParseAuthResults(raw)
	if !ok {
		t.Fatalf("ParseAuthResults(%q) ok = false, want true", raw)
	}
	if got.SPF.Status != AuthPass {
		t.Errorf("SPF.Status = %v, want AuthPass", got.SPF.Status)
	}
	if len(got.DKIM) != 1 || got.DKIM[0].Status != AuthPass || got.DKIM[0].Domain != "mail.anthropic.com" || got.DKIM[0].Selector != "s1" {
		t.Errorf("DKIM = %+v", got.DKIM)
	}
	if got.DMARC.Status != AuthPass || got.DMARC.HeaderFrom != "anthropic.com" {
		t.Errorf("DMARC = %+v", got.DMARC)
	}
	if got.Raw != raw {
		t.Errorf("Raw = %q, want %q", got.Raw, raw)
	}
}

func TestParseAuthResults_NoRecognisedMethod(t *testing.T) {
	for _, raw := range []string{
		"",
		"garbage with no semicolon",
		"mail.example.com; x-herold-spam=ham (score=0.10)",
	} {
		if _, ok := ParseAuthResults(raw); ok {
			t.Errorf("ParseAuthResults(%q) ok = true, want false", raw)
		}
	}
}

func TestAuthStatusUnmarshalUnknown(t *testing.T) {
	// Forward-compat: an unknown token from a newer server should
	// round-trip to AuthUnknown rather than failing.
	var s AuthStatus
	if err := json.Unmarshal([]byte(`"future-value"`), &s); err != nil {
		t.Fatalf("unmarshal unknown: %v", err)
	}
	if s != AuthUnknown {
		t.Errorf("unknown token = %v; want AuthUnknown", s)
	}
}
