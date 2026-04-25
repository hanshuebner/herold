package autodns_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/autodns"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/store"
)

func TestBuildDKIMRecord_RoundTrip(t *testing.T) {
	const pubB64 = "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAex0plus" +
		"randompaddingZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	rec, err := autodns.BuildDKIMRecord(store.DKIMAlgorithmRSASHA256, pubB64)
	if err != nil {
		t.Fatalf("BuildDKIMRecord: %v", err)
	}
	parsed, err := autodns.ParseDKIMRecord(rec)
	if err != nil {
		t.Fatalf("ParseDKIMRecord: %v", err)
	}
	if parsed.Version != "DKIM1" {
		t.Fatalf("Version: got %q want %q", parsed.Version, "DKIM1")
	}
	if parsed.KeyType != "rsa" {
		t.Fatalf("KeyType: got %q want %q", parsed.KeyType, "rsa")
	}
	if parsed.PublicKey != pubB64 {
		t.Fatalf("PublicKey: got %q want %q", parsed.PublicKey, pubB64)
	}
}

func TestBuildDKIMRecord_TXTChunking(t *testing.T) {
	// Construct a key string longer than 255 bytes so SegmentTXT will
	// chunk it.
	long := strings.Repeat("A", 600)
	rec, err := autodns.BuildDKIMRecord(store.DKIMAlgorithmRSASHA256, long)
	if err != nil {
		t.Fatalf("BuildDKIMRecord: %v", err)
	}
	if len(rec) <= 255 {
		t.Fatalf("expected long record, got %d bytes", len(rec))
	}
	chunks := autodns.SegmentTXT(rec)
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 255 {
			t.Fatalf("chunk %d size %d exceeds 255", i, len(c))
		}
	}
	if joined := strings.Join(chunks, ""); joined != rec {
		t.Fatalf("rejoined chunks differ from original")
	}
	// A short record fits in one chunk.
	short, err := autodns.BuildDKIMRecord(store.DKIMAlgorithmEd25519SHA256, "shortkey==")
	if err != nil {
		t.Fatalf("short BuildDKIMRecord: %v", err)
	}
	if got := autodns.SegmentTXT(short); len(got) != 1 || got[0] != short {
		t.Fatalf("short record chunking: got %v", got)
	}
}

func TestBuildMTASTSPolicy_PolicyTextShape(t *testing.T) {
	policy := autodns.MTASTSPolicy{
		Mode:          autodns.MTASTSModeEnforce,
		MX:            []string{"mx1.example.test", "mx2.example.test"},
		MaxAgeSeconds: 86400,
	}
	const fakeNow int64 = 1735689600 // 2025-01-01T00:00:00Z

	txt1, body1, err := autodns.BuildMTASTSPolicy(policy, fakeNow)
	if err != nil {
		t.Fatalf("BuildMTASTSPolicy: %v", err)
	}
	wantTXT := "v=STSv1; id=" + strconv.FormatInt(fakeNow, 10)
	if txt1 != wantTXT {
		t.Fatalf("TXT: got %q want %q", txt1, wantTXT)
	}
	wantBody := "version: STSv1\n" +
		"mode: enforce\n" +
		"mx: mx1.example.test\n" +
		"mx: mx2.example.test\n" +
		"max_age: 86400\n"
	if body1 != wantBody {
		t.Fatalf("policy body shape mismatch:\ngot:\n%q\nwant:\n%q", body1, wantBody)
	}
	// Re-call with a different unix timestamp: the id must change.
	txt2, _, err := autodns.BuildMTASTSPolicy(policy, fakeNow+1)
	if err != nil {
		t.Fatalf("BuildMTASTSPolicy second: %v", err)
	}
	if txt2 == txt1 {
		t.Fatalf("policy id did not change for fresh timestamp")
	}
}

func TestBuildTLSRPTRecord_MultipleRua(t *testing.T) {
	rec, err := autodns.BuildTLSRPTRecord([]string{
		"mailto:tlsrpt@example.test",
		"https://reports.example.test/ingest",
	})
	if err != nil {
		t.Fatalf("BuildTLSRPTRecord: %v", err)
	}
	want := "v=TLSRPTv1; rua=mailto:tlsrpt@example.test,https://reports.example.test/ingest"
	if rec != want {
		t.Fatalf("got %q want %q", rec, want)
	}
}

func TestBuildDMARCRecord_AllFields(t *testing.T) {
	policy := autodns.DMARCPolicy{
		Policy:             mailauth.DMARCPolicyReject,
		HasSubdomainPolicy: true,
		SubdomainPolicy:    mailauth.DMARCPolicyQuarantine,
		RUA:                []string{"mailto:agg@example.test"},
		RUF:                []string{"mailto:fail@example.test"},
		Pct:                50,
		ADKIM:              autodns.DMARCAlignmentStrict,
		ASPF:               autodns.DMARCAlignmentRelaxed,
		FO:                 autodns.DMARCFailureOptions{FailDKIM: true, FailSPF: true},
	}
	rec, err := autodns.BuildDMARCRecord(policy)
	if err != nil {
		t.Fatalf("BuildDMARCRecord: %v", err)
	}
	tags := strings.Split(rec, "; ")
	// First tag must be v=DMARC1.
	if tags[0] != "v=DMARC1" {
		t.Fatalf("first tag: %q", tags[0])
	}
	// p= must precede sp=, sp= before rua=, etc. Check by index.
	want := []string{
		"v=DMARC1",
		"p=reject",
		"sp=quarantine",
		"rua=mailto:agg@example.test",
		"ruf=mailto:fail@example.test",
		"pct=50",
		"adkim=s",
		"aspf=r",
		"fo=d:s",
	}
	if len(tags) != len(want) {
		t.Fatalf("tag count: got %d want %d (%q)", len(tags), len(want), rec)
	}
	for i, tag := range tags {
		if tag != want[i] {
			t.Fatalf("tag %d: got %q want %q", i, tag, want[i])
		}
	}
}
