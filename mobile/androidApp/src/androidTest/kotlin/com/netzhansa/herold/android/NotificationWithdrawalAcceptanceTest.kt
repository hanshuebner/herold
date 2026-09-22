package com.netzhansa.herold.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.UiDevice
import com.netzhansa.herold.android.diag.DiagLog
import com.netzhansa.herold.android.push.ActiveThread
import com.netzhansa.herold.android.push.PostedMailNotifications
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.push.MailNotification
import com.netzhansa.herold.shared.push.RegistrationOutcome
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.After
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
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
 * what the Suite or a second device is to the phone. Two paths carry it:
 * the client's own sync fold, and the `mail-dismiss` push herold's
 * dispatcher sends, taken from the in-tree fake FCM as the bytes the
 * server emitted.
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

    /** This run's FCM token, so the dev instance pushes to it alone. */
    private val token = "fcm-dismiss-${System.currentTimeMillis()}"

    @Before
    fun signedInWithAFreshMessage() {
        grantNotificationPermission()
        notifications.cancelAll()
        DiagLog.keepLog = true
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

    @After
    fun subscriptionRemoved() {
        runBlocking { app.container.session.value?.pushRegistrar?.unregister() }
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
        archive(server, accountId, target)

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

    /**
     * The push half of the contract, against the bytes herold's
     * dispatcher emitted: the message is read on a second JMAP session,
     * the `mail-dismiss` push the fake FCM recorded is handed to the
     * messaging service, and the notification goes without a sync pass.
     */
    @Test
    fun t04_theServersDismissPushForAReadMessageWithdrawsTheNotification() = runBlocking {
        withdrawnByDismissPush("seen") { server, accountId, target ->
            markSeen(server, accountId, target.id)
        }
    }

    /** The same for a message filed away on the other client. */
    @Test
    fun t05_theServersDismissPushForAnArchivedMessageWithdrawsTheNotification() = runBlocking {
        withdrawnByDismissPush("left-inbox") { server, accountId, target ->
            archive(server, accountId, target)
        }
    }

    /**
     * Drives one dismiss reason end to end. The message is provisioned,
     * pushed, notified and then resolved on the server by [resolve]; the
     * dismissal the dispatcher sends is read off the fake FCM and handed
     * to the messaging service.
     *
     * The withdrawal is attributed to that push alone: the radios are
     * down, so no fold can reach the server, and the local row is put
     * back to unread and in the inbox before the dismissal is injected -
     * the state under which the fold-time rule withdraws nothing. The
     * local row is still unread afterwards, and the push path's own line
     * is in the diagnostic ring.
     */
    private suspend fun withdrawnByDismissPush(
        reason: String,
        resolve: suspend (JmapClient, String, Email) -> Unit,
    ) {
        val fake = DevInstance.fakeFcmAddr
        FakeFcm.clear(fake)
        val registrar = app.container.session.value!!.pushRegistrar
        val registration = registrar.register(token)
        assertTrue("registration rejected: $registration", registration is RegistrationOutcome.Registered)
        assertNotNull(
            "no PushVerification handshake was sent",
            FakeFcm.await(fake) { it.data.containsKey("verification") },
        )

        val pushed = DevInstance.deliverMail(
            subject = "Dismiss push $reason ${System.currentTimeMillis()}",
            body = "The server withdraws this notification when another client resolves it.",
        )
        val arrival = FakeFcm.await(fake) { it.payload?.contains(pushed) == true }
        assertNotNull("herold sent no push for the delivered mail", arrival)
        injectPush(context, arrival!!.payload!!)
        awaitStored(pushed)
        val target = storedMessage(pushed)
        assertNotNull("the arrival push posted no notification", awaitNotificationFor(target))

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        resolve(server, accountId, target)

        val dismissal = FakeFcm.await(fake) { message ->
            val payload = message.payload ?: return@await false
            payload.contains("\"kind\":\"mail-dismiss\"") && payload.contains("\"emailId\":\"${target.id}\"")
        }
        assertNotNull("herold sent no mail-dismiss push for the resolved message", dismissal)
        assertTrue(
            "the dispatcher names the reason: ${dismissal!!.payload}",
            dismissal.payload!!.contains("\"reason\":\"$reason\""),
        )

        radios(up = false)
        try {
            // The state the fold-time rule leaves alone: unread, in the
            // inbox, and no way to learn otherwise.
            app.container.store.updateMembership(
                accountId = accountId,
                id = target.id,
                keywords = target.keywords - Keywords.SEEN,
                mailboxIds = target.mailboxIds,
                snoozedUntil = target.snoozedUntil,
            )
            injectPush(context, arrival.payload!!)
            assertNotNull(
                "the notification must be back before the dismissal",
                awaitNotificationFor(target),
            )
            DiagLog.ring.clear()

            injectPush(context, dismissal.payload!!)

            assertNull(
                "the server's dismissal withdraws the notification",
                awaitNoNotificationFor(target),
            )
            assertTrue(
                "the posted set no longer holds the thread",
                PostedMailNotifications.all(context).none { it.threadId == target.threadId },
            )
            val local = app.container.store.email(accountId, target.id)
            assertTrue(
                "the local row is still unread and in the inbox, so no fold withdrew it",
                local != null && local.isUnread && local.mailboxIds == target.mailboxIds,
            )
            assertTrue(
                "the push path records the withdrawal it performed: ${DiagLog.ring.lines()}",
                DiagLog.ring.lines().any {
                    it.ctx == "herold.push" && it.message == "withdrawing ${target.id}: $reason"
                },
            )
        } finally {
            radios(up = true)
        }
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

    /**
     * The thread's notification once the shade has it. A `notify` reaches
     * `activeNotifications` a moment later, so the wait is what the
     * device's own timing needs.
     */
    private fun awaitNotificationFor(email: Email): Notification? {
        val deadline = System.currentTimeMillis() + SHADE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            notificationFor(email)?.let { return it }
            Thread.sleep(POLL_MS)
        }
        return null
    }

    /** Null once the thread's notification has left the shade. */
    private fun awaitNoNotificationFor(email: Email): Notification? {
        val deadline = System.currentTimeMillis() + SHADE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (notificationFor(email) == null) return null
            Thread.sleep(POLL_MS)
        }
        return notificationFor(email)
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

    /** Files [target] away on the server, as the other client would. */
    private suspend fun archive(server: JmapClient, accountId: String, target: Email) {
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
    }

    /** The emulator's radios, which decide whether a fold can run at all. */
    private fun radios(up: Boolean) {
        val verb = if (up) "enable" else "disable"
        shellOut("svc wifi $verb")
        shellOut("svc data $verb")
        Thread.sleep(RADIO_SETTLE_MS)
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

        /** How long the radios are given to come up or go down. */
        const val RADIO_SETTLE_MS = 2_000L

        /** How long the shade is given to take a notification or let it go. */
        const val SHADE_TIMEOUT_MS = 10_000L
        const val POLL_MS = 250L
    }
}
