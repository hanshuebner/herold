package com.netzhansa.herold.shared.diag

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The bundle a report becomes, in the layout `herold bug-fetch` expands
 * (`internal/admin/cmd_bugfetch.go`): the parts it places by name, and
 * the `report.json` fields `/bug-inbox` reads.
 */
class BugBundleWriterTest {

    private val json = Json { ignoreUnknownKeys = true }

    private val capture = BugCapture(
        route = "thread/{accountId}/{threadId}",
        routeArguments = mapOf("accountId" to "acct-1", "threadId" to "T42"),
        accountScope = "acct-1",
        threadId = "T42",
        principal = "alice@example.local",
        serverUrl = "http://10.0.2.2:8080",
        accountIds = listOf("acct-1", "acct-2"),
        device = DeviceFacts(
            appVersion = "1.4.2",
            appCommit = "abc1234",
            androidVersion = "16",
            sdkInt = 36,
            manufacturer = "Google",
            model = "sdk_gphone64_arm64",
        ),
        sync = SyncFacts(state = "failed", lastError = "the server could not be reached", offline = true),
        outbox = OutboxSummary(
            queued = 2,
            failed = 1,
            entries = listOf(
                OutboxLine("SEND", "QUEUED", 0, "Send: quarterly numbers"),
                OutboxLine("ACTION", "FAILED", 3, "Archive", "forbidden"),
            ),
        ),
        push = PushFacts(
            choice = "AUTOMATIC",
            transport = "fcm",
            registered = true,
            permissionGranted = true,
        ),
        logs = listOf(
            LogLine(1_700_000_000_000, LogLevel.INFO, "herold.shell", "shell state=mail"),
            LogLine(1_700_000_001_000, LogLevel.WARN, "herold.outbox", "the drain gave up"),
        ),
        screenshots = listOf(byteArrayOf(0x89.toByte(), 'P'.code.toByte())),
        sessionDetails = mapOf("grantId" to "grant-9", "deviceClientId" to "herold-android-1"),
    )

    private val submission = BugSubmission(
        title = "the thread view is blank",
        note = "Opened a conversation from the notification and nothing drew.",
    )

    private fun build(
        submission: BugSubmission = this.submission,
        capture: BugCapture = this.capture,
    ): BugBundle = BugBundleWriter.build(submission, capture, createdAtMs = 1_700_000_002_000)

    private fun BugBundle.text(name: String): String =
        files.first { it.name == name }.bytes.decodeToString()

    private fun BugBundle.meta(): JsonObject =
        json.parseToJsonElement(text("report.json")).jsonObject

    @Test
    fun theBundleCarriesTheNamesTheFetchPlaces() {
        val bundle = build()
        assertEquals(
            listOf("report.json", "report.md", "logs.txt", "screenshot-1.png"),
            bundle.files.map { it.name },
        )
        assertEquals("image/png", bundle.files.first { it.name == "screenshot-1.png" }.type)
    }

    @Test
    fun theSubjectCarriesThePrefixTheFetchStrips() {
        assertEquals("herold bug: the thread view is blank", build().subject)
    }

    @Test
    fun anUndescribedReportIsNamedByWhereAndWhenItWasRaised() {
        val bundle = build(submission = BugSubmission())
        assertEquals("herold bug: thread 2023-11-14T22:13:22Z", bundle.subject)
    }

    @Test
    fun theRouteLabelDropsTheIds() {
        assertEquals("thread", BugBundleWriter.routeLabel("thread/{accountId}/{threadId}"))
        assertEquals("inbox", BugBundleWriter.routeLabel("inbox"))
        assertEquals("compose", BugBundleWriter.routeLabel("/compose/{mode}/{accountId}/{emailId}"))
        assertEquals("unknown", BugBundleWriter.routeLabel(""))
    }

    @Test
    fun aDescribedReportSaysSoAndCarriesTheText() {
        val meta = build().meta()
        assertEquals(true, meta["descriptionEntered"]?.jsonPrimitive?.content?.toBoolean())
        assertTrue(
            build().bodyText.startsWith("# Bug: the thread view is blank"),
            build().bodyText,
        )
        assertTrue(build().bodyText.contains("## Description"))
    }

    @Test
    fun aReportSentWithOneTapSaysNothingWasEntered() {
        val bundle = build(submission = BugSubmission())
        val meta = bundle.meta()
        assertEquals(false, meta["descriptionEntered"]?.jsonPrimitive?.content?.toBoolean())
        assertEquals("", meta["sketch"]?.jsonPrimitive?.content)
        assertEquals(
            BugBundleWriter.NO_DESCRIPTION,
            bundle.bodyText.lineSequence().first(),
        )
        // The capture still travels: the report is a capture with no words on it.
        assertTrue(bundle.bodyText.contains("## Page"), bundle.bodyText)
        assertTrue(bundle.bodyText.contains("## State"), bundle.bodyText)
        assertFalse(bundle.bodyText.contains("## Description"), bundle.bodyText)
        assertEquals(
            "thread/{accountId}/{threadId}",
            meta["context"]!!.jsonObject["route"]?.jsonPrimitive?.content,
        )
        assertEquals("1.4.2 (abc1234)", meta["app"]!!.jsonObject["version"]?.jsonPrimitive?.content)
        assertEquals(1, meta["screenshotCount"]?.jsonPrimitive?.content?.toInt())
    }

    @Test
    fun aNoteWithoutATitleStillCountsAsDescribed() {
        val bundle = build(submission = BugSubmission(note = "it went blank after a sync"))
        assertEquals(true, bundle.meta()["descriptionEntered"]?.jsonPrimitive?.content?.toBoolean())
        assertEquals("it went blank after a sync", bundle.meta()["sketch"]?.jsonPrimitive?.content)
        // With no title the subject still names the place and the time.
        assertEquals("herold bug: thread 2023-11-14T22:13:22Z", bundle.subject)
    }

    @Test
    fun theReportJsonNamesTheRouteAndTheVersion() {
        val meta = build().meta()
        assertEquals(BugBundleWriter.PROTOCOL, meta["protocol"]?.jsonPrimitive?.content)
        assertEquals("bug", meta["kind"]?.jsonPrimitive?.content)
        assertEquals("2023-11-14T22:13:22Z", meta["createdAt"]?.jsonPrimitive?.content)
        assertEquals(
            "herold://thread/{accountId}/{threadId}?accountId=acct-1&threadId=T42",
            meta["page"]!!.jsonObject["url"]?.jsonPrimitive?.content,
        )
        assertEquals("1.4.2 (abc1234)", meta["app"]!!.jsonObject["version"]?.jsonPrimitive?.content)
        assertEquals("herold-android", meta["app"]!!.jsonObject["id"]?.jsonPrimitive?.content)
        assertEquals(
            "thread/{accountId}/{threadId}",
            meta["context"]!!.jsonObject["route"]?.jsonPrimitive?.content,
        )
        assertEquals("alice@example.local", meta["principal"]!!.jsonObject["label"]?.jsonPrimitive?.content)
        assertEquals(1, meta["screenshotCount"]?.jsonPrimitive?.content?.toInt())
    }

    @Test
    fun theSketchIsTheTitleThenTheNote() {
        val meta = build().meta()
        assertEquals(
            "the thread view is blank\n\nOpened a conversation from the notification and nothing drew.",
            meta["sketch"]?.jsonPrimitive?.content,
        )
    }

    @Test
    fun theContextCarriesTheStateTheReporterCaptured() {
        val context = build().meta()["context"]!!.jsonObject
        assertEquals("T42", context["threadId"]?.jsonPrimitive?.content)
        assertEquals("acct-1", context["accountScope"]?.jsonPrimitive?.content)
        assertEquals("failed", context["sync"]!!.jsonObject["state"]?.jsonPrimitive?.content)
        assertEquals(true, context["sync"]!!.jsonObject["offline"]?.jsonPrimitive?.content?.toBoolean())
        assertEquals(3, context["outbox"]!!.jsonObject["total"]?.jsonPrimitive?.content?.toInt())
        assertEquals("fcm", context["push"]!!.jsonObject["transport"]?.jsonPrimitive?.content)
        assertEquals("36", context["device"]!!.jsonObject["sdkInt"]?.jsonPrimitive?.content)
    }

    @Test
    fun theOutboxSummaryDropsTheSubjects() {
        val entries = build().meta()["context"]!!.jsonObject["outbox"]!!.jsonObject["entries"]!!.jsonArray
        val labels = entries.map { it.jsonObject["label"]!!.jsonPrimitive.content }
        assertEquals(listOf("Send: (redacted)", "Archive"), labels)
        assertFalse(build().text("report.json").contains("quarterly numbers"))
    }

    @Test
    fun theLogsTravelAsEntriesAndAsALogFile() {
        val bundle = build()
        val logs = bundle.meta()["logs"]!!.jsonArray
        assertEquals(2, logs.size)
        assertEquals("herold.shell", logs[0].jsonObject["ctx"]?.jsonPrimitive?.content)
        assertEquals("shell state=mail", logs[0].jsonObject["msg"]?.jsonPrimitive?.content)
        val text = bundle.text("logs.txt")
        assertTrue(text.startsWith("2023-11-14T22:13:20Z INFO [herold.shell] shell state=mail"), text)
        assertTrue(text.endsWith("\n"), text)
    }

    @Test
    fun droppingTheScreenshotAndTheLogLeavesTheReport() {
        val bundle = build(
            submission = submission.copy(includeScreenshot = false, includeLogs = false),
        )
        assertEquals(listOf("report.json", "report.md", "logs.txt"), bundle.files.map { it.name })
        assertEquals("", bundle.text("logs.txt"))
        assertEquals(0, bundle.meta()["screenshotCount"]?.jsonPrimitive?.content?.toInt())
        assertEquals(0, bundle.meta()["logs"]!!.jsonArray.size)
    }

    @Test
    fun theSessionDetailsTravelOnlyWhenAskedFor() {
        assertNull(build().files.firstOrNull { it.name == "private.json" })
        val withPrivate = build(submission = submission.copy(includeSessionDetails = true))
        val part = assertNotNull(withPrivate.files.firstOrNull { it.name == "private.json" })
        val private = json.parseToJsonElement(part.bytes.decodeToString()).jsonObject
        assertEquals("grant-9", private["grantId"]?.jsonPrimitive?.content)
    }

    @Test
    fun theBodyIsTheMarkdownReport() {
        val bundle = build()
        assertEquals(bundle.bodyText, bundle.text("report.md"))
        assertTrue(bundle.bodyText.startsWith("# Bug: the thread view is blank"), bundle.bodyText)
        assertTrue(bundle.bodyText.contains("## Description"))
        assertTrue(bundle.bodyText.contains("herold Android 1.4.2 (abc1234)"))
        assertTrue(bundle.bodyText.contains("Android 16 (API 36), Google sdk_gphone64_arm64"))
        assertTrue(bundle.bodyText.contains("## Logs (tail)"))
        assertFalse(bundle.bodyText.contains("quarterly numbers"))
    }

    @Test
    fun aFeatureRequestIsTheSameBundleWithAnotherKind() {
        val bundle = build(submission = submission.copy(kind = BugKind.FEATURE))
        assertEquals("feature", bundle.meta()["kind"]?.jsonPrimitive?.content)
        assertTrue(bundle.bodyText.startsWith("# Feature: "))
    }

    @Test
    fun aReportWithNoNoteStillReads() {
        val bundle = build(submission = BugSubmission(title = "widget shows nothing", note = ""))
        assertEquals("widget shows nothing", bundle.meta()["sketch"]?.jsonPrimitive?.content)
        assertTrue(bundle.bodyText.contains("## Description\n\nwidget shows nothing"), bundle.bodyText)
        assertEquals("herold bug: widget shows nothing", bundle.subject)
    }
}
