package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// runLoopbackQueueSentBySelf: the queue hands the submitting principal
// to the loopback deliverer, which marks a local recipient SentBySelf
// exactly when that recipient is the submitter, so mail a principal
// sends to itself arrives read.
func runLoopbackQueueSentBySelf(t *testing.T, st store.Store) {
	t.Helper()
	f := newLoopbackQueueFixture(t, st)
	alice := store.PrincipalID(f.alicePID)

	cases := []struct {
		name      string
		submitter *store.PrincipalID
		want      bool
	}{
		{"submitted by the recipient", &alice, true},
		{"no submitting principal", nil, false},
	}
	for i, c := range cases {
		envID, err := f.q.Submit(f.ctx, queue.Submission{
			PrincipalID: c.submitter,
			MailFrom:    "alice@example.local",
			Recipients:  []string{"alice@example.local"},
			Body:        strings.NewReader("Subject: self\r\n\r\nbody\r\n"),
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
