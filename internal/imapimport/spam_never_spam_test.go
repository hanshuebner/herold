package imapimport

// spam_never_spam_test.go covers REQ-FILT-02a / REQ-FLT-16 (issue #382) on
// the IMAP-import path: resolveImportSpamTarget honours
// Classification.DeliveryOverride by routing to INBOX with no "$Junk"
// keyword, the same outcome a ham verdict gets, regardless of the
// underlying Verdict. Sieve never runs on this path (REQ-IMAP-IMP-31); the
// override is decided upstream by internal/admin/imap_import_spam.go's
// Classify, which evaluates the principal's never-spam managed rules
// directly against the message (internal/sieve.NeverSpamOverrideLabel).

import (
	"testing"

	"github.com/hanshuebner/herold/internal/spam"
)

func TestResolveImportSpamTarget_NeverSpamOverride_SpamVerdict(t *testing.T) {
	target := resolveImportSpamTarget(spam.Classification{
		Verdict:          spam.Spam,
		DeliveryOverride: "filter:Trusted senders",
	})
	if target.mailbox != "INBOX" {
		t.Errorf("mailbox = %q, want INBOX", target.mailbox)
	}
	for _, kw := range target.keywords {
		if kw == "$Junk" {
			t.Errorf("keywords = %v, must not carry $Junk when overridden", target.keywords)
		}
	}
}

func TestResolveImportSpamTarget_NeverSpamOverride_SuspectVerdict(t *testing.T) {
	target := resolveImportSpamTarget(spam.Classification{
		Verdict:          spam.Suspect,
		DeliveryOverride: "filter:42",
	})
	if target.mailbox != "INBOX" {
		t.Errorf("mailbox = %q, want INBOX", target.mailbox)
	}
	for _, kw := range target.keywords {
		if kw == "$Junk" {
			t.Errorf("keywords = %v, must not carry $Junk when overridden", target.keywords)
		}
	}
}

func TestResolveImportSpamTarget_NoOverride_SpamStillGoesToJunk(t *testing.T) {
	target := resolveImportSpamTarget(spam.Classification{Verdict: spam.Spam})
	if target.mailbox != "Junk" {
		t.Errorf("mailbox = %q, want Junk (no override present)", target.mailbox)
	}
}

func TestResolveImportSpamTarget_NeverSpamOverride_CategoryPreserved(t *testing.T) {
	target := resolveImportSpamTarget(spam.Classification{
		Verdict:          spam.Suspect,
		Category:         "updates",
		DeliveryOverride: "filter:42",
	})
	found := false
	for _, kw := range target.keywords {
		if kw == "$category-updates" {
			found = true
		}
	}
	if !found {
		t.Errorf("keywords = %v, want $category-updates preserved under an override", target.keywords)
	}
}
