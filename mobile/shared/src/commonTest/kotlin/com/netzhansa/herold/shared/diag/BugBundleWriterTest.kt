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

    private val firstShot = BugShot(
        route = "thread/{accountId}/{threadId}",
        routeArguments = mapOf("accountId" to "acct-1", "threadId" to "T42"),
        threadId = "T42",
        capturedAtMs = 1_700_000_000_500,
        screenshot = byteArrayOf(0x89.toByte(), 'P'.code.toByte()),
    )

    private val secondShot = BugShot(
        route = "inbox",
        capturedAtMs = 1_700_000_001_500,
        screenshot = byteArrayOf(0x89.toByte(), 'N'.code.toByte()),
    )

    private val capture = BugCapture(
        shots = listOf(firstShot),
        accountScope = "acct-1",
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
    fun theTitleIsWhatWasTypedAndTravelsInTheReport() {
        val bundle = build()
        assertEquals("the thread view is blank", bundle.title)
        assertEquals("the thread view is blank", bundle.meta()["title"]?.jsonPrimitive?.content)
    }

    @Test
    fun anUndescribedReportIsNamedByWhereAndWhenItWasRaised() {
        val bundle = build(submission = BugSubmission())
        assertEquals("thread 2023-11-14T22:13:22Z", bundle.title)
        assertEquals("thread 2023-11-14T22:13:22Z", bundle.meta()["title"]?.jsonPrimitive?.content)
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
            build().markdown.startsWith("# Bug: the thread view is blank"),
            build().markdown,
        )
        assertTrue(build().markdown.contains("## Description"))
    }

    @Test
    fun aReportSentWithOneTapSaysNothingWasEntered() {
        val bundle = build(submission = BugSubmission())
        val meta = bundle.meta()
        assertEquals(false, meta["descriptionEntered"]?.jsonPrimitive?.content?.toBoolean())
        assertEquals("", meta["sketch"]?.jsonPrimitive?.content)
        assertEquals(
            BugBundleWriter.NO_DESCRIPTION,
            bundle.markdown.lineSequence().first(),
        )
        // The capture still travels: the report is a capture with no words on it.
        assertTrue(bundle.markdown.contains("## Page"), bundle.markdown)
        assertTrue(bundle.markdown.contains("## State"), bundle.markdown)
        assertFalse(bundle.markdown.contains("## Description"), bundle.markdown)
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
        // With no title the report is still named by the place and the time.
        assertEquals("thread 2023-11-14T22:13:22Z", bundle.title)
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
        assertEquals(bundle.markdown, bundle.text("report.md"))
        assertTrue(bundle.markdown.startsWith("# Bug: the thread view is blank"), bundle.markdown)
        assertTrue(bundle.markdown.contains("## Description"))
        assertTrue(bundle.markdown.contains("herold Android 1.4.2 (abc1234)"))
        assertTrue(bundle.markdown.contains("Android 16 (API 36), Google sdk_gphone64_arm64"))
        assertTrue(bundle.markdown.contains("## Logs (tail)"))
        assertFalse(bundle.markdown.contains("quarterly numbers"))
    }

    @Test
    fun aFeatureRequestIsTheSameBundleWithAnotherKind() {
        val bundle = build(submission = submission.copy(kind = BugKind.FEATURE))
        assertEquals("feature", bundle.meta()["kind"]?.jsonPrimitive?.content)
        assertTrue(bundle.markdown.startsWith("# Feature: "))
    }

    @Test
    fun aReportWithNoNoteStillReads() {
        val bundle = build(submission = BugSubmission(title = "widget shows nothing", note = ""))
        assertEquals("widget shows nothing", bundle.meta()["sketch"]?.jsonPrimitive?.content)
        assertTrue(bundle.markdown.contains("## Description\n\nwidget shows nothing"), bundle.markdown)
        assertEquals("widget shows nothing", bundle.title)
    }

    @Test
    fun aReportOfSeveralScreensCarriesAPictureAndAnEntryForEach() {
        val bundle = build(capture = capture.withShot(secondShot))
        assertEquals(
            listOf("report.json", "report.md", "logs.txt", "screenshot-1.png", "screenshot-2.png"),
            bundle.files.map { it.name },
        )
        val meta = json.parseToJsonElement(bundle.text("report.json")).jsonObject
        assertEquals(2, meta["screenshotCount"]?.jsonPrimitive?.content?.toInt())
        val captures = meta["captures"]!!.jsonArray
        assertEquals(2, captures.size)
        assertEquals(1, captures[0].jsonObject["index"]?.jsonPrimitive?.content?.toInt())
        assertEquals(
            "thread/{accountId}/{threadId}",
            captures[0].jsonObject["route"]?.jsonPrimitive?.content,
        )
        assertEquals("T42", captures[0].jsonObject["threadId"]?.jsonPrimitive?.content)
        assertEquals(
            "acct-1",
            captures[0].jsonObject["routeArguments"]!!.jsonObject["accountId"]?.jsonPrimitive?.content,
        )
        assertEquals("2023-11-14T22:13:20.500Z", captures[0].jsonObject["capturedAt"]?.jsonPrimitive?.content)
        assertEquals(2, captures[1].jsonObject["index"]?.jsonPrimitive?.content?.toInt())
        assertEquals("inbox", captures[1].jsonObject["route"]?.jsonPrimitive?.content)
    }

    @Test
    fun theReportIsNamedAndRoutedByItsFirstCapture() {
        val bundle = build(submission = BugSubmission(), capture = capture.withShot(secondShot))
        assertEquals("thread 2023-11-14T22:13:22Z", bundle.title)
        val meta = json.parseToJsonElement(bundle.text("report.json")).jsonObject
        assertEquals(
            "thread/{accountId}/{threadId}",
            meta["context"]!!.jsonObject["route"]?.jsonPrimitive?.content,
        )
        assertEquals(
            "herold://thread/{accountId}/{threadId}?accountId=acct-1&threadId=T42",
            meta["page"]!!.jsonObject["url"]?.jsonPrimitive?.content,
        )
    }

    @Test
    fun theMarkdownListsEveryCaptureWithItsRoute() {
        val markdown = build(capture = capture.withShot(secondShot)).markdown
        assertTrue(markdown.contains("## Captures"), markdown)
        assertTrue(
            markdown.contains(
                "1. herold://thread/{accountId}/{threadId}?accountId=acct-1&threadId=T42 - " +
                    "2023-11-14T22:13:20.500Z (screenshot-1.png)",
            ),
            markdown,
        )
        assertTrue(markdown.contains("2. herold://inbox - 2023-11-14T22:13:21.500Z (screenshot-2.png)"), markdown)
    }

    @Test
    fun aCaptureTakenOffTheStripLeavesTheReport() {
        val three = capture.withShot(secondShot).withShot(secondShot.copy(route = "search"))
        val bundle = build(capture = three.withoutShot(1))
        assertEquals(
            listOf("report.json", "report.md", "logs.txt", "screenshot-1.png", "screenshot-2.png"),
            bundle.files.map { it.name },
        )
        val captures = json.parseToJsonElement(bundle.text("report.json"))
            .jsonObject["captures"]!!.jsonArray
        assertEquals(
            listOf("thread/{accountId}/{threadId}", "search"),
            captures.map { it.jsonObject["route"]!!.jsonPrimitive.content },
        )
    }

    @Test
    fun aCaptureThatTookNoPictureStillNamesItsScreen() {
        val bundle = build(capture = capture.withShot(secondShot.copy(screenshot = null)))
        assertEquals(
            listOf("report.json", "report.md", "logs.txt", "screenshot-1.png"),
            bundle.files.map { it.name },
        )
        val captures = json.parseToJsonElement(bundle.text("report.json"))
            .jsonObject["captures"]!!.jsonArray
        assertEquals(2, captures.size)
        assertEquals("inbox", captures[1].jsonObject["route"]?.jsonPrimitive?.content)
    }

    @Test
    fun droppingTheScreenshotsKeepsTheCapturesTheyWereTakenOn() {
        val bundle = build(
            submission = submission.copy(includeScreenshot = false),
            capture = capture.withShot(secondShot),
        )
        assertEquals(listOf("report.json", "report.md", "logs.txt"), bundle.files.map { it.name })
        val meta = json.parseToJsonElement(bundle.text("report.json")).jsonObject
        assertEquals(0, meta["screenshotCount"]?.jsonPrimitive?.content?.toInt())
        assertEquals(2, meta["captures"]!!.jsonArray.size)
        assertFalse(bundle.markdown.contains("(screenshot-1.png)"), bundle.markdown)
    }
}
