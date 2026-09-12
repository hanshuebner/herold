package com.netzhansa.herold.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import android.text.format.DateFormat
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.push.RegistrationOutcome
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.datetime.Clock
import kotlinx.datetime.Instant
import kotlinx.datetime.TimeZone
import kotlinx.datetime.toLocalDateTime
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.net.HttpURLConnection
import java.net.URL
import kotlin.time.Duration.Companion.days
import kotlin.time.Duration.Companion.minutes

/**
 * The custom snooze (issue #350, suite REQ-SNZ-05/10/11/12), driven
 * against an ephemeral herold whose snooze worker sweeps every five
 * seconds (`scripts/dev-instance.sh`), so a wake two minutes out lands
 * inside the run:
 *
 *   am instrument ... -e class com.netzhansa.herold.android.SnoozeAcceptanceTest \
 *     -e heroldBaseUrl ... -e heroldSmtpAddr ... -e heroldFakeFcmAddr ...
 *
 * The wake is read from the server and from the fake FCM - the push
 * herold actually emitted - rather than from the screen alone.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class SnoozeAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val notifications: NotificationManager
        get() = instrumentation.targetContext
            .getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    private val zone = TimeZone.currentSystemDefault()

    private val token = "fcm-snooze-${System.currentTimeMillis()}"

    @Before
    fun notificationsAllowed() {
        grantNotificationPermission()
        // An earlier run's notification is still in the shade; the wake
        // check reads the shade, so it starts from an empty one.
        notifications.cancelAll()
    }

    @Test
    fun t40_aCustomSnoozeTwoMinutesAheadWakesTheMessageWithAPush() = runBlocking {
        signInAndSync()
        val fake = DevInstance.fakeFcmAddr
        clearFakeMessages(fake)
        app.container.session.value!!.pushRegistrar.unregister()
        val registration = app.container.session.value!!.pushRegistrar.register(token)
        assertTrue("registration rejected: $registration", registration is RegistrationOutcome.Registered)

        val subject = DevInstance.deliverMail(
            subject = "Custom snooze ${System.currentTimeMillis()}",
            body = "Snoozed for two minutes, then back in the inbox.",
        )
        val target = awaitInbox(subject)

        val now = Clock.System.now()
        // The picker works in whole minutes: the wake is the start of the
        // minute two minutes out.
        val wakeAt = now.plus(2.minutes).toLocalDateTime(zone)
            .let { SnoozeClock.customWakeTime(it.date, it.hour, it.minute, zone) }
        // The date dialog opens on the day of the next full hour, which is
        // the day the wake falls on except in the last hour of the day.
        assumeTrue(
            "run the custom-pick wake check outside the last hour of the day",
            SnoozeClock.nextFullHour(now, zone).toLocalDateTime(zone).date == wakeAt.toLocalDateTime(zone).date,
        )

        openThread(target.threadId)
        pickCustomWakeTime(wakeAt)

        // The server holds the picked wake time, to the minute.
        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        assertEquals(
            "the server must hold the picked wake time",
            wakeAt,
            awaitServerSnooze(server, accountId, target.id),
        )

        // REQ-SNZ-10: the conversation is out of the inbox at once.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isEmpty()
        }
        compose.captureScreen("40-custom-snooze-leaves-the-inbox")

        // Everything the fake records from here on belongs to the wake:
        // the delivery's own push goes with this call.
        clearFakeMessages(fake)

        // REQ-SNZ-11: at the wake time the server clears the pair and the
        // message arrives in the inbox again, which pushes (issue #349).
        awaitServerAwake(server, accountId, target.id)
        val push = awaitPush(fake) { it.contains(subject) }
        assertNotNull("the wake must push like a fresh arrival", push)

        injectPush(instrumentation.targetContext.applicationContext, push!!)
        assertNotNull(
            "the wake push did not render as a notification for \"$subject\"",
            awaitNotification(subject),
        )

        // And the row is back on the list the user is looking at.
        awaitInbox(subject)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("41-custom-snooze-woke-into-the-inbox")
    }

    @Test
    fun t41_aSnoozedConversationStatesItsWakeTimeAndCancelBringsItBack() = runBlocking {
        signInAndSync()
        val subject = DevInstance.deliverMail(
            subject = "Snooze indicator ${System.currentTimeMillis()}",
            body = "Snoozed from another device, then cancelled here.",
        )
        val target = awaitInbox(subject)
        openThread(target.threadId)

        // The snooze arrives from elsewhere - another device, or this one
        // before the conversation was opened - so the open conversation has
        // to state it (suite REQ-SNZ-12).
        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        val wakeAt = Clock.System.now().plus(1.days).toLocalDateTime(zone)
            .let { SnoozeClock.customWakeTime(it.date, hour = 8, minute = 0, zone = zone) }
        app.container.session.value!!.client.emailSet(
            accountId,
            mapOf(target.id to buildJsonObject { put("snoozedUntil", SnoozeClock.wireValue(wakeAt)) }),
        )

        compose.waitUntil(WAKE_TIMEOUT_MS) {
            syncNow()
            compose.onAllNodesWithTag("thread-snoozed").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-snoozed").assertIsDisplayed()
        compose.onNodeWithTag("thread-snoozed-until", useUnmergedTree = true)
            .assertTextContains(
                "Snoozed until ${SnoozeClock.describe(wakeAt, Clock.System.now(), zone)}",
                substring = true,
            )
        compose.captureScreen("42-snoozed-indicator")

        // REQ-SNZ-12: cancelling clears the wake time, and herold takes the
        // $snoozed keyword with it.
        compose.onNodeWithTag("thread-snooze-cancel").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-snoozed").fetchSemanticsNodes().isEmpty()
        }
        assertNull(
            "the server must hold no wake time after the cancel",
            DevInstance.serverEmail(server, accountId, target.id)!!.snoozedUntil,
        )
        compose.captureScreen("43-snooze-cancelled")

        // REQ-SNZ-10 in reverse: the conversation is on the list again.
        compose.onNodeWithTag("thread-back").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
    }

    // ---- the flow under test -------------------------------------------

    /** Drives the sheet's custom row, the date dialog and the time dialog. */
    private fun pickCustomWakeTime(wakeAt: Instant) {
        val local = wakeAt.toLocalDateTime(zone)
        compose.onNodeWithTag("thread-snooze").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("snooze-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("38-snooze-sheet-with-custom-row")

        compose.onNodeWithTag("snooze-custom").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("snooze-date-confirm").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("39-snooze-date-picker")
        compose.onNodeWithTag("snooze-date-confirm").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("snooze-time-confirm").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("39b-snooze-time-picker")

        // The dial is the default surface; an exact minute goes in through
        // the keyboard entry the toggle switches to.
        compose.onNodeWithTag("snooze-time-mode").performClick()
        compose.waitForIdle()
        val fields = compose.onAllNodes(hasSetTextAction())
        val twentyFourHour = DateFormat.is24HourFormat(instrumentation.targetContext)
        val hour = if (twentyFourHour) local.hour else (local.hour % 12).let { if (it == 0) 12 else it }
        fields[0].performTextReplacement(hour.toString())
        fields[1].performTextReplacement(local.minute.toString().padStart(2, '0'))
        if (!twentyFourHour) {
            compose.onAllNodesWithText(if (local.hour < 12) "AM" else "PM").onFirst().performClick()
        }
        compose.onNodeWithTag("snooze-time-confirm").performClick()
    }

    // ---- helpers -------------------------------------------------------

    private fun signInAndSync() = runBlocking {
        if (app.container.session.value == null) {
            val result = app.container.signIn(
                DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
        }
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun syncNow(): Boolean {
        runBlocking { app.container.session.value!!.syncEngine.syncAll() }
        compose.waitForIdle()
        return true
    }

    /** Syncs until the message with [subject] is in the local inbox. */
    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(40) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(500)
        }
        error("the seeded message \"$subject\" never reached the inbox")
    }

    private fun openThread(threadId: String) {
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-$threadId"))
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** The wake time the server holds, once the `Email/set` has landed. */
    private suspend fun awaitServerSnooze(client: JmapClient, accountId: String, id: String): Instant {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            DevInstance.serverEmail(client, accountId, id)?.snoozedUntil
                ?.let { value -> SnoozeClock.parseWake(value)?.let { return it } }
            Thread.sleep(POLL_MS)
        }
        error("the server never took the snooze for $id")
    }

    /** Waits until the worker has released the snooze (REQ-SNZ-11). */
    private suspend fun awaitServerAwake(client: JmapClient, accountId: String, id: String) {
        val deadline = System.currentTimeMillis() + WAKE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (DevInstance.serverEmail(client, accountId, id)?.snoozedUntil == null) return
            Thread.sleep(POLL_MS)
        }
        error("the snooze worker never released $id")
    }

    // ---- the fake FCM's status API --------------------------------------

    private fun clearFakeMessages(addr: String) {
        val connection = URL("http://$addr/messages").openConnection() as HttpURLConnection
        connection.requestMethod = "DELETE"
        connection.inputStream.use { it.readBytes() }
    }

    /** The first recorded payload that matches, whichever token it went to. */
    private fun awaitPush(addr: String, matches: (String) -> Boolean): String? {
        val deadline = System.currentTimeMillis() + WAKE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val text = URL("http://$addr/messages").readText()
            Json.parseToJsonElement(text).jsonArray
                .mapNotNull { it.jsonObject["data"]?.jsonObject?.get("payload")?.jsonPrimitive?.content }
                .firstOrNull(matches)
                ?.let { return it }
            Thread.sleep(POLL_MS)
        }
        return null
    }

    /** The posted notification for [subject], once the service has rendered it. */
    private fun awaitNotification(subject: String): Notification? {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            notifications.activeNotifications.firstOrNull {
                it.notification.flags and Notification.FLAG_GROUP_SUMMARY == 0 &&
                    it.notification.extras.getString(Notification.EXTRA_TEXT).orEmpty().contains(subject)
            }?.let { return it.notification }
            Thread.sleep(POLL_MS)
        }
        return null
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L

        /** Two minutes of snooze plus the instance's five-second sweep. */
        const val WAKE_TIMEOUT_MS = 210_000L
        const val POLL_MS = 1_000L
    }
}
