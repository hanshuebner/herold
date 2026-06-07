package admin

// imap_import_adapter.go — bridge between *imapimport.Pool and
// protoadmin.IMAPImportStatusProvider (REQ-IMAP-IMP-65, wave 5).
//
// The imapimport and protoadmin packages cannot import each other (protoadmin
// is imported by imapimport's tests, and imapimport must not import protoadmin
// to avoid a cycle). This tiny adapter in the admin package sits between them:
// it calls pool.Snapshot(), copies the fields field-for-field into the
// protoadmin wire type, and returns the result.

import (
	"github.com/hanshuebner/herold/internal/imapimport"
	"github.com/hanshuebner/herold/internal/protoadmin"
)

// imapImportPoolStatusAdapter adapts *imapimport.Pool to the
// protoadmin.IMAPImportStatusProvider interface. The admin server stores this
// as IMAPImportStatusProvider so handlers can call Snapshot() without
// depending on imapimport directly.
type imapImportPoolStatusAdapter struct {
	pool *imapimport.Pool
}

// Snapshot calls pool.Snapshot() and copies every WorkerStatus field into the
// equivalent protoadmin.IMAPImportWorkerStatus. The copy is field-for-field;
// the two structs are defined to be identical in shape (see protoadmin/server.go
// and imapimport/status.go). This is checked at compile time by the
// field assignments below — a mismatch causes a type error.
func (a imapImportPoolStatusAdapter) Snapshot() []protoadmin.IMAPImportWorkerStatus {
	src := a.pool.Snapshot()
	out := make([]protoadmin.IMAPImportWorkerStatus, len(src))
	for i, s := range src {
		out[i] = protoadmin.IMAPImportWorkerStatus{
			AccountID:           s.AccountID,
			PrincipalID:         s.PrincipalID,
			AccountName:         s.AccountName,
			Host:                s.Host,
			Phase:               s.Phase,
			CurrentFolder:       s.CurrentFolder,
			ConnMode:            s.ConnMode,
			Connected:           s.Connected,
			PhaseSince:          s.PhaseSince,
			LastSyncAt:          s.LastSyncAt,
			ConsecutiveFailures: s.ConsecutiveFailures,
			NextPollAt:          s.NextPollAt,
			MessagesFetched:     s.MessagesFetched,
			FlagsPropagated:     s.FlagsPropagated,
			LastError:           s.LastError,
		}
	}
	return out
}
