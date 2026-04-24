// Package corpus produces deterministic synthetic fixtures for the test
// harness: principals, mailboxes, messages. Every generator takes a seed
// and uses math/rand seeded from it; no wall-clock reads, no crypto/rand.
// Identical seeds produce identical output.
//
// The fixtures are deliberately predictable: emails are user000@example.test,
// mailboxes are the five standard SPECIAL-USE folders, message bodies are
// short English-ish plain text with fixed header shapes. Tests that need
// pathological inputs (very large, adversarial MIME) build their own.
package corpus

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// standardMailboxes lists the SPECIAL-USE folders Mailboxes generates.
var standardMailboxes = []struct {
	Name string
	Attr store.MailboxAttributes
}{
	{"INBOX", store.MailboxAttrInbox | store.MailboxAttrSubscribed},
	{"Sent", store.MailboxAttrSent | store.MailboxAttrSubscribed},
	{"Drafts", store.MailboxAttrDrafts},
	{"Trash", store.MailboxAttrTrash},
	{"Spam", store.MailboxAttrJunk},
}

// subjectWords is the word pool used to synthesize deterministic subjects.
var subjectWords = []string{
	"invoice", "meeting", "report", "update", "proposal",
	"budget", "schedule", "status", "review", "draft",
	"announcement", "reminder", "question", "feedback", "approval",
}

// bodyWords is the word pool for message bodies.
var bodyWords = []string{
	"the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog",
	"herold", "delivery", "mailbox", "queue", "message", "thread",
	"please", "review", "attached", "summary", "thanks", "regards",
}

// Principals returns n deterministic principals. CanonicalEmail is
// "user%03d@example.test" for consistent lexicographic ordering. All
// principals are PrincipalKindUser, unlimited quota, no flags.
func Principals(seed int64, n int) []store.Principal {
	r := rand.New(rand.NewSource(seed))
	out := make([]store.Principal, n)
	for i := 0; i < n; i++ {
		out[i] = store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: fmt.Sprintf("user%03d@example.test", i),
			DisplayName:    fmt.Sprintf("User %03d", i),
			// Deterministic pseudo-hash so two identical seeds produce
			// byte-equal rows; real code never uses a corpus hash for auth.
			PasswordHash: fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=4$seed%d$%d", seed, r.Int63()),
		}
	}
	return out
}

// Mailboxes returns the standard five SPECIAL-USE folders for each
// principal in principals. The returned slice carries MailboxID==0 rows
// (assigned by the store at insert); PrincipalID is populated.
//
// seed is accepted for API symmetry; currently unused because the mailbox
// shape is fixed, but reserved for when we randomise (e.g. add user-named
// subfolders).
func Mailboxes(seed int64, principals []store.Principal) []store.Mailbox {
	_ = seed
	var out []store.Mailbox
	for _, p := range principals {
		for _, m := range standardMailboxes {
			out = append(out, store.Mailbox{
				PrincipalID: p.ID,
				Name:        m.Name,
				Attributes:  m.Attr,
			})
		}
	}
	return out
}

// Messages returns n deterministic messages targeted at mailboxID. The
// caller populates MailboxID if different; this generator fills Subject,
// From, To, body Blob is left zero (callers put the rendered bytes into
// Blobs() themselves and set msg.Blob before InsertMessage).
//
// Bodies are short; Envelope is populated consistently with the rendered
// bytes so tests that consult the cached envelope see the same values the
// mailparse subsystem would derive.
func Messages(seed int64, mailboxID store.MailboxID, n int) []store.Message {
	r := rand.New(rand.NewSource(seed))
	out := make([]store.Message, n)
	// Anchor the InternalDate at a fixed instant so the output is
	// deterministic across runs; add i*minutes so messages sort in a
	// predictable order.
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		subject := fmt.Sprintf("%s %s %d",
			subjectWords[r.Intn(len(subjectWords))],
			subjectWords[r.Intn(len(subjectWords))],
			i,
		)
		from := fmt.Sprintf("sender%03d@remote.test", i%10)
		to := fmt.Sprintf("user%03d@example.test", i%5)
		body := generateBody(r, 12+r.Intn(24))
		size := approximateSize(subject, from, to, body)
		out[i] = store.Message{
			MailboxID:    mailboxID,
			Size:         size,
			InternalDate: anchor.Add(time.Duration(i) * time.Minute),
			ReceivedAt:   anchor.Add(time.Duration(i) * time.Minute),
			Envelope: store.Envelope{
				Subject:   subject,
				From:      from,
				To:        to,
				MessageID: fmt.Sprintf("msg-%d-%d@example.test", seed, i),
				Date:      anchor.Add(time.Duration(i) * time.Minute),
			},
		}
	}
	return out
}

// RenderRFC822 produces the RFC 5322 wire bytes for msg using the fields in
// msg.Envelope plus a synthesized body. Tests use this to populate the
// blob store alongside InsertMessage.
func RenderRFC822(seed int64, msg store.Message, index int) []byte {
	r := rand.New(rand.NewSource(seed + int64(index)))
	body := generateBody(r, 12+r.Intn(24))
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", msg.Envelope.From)
	fmt.Fprintf(&b, "To: %s\r\n", msg.Envelope.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Envelope.Subject)
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", msg.Envelope.MessageID)
	fmt.Fprintf(&b, "Date: %s\r\n", msg.Envelope.Date.Format(time.RFC1123Z))
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}

func generateBody(r *rand.Rand, words int) string {
	var b strings.Builder
	for i := 0; i < words; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(bodyWords[r.Intn(len(bodyWords))])
	}
	b.WriteByte('.')
	return b.String()
}

func approximateSize(subject, from, to, body string) int64 {
	// Rough header overhead + body; tests that need exact sizes call
	// RenderRFC822 and measure the rendered bytes.
	return int64(len(subject) + len(from) + len(to) + len(body) + 128)
}

// Seed inserts principals, mailboxes, and messages into s. Returns the
// inserted principals and mailboxes (with IDs assigned by the store); the
// messages list is returned keyed by mailbox for callers that want to
// assert specific rows.
//
// messageBodies are rendered into the blob store; msg.Blob and msg.Size
// are set to the real hash / length before InsertMessage. INBOX receives
// all messages (messagesPerMailbox applied to INBOX only) — the other
// mailboxes are created empty, which matches how IMAP tests expect the
// fixtures.
func Seed(
	t testing.TB,
	s store.Store,
	seed int64,
	principals, mailboxesPerPrincipal, messagesPerMailbox int,
) (insertedPrincipals []store.Principal, insertedMailboxes []store.Mailbox, insertedMessages []store.Message) {
	t.Helper()
	_ = mailboxesPerPrincipal // reserved; standard five folders are always created.
	ctx := context.Background()
	pRows := Principals(seed, principals)
	for i := range pRows {
		p, err := s.Meta().InsertPrincipal(ctx, pRows[i])
		if err != nil {
			t.Fatalf("corpus: insert principal %d: %v", i, err)
		}
		pRows[i] = p
		insertedPrincipals = append(insertedPrincipals, p)
	}
	mboxRows := Mailboxes(seed, pRows)
	for i := range mboxRows {
		mb, err := s.Meta().InsertMailbox(ctx, mboxRows[i])
		if err != nil {
			t.Fatalf("corpus: insert mailbox %d: %v", i, err)
		}
		mboxRows[i] = mb
		insertedMailboxes = append(insertedMailboxes, mb)
	}
	if messagesPerMailbox <= 0 {
		return insertedPrincipals, insertedMailboxes, nil
	}
	for _, mb := range mboxRows {
		if mb.Name != "INBOX" {
			continue
		}
		msgs := Messages(seed, mb.ID, messagesPerMailbox)
		for i, msg := range msgs {
			body := RenderRFC822(seed, msg, i)
			ref, err := s.Blobs().Put(ctx, strings.NewReader(string(body)))
			if err != nil {
				t.Fatalf("corpus: put blob: %v", err)
			}
			msg.Blob = ref
			msg.Size = ref.Size
			uid, modseq, err := s.Meta().InsertMessage(ctx, msg)
			if err != nil {
				t.Fatalf("corpus: insert message %d: %v", i, err)
			}
			msg.UID = uid
			msg.ModSeq = modseq
			insertedMessages = append(insertedMessages, msg)
		}
	}
	return insertedPrincipals, insertedMailboxes, insertedMessages
}
