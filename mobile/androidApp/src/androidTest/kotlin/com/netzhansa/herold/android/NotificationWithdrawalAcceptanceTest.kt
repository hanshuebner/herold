package com.netzhansa.herold.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.UiDevice
import com.netzhansa.herold.android.push.ActiveThread
import com.netzhansa.herold.android.push.PostedMailNotifications
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.push.MailNotification
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
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
 * A notification is withdrawn when the message it stands for is read or
 * filed away on another client (issue #481, REQ-AND-PUSH-14). The other
 * client is a second JMAP session against the same dev instance, which is
 * what the Suite or a second device is to the phone; the client's own sync
 * path is what brings the change to the shade.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class NotificationWithdrawalAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val context: Context get() = instrumentation.targetContext.applicationContext

    private val app get() = context as HeroldApplication

    private val notifications: NotificationManager
        get() = instrumentation.targetContext
            .getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    private lateinit var subject: String

    @Before
    fun signedInWithAFreshMessage() {
        grantNotificationPermission()
        notifications.cancelAll()
        PostedMailNotifications.clear(context)
        ActiveThread.left("", "")
        val result = runBlocking {
            app.container.signInWithPassword(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
        }
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        subject = DevInstance.deliverMail(
            subject = "Withdrawal acceptance ${System.currentTimeMillis()}",
            body = "The notification for this message goes when another client reads it.",
        )
        awaitStored(subject)
    }

    @Test
    fun t01_aMessageReadOnAnotherClientWithdrawsItsNotification() = runBlocking {
        val target = storedMessage(subject)
        postNotificationFor(target)
        showShade("39-notification-posted")

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        markSeen(server, accountId, target.id)

        syncUntil { notificationFor(target) == null }
        assertTrue(
            "the posted set no longer holds the thread",
            PostedMailNotifications.all(context).none { it.threadId == target.threadId },
        )
        showShade("40-notification-withdrawn")
    }

    @Test
    fun t02_aMessageArchivedOnAnotherClientWithdrawsItsNotification() = runBlocking {
        val target = storedMessage(subject)
        postNotificationFor(target)

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        val inboxId = app.container.store.mailboxList()
            .first { it.accountId == accountId && it.role == MailboxRoles.INBOX }.id
        val archiveId = app.container.store.mailboxList()
            .first { it.accountId == accountId && it.role == MailboxRoles.ARCHIVE }.id
        val outcome = server.emailSet(
            accountId,
            mapOf(
                target.id to buildJsonObject {
                    put("mailboxIds/$inboxId", JsonPrimitive(null as String?))
                    put("mailboxIds/$archiveId", true)
                },
            ),
        )
        assertTrue("the archive was refused: ${outcome.notUpdated}", outcome.isCompleteSuccess)

        syncUntil { notificationFor(target) == null }
    }

    /**
     * Two messages of one conversation share one notification, so reading
     * the first elsewhere leaves it standing for the second.
     */
    @Test
    fun t03_aThreadsNotificationStandsUntilItsLastNotifiedMessageIsRead() = runBlocking {
        val first = storedMessage(subject)
        val parent = first.messageId.firstOrNull()
        assertNotNull("the delivered message carries a Message-ID to thread on", parent)
        val replySubject = DevInstance.deliverMail(
            subject = "Re: $subject",
            body = "And a second message on the same conversation.",
            headers = "In-Reply-To: <$parent>\r\nReferences: <$parent>\r\n",
        )
        awaitStored(replySubject)
        val second = storedMessage(replySubject)
        assertEquals("the two messages must share a thread", first.threadId, second.threadId)

        postNotificationFor(first)
        postNotificationFor(second)
        assertEquals(
            "both messages are notified on the one thread",
            setOf(first.id, second.id),
            PostedMailNotifications.all(context).first { it.threadId == first.threadId }.emailIds,
        )

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        markSeen(server, accountId, first.id)
        syncUntil {
            app.container.store.email(accountId, first.id)?.isUnread == false
        }
        assertNotNull(
            "the unread message still has something to say",
            notificationFor(first),
        )

        markSeen(server, accountId, second.id)
        syncUntil { notificationFor(first) == null }
    }

    // ---- helpers -------------------------------------------------------

    /**
     * Posts the notification a push for [email] produces, through the
     * messaging service's own handler as the Firebase SDK feeds it.
     */
    private fun postNotificationFor(email: Email) {
        injectPush(context, payloadFor(email))
        compose.waitForIdle()
        assertNotNull("no notification was posted for the push", notificationFor(email))
    }

    private fun notificationFor(email: Email): Notification? {
        val tag = MailNotification.tagFor(email.accountId, email.threadId)
        return notifications.activeNotifications
            .firstOrNull { it.tag == tag && it.notification.flags and Notification.FLAG_GROUP_SUMMARY == 0 }
            ?.notification
    }

    private suspend fun markSeen(server: JmapClient, accountId: String, id: String) {
        val outcome = server.emailSet(
            accountId,
            mapOf(id to buildJsonObject { put("keywords/${Keywords.SEEN}", true) }),
        )
        assertTrue("marking seen was refused: ${outcome.notUpdated}", outcome.isCompleteSuccess)
    }

    /** Runs the client's normal sync pass until [done] holds. */
    private fun syncUntil(done: suspend () -> Boolean) {
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking {
                app.container.session.value!!.syncEngine.syncAll()
                done()
            }
        }
    }

    private fun awaitStored(subject: String) {
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking {
                app.container.session.value!!.syncEngine.syncAll()
                app.container.store.inboxEmails().first().any { it.subject == subject }
            }
        }
    }

    private fun storedMessage(subject: String): Email = runBlocking {
        app.container.store.inboxEmails().first().first { it.subject == subject }
    }

    /** The envelope herold's dispatcher builds for a new message. */
    private fun payloadFor(email: Email): String = """
        {"@type":"StateChange","changed":{"${email.accountId}":{"Email":"1"}},
         "kind":"mail","type":"email","from":"${email.senderDisplay}",
         "body":"${email.subject.jsonEscaped()}","subject":"${email.subject.jsonEscaped()}",
         "preview":"${email.preview.take(80).jsonEscaped()}","mailbox":"Inbox",
         "emailId":"${email.id}","msgid":"${email.id}","threadId":"${email.threadId}"}
    """.trimIndent()

    private fun String.jsonEscaped(): String =
        replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")

    /** Opens the shade so the screenshot shows what the user is left with. */
    private fun showShade(name: String) {
        val device = UiDevice.getInstance(instrumentation)
        device.openNotification()
        device.waitForIdle()
        compose.captureScreen(name)
        device.pressBack()
    }

    private companion object {
        const val TIMEOUT_MS = 60_000L
    }
}
