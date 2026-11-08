package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// runLoopbackQueueOrigin (re #559): the loopback deliverer ingests a
// queue row as an authenticated local submission only when the submitting
// path marked it; a forwarded or list-expanded row (marker unset) reaches
// IngestBytes as inbound mail. The marker is read back from the
// persisted queue row.
func runLoopbackQueueOrigin(t *testing.T, st store.Store) {
	t.Helper()
	f := newLoopbackQueueFixture(t, st)
	for i, c := range []struct {
		name string
		auth bool
	}{
		{"authenticated submission", true},
		{"forwarded or list-expanded copy", false},
	} {
		envID, err := f.q.Submit(f.ctx, queue.Submission{
			MailFrom:           "sender@elsewhere.test",
			Recipients:         []string{"alice@example.local"},
			Body:               strings.NewReader("Subject: origin\r\n\r\nbody\r\n"),
			AuthenticatedLocal: c.auth,
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
		if got := f.ing.last.LocalSubmission; got != c.auth {
			t.Errorf("%s: LocalSubmission = %v, want %v", c.name, got, c.auth)
		}
	}
}

func TestQueueLoopback_Origin(t *testing.T) {
	runLoopbackQueueOrigin(t, openSQLiteStore(t))
}

func TestQueueLoopback_Origin_Postgres(t *testing.T) {
	runLoopbackQueueOrigin(t, openPostgresStoreForLoopback(t))
}
