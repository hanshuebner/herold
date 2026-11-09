package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// runLoopbackQueueSentBySelf: the queue hands the submitting principal
// and the interactive-submission marker to the loopback deliverer, which
// marks a local recipient SentBySelf only when the recipient is the
// submitter and the user sent the message interactively (JMAP
// EmailSubmission, SMTP submission). A send-API or server-generated
// self-send arrives unread (re #565).
func runLoopbackQueueSentBySelf(t *testing.T, st store.Store) {
	t.Helper()
	f := newLoopbackQueueFixture(t, st)
	alice := store.PrincipalID(f.alicePID)
	dir := directory.New(f.st.Meta(), nil, f.clk, nil)
	bobPID, err := dir.CreatePrincipal(f.ctx, "bob@example.local", "correct-horse-battery-2")
	if err != nil {
		t.Fatalf("CreatePrincipal bob: %v", err)
	}
	bob := store.PrincipalID(bobPID)

	cases := []struct {
		name        string
		submitter   *store.PrincipalID
		interactive bool
		want        bool
	}{
		{"interactive self-send", &alice, true, true},
		{"send API self-send", &alice, false, false},
		{"no submitting principal", nil, false, false},
		{"interactive send from another principal", &bob, true, false},
		{"send API from another principal", &bob, false, false},
	}
	for i, c := range cases {
		envID, err := f.q.Submit(f.ctx, queue.Submission{
			PrincipalID:           c.submitter,
			MailFrom:              "sender@example.local",
			Recipients:            []string{"alice@example.local"},
			Body:                  strings.NewReader("Subject: self\r\n\r\nbody\r\n"),
			AuthenticatedLocal:    true,
			InteractiveSubmission: c.interactive,
		})
		if err != nil {
			t.Fatalf("%s: Submit: %v", c.name, err)
		}
		if !loopbackQueueWaitFor(t, 5*time.Second, func() bool {
			rows, _ := f.st.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
			return len(rows) == 1 && rows[0].State == store.QueueStateDone
		}) {
			t.Fatalf("%s: queue row did not reach Done", c.name)
		}
		if f.ing.calls != i+1 {
			t.Fatalf("%s: IngestBytes calls = %d, want %d", c.name, f.ing.calls, i+1)
		}
		if got := f.ing.last.Recipients[0].SentBySelf; got != c.want {
			t.Errorf("%s: SentBySelf = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestQueueLoopback_SentBySelf(t *testing.T) {
	runLoopbackQueueSentBySelf(t, openSQLiteStore(t))
}

func TestQueueLoopback_SentBySelf_Postgres(t *testing.T) {
	runLoopbackQueueSentBySelf(t, openPostgresStoreForLoopback(t))
}
