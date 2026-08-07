package storepg

import "github.com/jackc/pgx/v5/pgxpool"

// Pool exposes the underlying *pgxpool.Pool of a Store for white-box
// tests that need raw SQL access (e.g. seeding a pre-migration row
// shape) without going through the store.Store interface.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Migration0005SQL is the verbatim 0005_state_change_generic.sql body,
// re-exported for the migration-mapping test in storepg_test.go so the
// test exercises the production migration text and never drifts from
// it.
var Migration0005SQL = func() string {
	body, err := migrationsFS.ReadFile("migrations/0005_state_change_generic.sql")
	if err != nil {
		panic(err)
	}
	return string(body)
}()

// Migration0070SQL is the verbatim 0070_rethread_same_msgid.sql body,
// re-exported for the migration test in storepg_test.go.
var Migration0070SQL = func() string {
	body, err := migrationsFS.ReadFile("migrations/0070_rethread_same_msgid.sql")
	if err != nil {
		panic(err)
	}
	return string(body)
}()

// Migration0100SQL is the verbatim 0100_messages_failed_image_signal.sql
// body, re-exported so TestMigration0100RetryableBackfill can assert
// Migration0100BackfillSQL below is not a hand-copied statement that
// has drifted from the real migration file.
var Migration0100SQL = func() string {
	body, err := migrationsFS.ReadFile("migrations/0100_messages_failed_image_signal.sql")
	if err != nil {
		panic(err)
	}
	return string(body)
}()

// Migration0100BackfillSQL is the verbatim back-fill UPDATE statement
// from 0100_messages_failed_image_signal.sql (issue #271), re-exported
// for TestMigration0100RetryableBackfill in storepg_test.go. Only the
// UPDATE is exported (not the whole file): the ALTER TABLE statements
// that precede it cannot be re-run against a DB that already has the
// columns, which every test DB does once storepg.Open has run all
// migrations once.
const Migration0100BackfillSQL = `UPDATE messages SET retryable_failed_image_count = failed_image_count WHERE failed_image_count > 0`

// Migration0101SQL is the verbatim 0101_snooze_wake_destination.sql
// body, re-exported so TestMigration0101WakeMailboxBackfill can assert
// Migration0101BackfillSQL below is not a hand-copied statement that
// has drifted from the real migration file.
var Migration0101SQL = func() string {
	body, err := migrationsFS.ReadFile("migrations/0101_snooze_wake_destination.sql")
	if err != nil {
		panic(err)
	}
	return string(body)
}()

// Migration0101BackfillSQL is the verbatim back-fill UPDATE statement
// from 0101_snooze_wake_destination.sql (issue #274), re-exported for
// TestMigration0101WakeMailboxBackfill in storepg_test.go. Only the
// UPDATE is exported (not the whole file): the ALTER TABLE statement
// that precedes it cannot be re-run against a DB that already has the
// column, which every test DB does once storepg.Open has run all
// migrations once.
const Migration0101BackfillSQL = `UPDATE message_mailboxes
   SET wake_mailbox_id = (
         SELECT mb.id
           FROM mailboxes mb
          WHERE mb.principal_id = (
                  SELECT m.principal_id FROM messages m WHERE m.id = message_mailboxes.message_id
                )
            AND ((mb.attributes & 1) = 1 OR UPPER(mb.name) = 'INBOX')
          ORDER BY (mb.attributes & 1) DESC, mb.id ASC
          LIMIT 1
       )
 WHERE snoozed_until_us IS NOT NULL`

// TruncateAll moved to testseam.go (regular build) so external test
// packages (e.g. test/e2e/fixtures) can call it across package
// boundaries. _test.go files are not visible to importers.
