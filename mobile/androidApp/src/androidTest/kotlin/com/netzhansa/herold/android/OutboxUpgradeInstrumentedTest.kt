package com.netzhansa.herold.android

import android.database.sqlite.SQLiteDatabase
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.outbox.NewOutboxEntry
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.store.DatabaseDriverFactory
import com.netzhansa.herold.shared.store.FileBlobFileStore
import com.netzhansa.herold.shared.store.SqlDelightLocalStore
import com.netzhansa.herold.shared.store.createDatabase
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * What an app update does to work that has not been sent (issue #420).
 * Three bug reports carrying crash traces were last seen queued on one
 * build and gone on the next, so the two ways an entry can leave without
 * being delivered are pinned here: a schema migration keeps it, and the
 * clear that goes with an account hands back what it took so the app can
 * say so.
 *
 * It drives the store directly, so it needs no server and no sign-in.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class OutboxUpgradeInstrumentedTest {

    private val context = InstrumentationRegistry.getInstrumentation().targetContext

    @Before
    fun dropAnyEarlierScratchStore() {
        context.deleteDatabase(DATABASE)
    }

    @After
    fun dropTheScratchStore() {
        context.deleteDatabase(DATABASE)
    }

    /**
     * A report queued by the build before this one is still queued after
     * the update: the store opens at schema 8, the app's own open runs
     * the migration to 9, and the entry reads back with everything it
     * carried.
     */
    @Test
    fun t93_anEntryQueuedOnTheOlderSchemaSurvivesTheUpgrade() {
        val path = context.getDatabasePath(DATABASE)
        path.parentFile?.mkdirs()
        val old = SQLiteDatabase.openOrCreateDatabase(path, null)
        try {
            // The schema as release 8 held it: the blob cache the
            // migration rewrites, and the outbox it does not touch.
            old.execSQL(V8_BLOB_CACHE)
            old.execSQL(V8_BLOB_CACHE_INDEX)
            old.execSQL(V8_OUTBOX)
            old.execSQL(V8_OUTBOX_INDEX)
            old.execSQL(
                "INSERT INTO outbox(accountId, kind, label, payload, revertJson, entityIds, " +
                    "createdAt, state, attempts, lastError, permanent, nextAttemptAt) " +
                    "VALUES ('acct-a', 'BUG_REPORT', ?, ?, NULL, '', 1700000000000, 'failed', 1, ?, 1, 0)",
                arrayOf(QUEUED_LABEL, QUEUED_PAYLOAD, QUEUED_ERROR),
            )
            old.version = 8
        } finally {
            old.close()
        }

        val store = SqlDelightLocalStore(
            database = createDatabase(DatabaseDriverFactory(context, DATABASE)),
            blobFiles = FileBlobFileStore(context),
            now = { System.currentTimeMillis() },
        )
        val entries = runBlocking { store.outboxList() }

        assertEquals("the queued report did not survive the upgrade", 1, entries.size)
        val entry = entries.single()
        assertEquals(OutboxKind.BUG_REPORT, entry.kind)
        assertEquals(QUEUED_LABEL, entry.label)
        assertEquals(QUEUED_PAYLOAD, entry.payload)
        assertEquals(QUEUED_ERROR, entry.lastError)
        assertEquals(1, entry.attempts)
        assertEquals(OutboxState.FAILED, entry.state)
        // The blob cache was rewritten by the same migration, so the
        // store is genuinely at the new schema.
        assertEquals(0L, runBlocking { store.blobCacheSize() })
    }

    /**
     * The one thing that does take unsent entries away - the clear that
     * runs when an account signs out or another principal signs in -
     * hands back what it dropped, so the app can name it in the log
     * instead of the entries going quiet.
     */
    @Test
    fun t94_theClearThatGoesWithAnAccountHandsBackWhatItDropped() {
        val store = SqlDelightLocalStore(
            database = createDatabase(DatabaseDriverFactory(context, DATABASE)),
            blobFiles = FileBlobFileStore(context),
            now = { System.currentTimeMillis() },
        )
        runBlocking {
            store.enqueueOutbox(
                NewOutboxEntry(
                    accountId = "acct-a",
                    kind = OutboxKind.BUG_REPORT,
                    label = QUEUED_LABEL,
                    payload = QUEUED_PAYLOAD,
                    createdAt = 1_700_000_000_000,
                ),
            )
            val dropped = store.clearAll()

            assertEquals("the clear said nothing about what it took", 1, dropped.size)
            assertEquals(QUEUED_LABEL, dropped.single().label)
            assertEquals(OutboxKind.BUG_REPORT, dropped.single().kind)
            assertTrue("the entries outlived the clear", store.outboxList().isEmpty())
        }
    }

    private companion object {
        const val DATABASE = "herold-outbox-upgrade-test.db"
        const val QUEUED_LABEL = "Bug report: the app closed when a new email arrived"
        const val QUEUED_PAYLOAD = "{\"accountId\":\"acct-a\",\"title\":\"the app closed\",\"parts\":[]}"
        const val QUEUED_ERROR = "the bug report was refused: unexpected part \"crash.txt\""

        const val V8_BLOB_CACHE = """
            CREATE TABLE blob_cache (
                accountId TEXT NOT NULL,
                blobId TEXT NOT NULL,
                contentType TEXT NOT NULL DEFAULT '',
                bytes BLOB NOT NULL,
                size INTEGER NOT NULL,
                lastUsedAt INTEGER NOT NULL,
                PRIMARY KEY (accountId, blobId)
            )
        """

        const val V8_BLOB_CACHE_INDEX = "CREATE INDEX blob_cache_lru ON blob_cache(lastUsedAt)"

        const val V8_OUTBOX = """
            CREATE TABLE outbox (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                accountId TEXT NOT NULL,
                kind TEXT NOT NULL,
                label TEXT NOT NULL DEFAULT '',
                payload TEXT NOT NULL,
                revertJson TEXT,
                entityIds TEXT NOT NULL DEFAULT '',
                createdAt INTEGER NOT NULL DEFAULT 0,
                state TEXT NOT NULL DEFAULT 'queued',
                attempts INTEGER NOT NULL DEFAULT 0,
                lastError TEXT,
                permanent INTEGER NOT NULL DEFAULT 0,
                nextAttemptAt INTEGER NOT NULL DEFAULT 0
            )
        """

        const val V8_OUTBOX_INDEX = "CREATE INDEX outbox_account ON outbox(accountId, id)"
    }
}
