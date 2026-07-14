package storesqlite

import (
	"context"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// ForceCredentialForTest writes passwordHash/totpSecret directly onto the
// principals row identified by id, bypassing UpdatePrincipal's
// REQ-SUBACCT-02 guard (sub-principal cannot carry a credential).
//
// This is a TEST-ONLY helper — it is in a regular (non-_test) build file
// because external test packages (internal/directory, internal/protologin,
// internal/sasl) need to construct a sub-principal row that carries a
// verifiably-correct credential in order to prove that the auth layer
// (Directory.Authenticate and everything built on it) rejects a
// sub-principal even when the row is credentialed -- a defense-in-depth
// property that is otherwise unreachable through any store API, since
// both InsertSubPrincipal and UpdatePrincipal refuse to persist a
// credential on a sub-principal. It is intentionally not on the
// store.Store interface, so production code cannot reach it without an
// explicit *storesqlite.Store import + type assertion.
func (s *Store) ForceCredentialForTest(ctx context.Context, id store.PrincipalID, passwordHash string, totpSecret []byte) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()

	now := usMicros(s.clock.Now().UTC())
	res, err := s.db.ExecContext(ctx, `
		UPDATE principals
		   SET password_hash = ?, totp_secret = ?, updated_at_us = ?
		 WHERE id = ?`,
		passwordHash, totpSecret, now, int64(id))
	if err != nil {
		return fmt.Errorf("storesqlite: ForceCredentialForTest: %w", mapErr(err))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storesqlite: ForceCredentialForTest: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("storesqlite: ForceCredentialForTest: %w", store.ErrNotFound)
	}
	return nil
}
