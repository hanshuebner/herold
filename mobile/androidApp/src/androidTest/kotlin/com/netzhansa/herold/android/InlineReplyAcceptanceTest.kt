package com.netzhansa.herold.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.os.Bundle
import androidx.core.app.RemoteInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import com.netzhansa.herold.android.push.MailNotifier
import com.netzhansa.herold.android.ui.settings.UndoSendPreference
import com.netzhansa.herold.android.ui.settings.UndoSendWindow
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Inline reply from the notification shade (REQ-AND-PUSH-21, issue #400):
 * the user types in the shade and the reply is written, queued in the
 * outbox under the undo window and carried to the recipient with the app
 * closed.
 *
 * No activity rule - the whole point is that the app is not on screen.
 * The reply goes through the action's own `PendingIntent` carrying
 * `RemoteInput` results, which is exactly what the platform sends when
 * the user taps the shade's send arrow.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class InlineReplyAcceptanceTest {

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val context: Context get() = instrumentation.targetContext.applicationContext

    private val app get() = context as HeroldApplication

    private val device get() = UiDevice.getInstance(instrumentation)

    private val notifications: NotificationManager
        get() = context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    private lateinit var subject: String

    @Before
    fun signedInWithAMessageToAnswer() {
        grantNotificationPermission()
        notifications.cancelAll()
        device.pressHome()
        UndoSendPreference.remember(context, UndoSendWindow.FIVE)
        val result = runBlocking {
            app.container.signInWithPassword(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
        }
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        subject = DevInstance.deliverMail(
            subject = "Inline reply ${System.currentTimeMillis()}",
            from = "Bob Example <${DevInstance.recipientEmail}>",
            body = "Can you confirm the schedule?",
        )
        runBlocking { DevInstance.awaitFiled(subject) }
        awaitInbox(subject)
    }

    @After
    fun backToTheLauncher() {
        device.pressBack()
        device.pressHome()
    }

    /** The shade draws the reply field the action's `RemoteInput` asks for. */
    @Test
    fun t76_theShadeOffersAReplyField() {
        postNotificationFor(awaitInbox(subject))
        device.openNotification()
        assertTrue(
            "the notification must be in the shade",
            device.wait(Until.hasObject(By.textContains(subject)), TIMEOUT_MS),
        )
        val reply = device.wait(Until.findObject(By.text(MailNotifier.REPLY_TITLE)), TIMEOUT_MS)
        assertNotNull("the shade must carry a Reply action", reply)
        reply!!.click()
        assertTrue(
            "tapping Reply must open the inline reply field",
            device.wait(Until.hasObject(By.clazz("android.widget.EditText")), TIMEOUT_MS),
        )
        captureDeviceScreen("16-shade-inline-reply-field")
    }

    /**
     * The end-to-end path: the typed text becomes an outbox send that
     * reaches the recipient, and the notification reports it.
     */
    @Test
    fun t77_aTypedReplyIsQueuedAndReachesTheRecipient() {
        val target = awaitInbox(subject)
        val posted = postNotificationFor(target)
        val action = posted.notification.actions.orEmpty()
            .first { it.remoteInputs?.isNotEmpty() == true }

        val text = "Confirmed, the schedule works."
        val results = Bundle().apply { putCharSequence(MailNotifier.REPLY_RESULT_KEY, text) }
        val fillIn = Intent()
        RemoteInput.addResultsToIntent(
            action.remoteInputs!!.map {
                RemoteInput.Builder(it.resultKey).setLabel(it.label).build()
            }.toTypedArray(),
            fillIn,
            results,
        )
        action.actionIntent.send(context, 0, fillIn)

        // The reply is durable before anything leaves the device, and it
        // waits out the undo window there.
        val queued = awaitOutboxEntry("Re: " + target.subject)
        assertTrue("the queued reply must be a send, saw $queued", queued.kind.name == "SEND")

        // The shade follows it: sending while it is held, then sent.
        assertTrue(
            "the notification must report the reply as sending",
            awaitNotificationTitle(MailNotifier.SENDING_TITLE),
        )
        device.openNotification()
        captureDeviceScreen("17-shade-reply-sending")
        device.pressBack()

        val arrived = runBlocking { awaitDelivered("Re: " + target.subject) }
        assertTrue(
            "the reply must quote the message it answers",
            arrived.inReplyTo.isNotEmpty(),
        )
        assertTrue(
            "the reply must carry the typed text, saw \"${arrived.bodyText}\"",
            arrived.bodyText.orEmpty().contains("Confirmed, the schedule works"),
        )
        assertTrue(
            "the notification must report the reply as sent",
            awaitNotificationTitle(MailNotifier.SENT_TITLE),
        )
        device.openNotification()
        captureDeviceScreen("18-shade-reply-sent")
    }

    /** Injects the push for [email] and returns the notification it posted. */
    private fun postNotificationFor(email: Email): android.service.notification.StatusBarNotification {
        notifications.cancelAll()
        injectPush(context, payloadFor(email))
        val posted = waitForNotification()
        assertNotNull("no notification was posted for the push", posted)
        return posted!!
    }

    private fun waitForNotification(): android.service.notification.StatusBarNotification? {
        val deadline = System.currentTimeMillis() + POST_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            notifications.activeNotifications.firstOrNull {
                it.notification.flags and Notification.FLAG_GROUP_SUMMARY == 0
            }?.let { return it }
            Thread.sleep(POLL_MS)
        }
        return null
    }

    /** True once the thread's notification carries [title]. */
    private fun awaitNotificationTitle(title: String): Boolean {
        val deadline = System.currentTimeMillis() + STATUS_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val shown = notifications.activeNotifications.any {
                it.notification.extras.getCharSequence(Notification.EXTRA_TITLE)?.toString() == title
            }
            if (shown) return true
            Thread.sleep(POLL_MS)
        }
        return false
    }

    private fun awaitOutboxEntry(subject: String) = runBlocking {
        repeat(OUTBOX_POLLS) {
            app.container.outbox.list().firstOrNull { it.label.contains(subject) }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("no outbox entry for a reply to \"$subject\", saw ${app.container.outbox.list()}")
    }

    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(INBOX_POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS * 4)
        }
        error("the seeded message \"$subject\" never reached the inbox")
    }

    private suspend fun awaitDelivered(subject: String): Email {
        repeat(DELIVERY_POLLS) {
            delivered(subject)?.let { return it }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no reply with subject \"$subject\" reached ${DevInstance.recipientEmail}")
    }

    private suspend fun delivered(subject: String): Email? {
        val recipient = DevInstance.recipientClient()
        val accountId = recipient.session().mailAccountId!!
        val ids = recipient.emailQueryInbox(
            accountId,
            recipient.mailboxGet(accountId).list.first { it.role == MailboxRoles.INBOX }.id,
            20,
        )
        return recipient.emailGet(accountId, ids, withBody = true).list
            .map { it.toStoreRow(accountId) }
            .firstOrNull { it.subject == subject }
    }

    private fun payloadFor(email: Email): String = """
        {"@type":"StateChange","changed":{"${email.accountId}":{"Email":"1"}},
         "kind":"mail","type":"email","from":"${email.senderDisplay}",
         "body":"${email.subject.jsonEscaped()}","subject":"${email.subject.jsonEscaped()}",
         "preview":"${email.preview.take(80).jsonEscaped()}","mailbox":"Inbox",
         "emailId":"${email.id}","msgid":"${email.id}","threadId":"${email.threadId}"}
    """.trimIndent()

    private fun String.jsonEscaped(): String =
        replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")

    private companion object {
        const val TIMEOUT_MS = 15_000L
        const val POST_TIMEOUT_MS = 15_000L
        const val STATUS_TIMEOUT_MS = 60_000L
        const val POLL_MS = 250L
        const val OUTBOX_POLLS = 40
        const val INBOX_POLLS = 30
        const val DELIVERY_POLLS = 40
        const val DELIVERY_POLL_MS = 1_000L
    }
}
