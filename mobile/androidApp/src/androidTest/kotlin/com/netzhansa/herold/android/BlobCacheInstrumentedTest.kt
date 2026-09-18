package com.netzhansa.herold.android

import android.database.sqlite.SQLiteBlobTooBigException
import android.database.sqlite.SQLiteDatabase
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.store.DatabaseDriverFactory
import com.netzhansa.herold.shared.store.FileBlobFileStore
import com.netzhansa.herold.shared.store.SqlDelightLocalStore
import com.netzhansa.herold.shared.store.createDatabase
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The blob cache against Android's cursor window (issue #420). The
 * platform hands a row to the client through a 2 MiB window, so an
 * inline image from a picture-heavy newsletter cannot travel in a row -
 * the first check states that limit against the layout the cache used
 * to have, and the second round-trips the same bytes through the cache
 * as it is now.
 *
 * It drives the store directly, so it needs no server and no sign-in.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class BlobCacheInstrumentedTest {

    private val context = InstrumentationRegistry.getInstrumentation().targetContext

    private val store by lazy {
        SqlDelightLocalStore(
            database = createDatabase(DatabaseDriverFactory(context, DATABASE)),
            blobFiles = FileBlobFileStore(context),
            now = { System.currentTimeMillis() },
        )
    }

    @After
    fun dropTheScratchStore() {
        runBlocking { store.clearAll() }
        context.deleteDatabase(DATABASE)
    }

    @Test
    fun t90_aThreeMegabyteBlobInARowCannotBeReadBack() {
        // The layout the cache had: the bytes as a column of the row.
        val database = SQLiteDatabase.create(null)
        try {
            database.execSQL("CREATE TABLE blob_row (id TEXT PRIMARY KEY, bytes BLOB NOT NULL)")
            val insert = database.compileStatement("INSERT INTO blob_row(id, bytes) VALUES ('b1', ?)")
            insert.bindBlob(1, bytes(BLOB_SIZE))
            insert.executeInsert()

            val failure = runCatching {
                database.rawQuery("SELECT bytes FROM blob_row WHERE id = 'b1'", null).use { cursor ->
                    cursor.moveToFirst()
                    cursor.getBlob(0)
                }
            }.exceptionOrNull()
            assertNotNull("a $BLOB_SIZE byte row must not fit the cursor window", failure)
            assertTrue(
                "the platform must refuse the row, saw $failure",
                failure is SQLiteBlobTooBigException,
            )
        } finally {
            database.close()
        }
    }

    @Test
    fun t91_theCacheRoundTripsABlobLargerThanTheCursorWindow() = runBlocking {
        val written = bytes(BLOB_SIZE)
        store.cacheBlob("acct-1", "blob-big", "image/jpeg", written)

        val read = store.cachedBlob("acct-1", "blob-big")
        assertNotNull("the cache must answer for a $BLOB_SIZE byte blob", read)
        assertEquals("image/jpeg", read!!.contentType)
        assertEquals(BLOB_SIZE, read.bytes.size)
        assertTrue("the bytes must come back as they went in", written.contentEquals(read.bytes))
        assertEquals(BLOB_SIZE.toLong(), store.blobCacheSize())

        // A second read is the one a re-opened thread makes.
        val again = store.cachedBlob("acct-1", "blob-big")
        assertNotNull(again)
        assertTrue(written.contentEquals(again!!.bytes))
    }

    /** Bytes that do not compress, so the row weighs what it says. */
    private fun bytes(size: Int): ByteArray {
        val out = ByteArray(size)
        var state = 0x5eed
        for (index in out.indices) {
            state = state * 1103515245 + 12345
            out[index] = (state ushr 16).toByte()
        }
        return out
    }

    private companion object {
        /** Over Android's 2 MiB cursor window, as the reported blob was. */
        const val BLOB_SIZE = 3 * 1024 * 1024

        const val DATABASE = "herold-blobcache-test.db"
    }
}
