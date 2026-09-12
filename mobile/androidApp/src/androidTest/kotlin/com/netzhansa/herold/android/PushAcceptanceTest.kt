package com.netzhansa.herold.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import android.os.Build
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.UiDevice
import com.netzhansa.herold.android.push.ActiveThread
import com.netzhansa.herold.android.push.MailNotifier
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.push.MailNotification
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The milestone 1b acceptance run (issue #328): a push herold sent arrives
 * as a notification, its actions apply, and a tap opens the thread.
 *
 * Google's delivery leg cannot run from a test, so the message is injected
 * into the service's own handler exactly as the SDK would: a [RemoteMessage]
 * carrying the `data.payload` envelope the dispatcher builds
 * (`internal/webpush/payload.go`). Everything downstream of that - the
 * bounded reconcile, the channel, the rendering, the actions, the tap
 * intent - is the code the device runs.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class PushAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val notifications: NotificationManager
        get() = instrumentation.targetContext
            .getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    @Before
    fun signedInWithNotificationsAllowed() {
        grantNotificationPermission()
        notifications.cancelAll()
        ActiveThread.left("", "")
        val result = runBlocking {
            app.container.signInWithPassword(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
        }
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        // Each test provisions the message it pushes about, so a run does
        // not depend on what an earlier one left in the inbox.
        subject = DevInstance.deliverMail(
            subject = "Push acceptance ${System.currentTimeMillis()}",
            body = "Are you free at noon? The place on the corner has a new menu.",
        )
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking {
                app.container.session.value!!.syncEngine.syncAll()
                app.container.store.inboxEmails().first().any { it.subject == subject }
            }
        }
    }

    private lateinit var subject: String

    @Test
    fun t01_aPushRendersAsAMailNotificationWithItsActions() {
        val target = deliveredMessage()
        val posted = deliver(payloadFor(target))

        val title = posted.extras.getString(Notification.EXTRA_TITLE).orEmpty()
        val body = posted.extras.getString(Notification.EXTRA_TEXT).orEmpty()
        val expanded = posted.extras.getString(Notification.EXTRA_BIG_TEXT).orEmpty()
        assertEquals("the sender's display name is the title", target.senderDisplay, title)
        assertEquals("the subject is the body", target.subject, body)
        assertTrue("the preview is on the expanded line: $expanded", expanded.contains(target.preview.take(20)))
        assertEquals("mail", posted.channelId)
        assertNotNull("the sender's avatar is the large icon", posted.getLargeIcon())

        val actions = posted.actions.orEmpty().map { it.title.toString() }
        assertEquals(
            listOf(MailNotifier.ARCHIVE_TITLE, MailNotifier.MARK_READ_TITLE, MailNotifier.REPLY_TITLE),
            actions,
        )

        // Grouped under a per-account summary (REQ-AND-PUSH-12).
        assertEquals(MailNotification.groupFor(target.accountId), posted.group)
        val summary = activeNotifications().firstOrNull {
            it.notification.flags and Notification.FLAG_GROUP_SUMMARY != 0
        }
        assertNotNull("the account's notifications must have a summary", summary)
        assertEquals(
            "the bundle is headed by the account the mail arrived for",
            DevInstance.email,
            summary!!.notification.extras.getString(Notification.EXTRA_TITLE),
        )

        showShade("10-push-notification")
    }

    @Test
    fun t02_aSecondMessageOnTheThreadReplacesTheNotificationRatherThanStacking() {
        val target = deliveredMessage()
        deliver(payloadFor(target))
        deliver(payloadFor(target, subject = "Re: " + target.subject, emailId = target.id + "0"))

        val tag = MailNotification.tagFor(target.accountId, target.threadId)
        val onThread = activeNotifications().filter { it.tag == tag }
        assertEquals("one notification per thread", 1, onThread.size)
        val body = onThread.single().notification.extras.getString(Notification.EXTRA_TEXT).orEmpty()
        assertTrue("the newest message's subject is shown: $body", body.startsWith("Re: "))
    }

    @Test
    fun t03_aPushForTheThreadOnScreenPostsNothing() {
        val target = deliveredMessage()
        ActiveThread.entered(target.accountId, target.threadId)
        try {
            injectPush(instrumentation.targetContext.applicationContext, payloadFor(target))
            compose.waitForIdle()
            val tag = MailNotification.tagFor(target.accountId, target.threadId)
            assertTrue(
                "no notification while the thread is on screen",
                activeNotifications().none { it.tag == tag },
            )
        } finally {
            ActiveThread.left(target.accountId, target.threadId)
        }
    }

    @Test
    fun t04_theMarkReadActionAppliesOnTheServer() = runBlocking {
        val target = deliveredMessage()
        val posted = deliver(payloadFor(target))
        val action = posted.actions.orEmpty()
            .first { it.title.toString() == MailNotifier.MARK_READ_TITLE }

        action.actionIntent.send()

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking {
                DevInstance.serverEmail(server, accountId, target.id)
                    ?.keywords?.contains(Keywords.SEEN) == true
            }
        }
        compose.waitUntil(TIMEOUT_MS) {
            activeNotifications().none {
                it.tag == MailNotification.tagFor(target.accountId, target.threadId)
            }
        }
    }

    @Test
    fun t05_theArchiveActionTakesTheMessageOutOfTheInboxOnTheServer() = runBlocking {
        val target = deliveredMessage()
        val posted = deliver(payloadFor(target))
        val action = posted.actions.orEmpty()
            .first { it.title.toString() == MailNotifier.ARCHIVE_TITLE }

        action.actionIntent.send()

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        val inboxId = app.container.store.mailboxList()
            .first { it.accountId == accountId && it.role == MailboxRoles.INBOX }.id
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking {
                DevInstance.serverEmail(server, accountId, target.id)
                    ?.mailboxIds?.contains(inboxId) == false
            }
        }
        compose.waitUntil(TIMEOUT_MS) {
            activeNotifications().none {
                it.tag == MailNotification.tagFor(target.accountId, target.threadId)
            }
        }
    }

    @Test
    fun t06_twoThreadsBundleUnderTheAccountsHeader() {
        val target = deliveredMessage()
        deliver(payloadFor(target))
        deliver(
            payloadFor(
                target,
                subject = "Second thread " + System.currentTimeMillis(),
                emailId = target.id + "1",
            ).replace("\"${target.threadId}\"", "\"${target.threadId}x\""),
        )

        val children = activeNotifications().filter {
            it.notification.flags and Notification.FLAG_GROUP_SUMMARY == 0
        }
        assertEquals("two threads, two notifications", 2, children.size)
        showShade("17-account-group-header")
    }

    // ---- helpers -------------------------------------------------------

    /**
     * Hands [payload] to the messaging service the way the Firebase SDK
     * does and returns the notification it posted.
     */
    private fun deliver(payload: String): Notification {
        injectPush(instrumentation.targetContext.applicationContext, payload)
        compose.waitForIdle()
        val posted = activeNotifications().firstOrNull {
            it.notification.flags and Notification.FLAG_GROUP_SUMMARY == 0
        }
        assertNotNull("no notification was posted for the push", posted)
        return posted!!.notification
    }

    /**
     * The envelope herold's dispatcher builds for a new message, filled
     * with a real message of the dev instance so the ids route.
     */
    private fun payloadFor(
        email: Email,
        subject: String = email.subject,
        emailId: String = email.id,
    ): String = """
        {"@type":"StateChange","changed":{"${email.accountId}":{"Email":"1"}},
         "kind":"mail","type":"email","from":"${email.senderDisplay}",
         "body":"${subject.jsonEscaped()}","subject":"${subject.jsonEscaped()}",
         "preview":"${email.preview.take(80).jsonEscaped()}","mailbox":"Inbox",
         "emailId":"$emailId","msgid":"$emailId","threadId":"${email.threadId}"}
    """.trimIndent()

    private fun String.jsonEscaped(): String =
        replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")

    /** The message this test delivered for itself. */
    private fun deliveredMessage(): Email = runBlocking {
        app.container.store.inboxEmails().first().first { it.subject == subject }
    }

    private fun activeNotifications() = notifications.activeNotifications.toList()

    /** Opens the shade so the screenshot shows what the user sees. */
    private fun showShade(name: String) {
        val device = UiDevice.getInstance(instrumentation)
        device.openNotification()
        device.waitForIdle()
        compose.captureScreen(name)
        device.pressBack()
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
