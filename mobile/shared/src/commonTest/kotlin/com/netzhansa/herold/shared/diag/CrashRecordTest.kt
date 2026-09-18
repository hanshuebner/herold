package com.netzhansa.herold.shared.diag

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The crash a report carries from the run that died (issue #420): what
 * the record keeps between two processes, and how the next bundle
 * presents it.
 */
class CrashRecordTest {

    private val json = Json { ignoreUnknownKeys = true }

    private val record = CrashRecord(
        atMs = 1_700_000_000_000,
        threadName = "main",
        exception = "android.database.sqlite.SQLiteBlobTooBigException",
        message = "Row too big to fit into CursorWindow requiredPos=0, totalRows=1",
        stack = listOf(
            "android.database.sqlite.SQLiteBlobTooBigException: Row too big to fit into CursorWindow",
            "\tat android.database.sqlite.SQLiteConnection.nativeExecuteForCursorWindow(Native Method)",
            "\tat com.netzhansa.herold.shared.store.SqlDelightLocalStore\$cachedBlob\$2.invokeSuspend(SqlDelightLocalStore.kt:352)",
            "\tat com.netzhansa.herold.android.ui.thread.ThreadScreenKt.ThreadScreen\$blobOf(ThreadScreen.kt:173)",
        ).joinToString("\n"),
        appVersion = "0.8.0",
        appCommit = "047c84ab",
        route = "thread/{accountId}/{threadId}",
        logs = listOf(
            LogLine(1_699_999_999_000, LogLevel.INFO, "herold.shell", "shell state=mail"),
            LogLine(1_699_999_999_500, LogLevel.INFO, "herold.sync", "sync syncing"),
        ),
    )

    private val capture = BugCapture(
        shots = listOf(BugShot(route = "inbox", capturedAtMs = 1_700_000_100_000)),
        device = DeviceFacts("0.8.0", "047c84ab", "17", 37, "Google", "Pixel 9"),
        sync = SyncFacts(state = "idle"),
        push = PushFacts(choice = "AUTOMATIC"),
    )

    private fun bundle(crash: CrashRecord? = record): BugBundle =
        BugBundleWriter.build(
            BugSubmission(title = "the app closed on a thread"),
            capture,
            createdAtMs = 1_700_000_200_000,
            crash = crash,
        )

    private fun BugBundle.text(name: String): String =
        files.first { it.name == name }.bytes.decodeToString()

    private fun BugBundle.meta(): JsonObject =
        json.parseToJsonElement(text("report.json")).jsonObject

    @Test
    fun aRecordSurvivesEncodingAndComesBackWhole() {
        val read = CrashRecords.decode(CrashRecords.encode(record))
        assertNotNull(read)
        assertEquals(record.exception, read.exception)
        assertEquals(record.message, read.message)
        assertEquals(record.stack, read.stack)
        assertEquals(record.route, read.route)
        assertEquals(record.appCommit, read.appCommit)
        assertEquals(listOf("shell state=mail", "sync syncing"), read.logs.map { it.message })
    }

    @Test
    fun anUnreadableRecordReadsAsNoCrash() {
        assertNull(CrashRecords.decode("{not json"))
        assertNull(CrashRecords.decode("{}"))
        assertNull(CrashRecords.decode(CrashRecords.encode(record.copy(stack = ""))))
    }

    @Test
    fun theNextBundleCarriesTheTraceAsItsOwnPart() {
        val bundle = bundle()
        assertTrue("crash.txt" in bundle.files.map { it.name }, bundle.files.map { it.name }.toString())
        val crash = bundle.text("crash.txt")
        assertTrue("herold Android 0.8.0 (047c84ab)" in crash, crash)
        assertTrue("SQLiteBlobTooBigException" in crash, crash)
        assertTrue("ThreadScreen.kt:173" in crash, crash)
        // The ring's run-up travels with the trace: the lines held when
        // the process died, which the live ring no longer has.
        assertTrue("shell state=mail" in crash, crash)
        assertTrue("2023-11-14T22:13:20Z" in crash, crash)
    }

    @Test
    fun theReportReadsTheCrashInItsOwnSection() {
        val markdown = bundle().text("report.md")
        assertTrue("## Crash" in markdown, markdown)
        assertTrue("SQLiteBlobTooBigException" in markdown, markdown)
        assertTrue("crash.txt" in markdown, markdown)
        assertTrue("- Route: thread/{accountId}/{threadId}" in markdown, markdown)
    }

    @Test
    fun theReportMetaNamesTheCrashForTriage() {
        val crash = bundle().meta()["crash"]?.jsonObject
        assertNotNull(crash)
        assertEquals(
            "android.database.sqlite.SQLiteBlobTooBigException",
            crash["exception"]?.jsonPrimitive?.content,
        )
        assertEquals("2023-11-14T22:13:20Z", crash["at"]?.jsonPrimitive?.content)
        assertEquals("crash.txt", crash["file"]?.jsonPrimitive?.content)
    }

    @Test
    fun aReportWithoutACrashCarriesNoCrashPart() {
        val bundle = bundle(crash = null)
        assertFalse("crash.txt" in bundle.files.map { it.name })
        assertNull(bundle.meta()["crash"])
        assertFalse("## Crash" in bundle.text("report.md"))
    }
}
