package maillist_test

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/dsn"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

const bounceTestDSN = "From: MAILER-DAEMON@mx.example.net\r\n" +
	"To: list+bounce-XXXX@example.test\r\n" +
	"Subject: Undelivered Mail Returned to Sender\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status; boundary=\"B\"\r\n" +
	"\r\n" +
	"--B\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"Delivery failed.\r\n" +
	"\r\n" +
	"--B\r\n" +
	"Content-Type: message/delivery-status\r\n" +
	"\r\n" +
	"Reporting-MTA: dns; mx.example.net\r\n" +
	"\r\n" +
	"Final-Recipient: rfc822; member@example.net\r\n" +
	"Action: failed\r\n" +
	"Status: 5.1.1\r\n" +
	"Diagnostic-Code: smtp; 550 5.1.1 unknown user\r\n" +
	"\r\n" +
	"--B--\r\n"

// TestBounceProcessor_AttributesAndClassifies is the REQ-MLIST-51 e2e:
// a valid VERP token on a real DSN attributes to the correct member
// with the correct classification.
func TestBounceProcessor_AttributesAndClassifies(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ml := mustInsertList(t, st, "list@example.test", false)
		member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

		ts := maillist.NewTokenSigner(testDataKey())
		token, err := ts.Sign(maillist.TokenPurposeVERP, ml.ID, member.ID, time.Time{})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}

		bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, clock.NewFake(time.Now()), discardLogger())
		res, err := bp.ProcessBounce(context.Background(), maillist.BounceInput{
			List:  ml,
			Token: token,
			Raw:   []byte(bounceTestDSN),
		})
		if err != nil {
			t.Fatalf("ProcessBounce: %v", err)
		}
		if !res.Attributed {
			t.Fatalf("Attributed = false, want true")
		}
		if res.MemberID != member.ID {
			t.Fatalf("MemberID = %d, want %d", res.MemberID, member.ID)
		}
		if res.Classification != dsn.ClassificationHard {
			t.Fatalf("Classification = %v, want Hard", res.Classification)
		}
		if len(res.Report.Recipients) != 1 || res.Report.Recipients[0].FinalRecipient != "member@example.net" {
			t.Fatalf("Report.Recipients = %+v, want one entry for member@example.net", res.Report.Recipients)
		}
	})
}

// TestBounceProcessor_UnverifiableToken_NoAttribution exercises the
// REQ-MLIST-52 fail-closed contract: a tampered/forged token produces
// Attributed=false and a zero MemberID, never a guessed attribution,
// and ProcessBounce itself does not error (an unverifiable bounce token
// is an expected inbound event, not a processing failure).
func TestBounceProcessor_UnverifiableToken_NoAttribution(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, clock.NewFake(time.Now()), discardLogger())
	res, err := bp.ProcessBounce(context.Background(), maillist.BounceInput{
		List:  ml,
		Token: "forged-not-a-real-token",
		Raw:   []byte(bounceTestDSN),
	})
	if err != nil {
		t.Fatalf("ProcessBounce: %v", err)
	}
	if res.Attributed {
		t.Fatalf("Attributed = true for a forged token, want false")
	}
	if res.MemberID != 0 {
		t.Fatalf("MemberID = %d for an unattributed result, want 0", res.MemberID)
	}

	logs, lerr := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "maillist.bounce.unverified"})
	if lerr != nil {
		t.Fatalf("ListAuditLog: %v", lerr)
	}
	if len(logs) == 0 {
		t.Fatalf("no maillist.bounce.unverified audit row for the unverifiable token")
	}
}

// TestBounceProcessor_CrossListToken_NoAttribution exercises
// REQ-MLIST-52 for a token minted for a DIFFERENT list: it must not be
// accepted just because the member id happens to collide.
func TestBounceProcessor_CrossListToken_NoAttribution(t *testing.T) {
	st := openSQLiteStore(t)
	mlA := mustInsertList(t, st, "list-a@example.test", false)
	mlB := mustInsertList(t, st, "list-b@example.test", false)
	memberA := mustAddExternalMember(t, st, mlA.ID, "member@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	tokenForA, err := ts.Sign(maillist.TokenPurposeVERP, mlA.ID, memberA.ID, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, clock.NewFake(time.Now()), discardLogger())
	res, err := bp.ProcessBounce(context.Background(), maillist.BounceInput{
		List:  mlB,
		Token: tokenForA,
		Raw:   []byte(bounceTestDSN),
	})
	if err != nil {
		t.Fatalf("ProcessBounce: %v", err)
	}
	if res.Attributed {
		t.Fatalf("Attributed = true for a cross-list token, want false")
	}
}

// TestBounceProcessor_NoTokenSigner_NoAttribution exercises the
// deployment-not-configured-yet case: no TokenSigner wired at all still
// degrades to "no attribution" rather than panicking or erroring.
func TestBounceProcessor_NoTokenSigner_NoAttribution(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)

	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), nil, clock.NewFake(time.Now()), discardLogger())
	res, err := bp.ProcessBounce(context.Background(), maillist.BounceInput{
		List:  ml,
		Token: "anything",
		Raw:   []byte(bounceTestDSN),
	})
	if err != nil {
		t.Fatalf("ProcessBounce: %v", err)
	}
	if res.Attributed {
		t.Fatalf("Attributed = true with no TokenSigner wired, want false")
	}
}

// TestBounceProcessor_MalformedDSN_StillAttributedButUnknown verifies
// that a valid token paired with a body that is not even a well-formed
// RFC 5322 message still attributes (the token verified), but
// classifies Unknown rather than erroring the whole call.
func TestBounceProcessor_MalformedDSN_StillAttributedButUnknown(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	token, err := ts.Sign(maillist.TokenPurposeVERP, ml.ID, member.ID, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, clock.NewFake(time.Now()), discardLogger())
	res, err := bp.ProcessBounce(context.Background(), maillist.BounceInput{
		List:  ml,
		Token: token,
		Raw:   []byte(""),
	})
	if err != nil {
		t.Fatalf("ProcessBounce: %v", err)
	}
	if !res.Attributed || res.MemberID != member.ID {
		t.Fatalf("result = %+v, want Attributed=true MemberID=%d", res, member.ID)
	}
	if res.Classification != dsn.ClassificationUnknown {
		t.Fatalf("Classification = %v, want Unknown for an unparsable body", res.Classification)
	}
}
