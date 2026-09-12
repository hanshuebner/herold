package com.netzhansa.herold.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import android.os.Build
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import com.netzhansa.herold.android.push.MailNotifier
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Tapping a notification opens the thread (REQ-AND-PUSH-13), from the state
 * the user is actually in when a push arrives: the app is not on screen.
 *
 * No activity rule here - the tap's `PendingIntent` is what starts the
 * shell, exactly as the system does when the user touches the notification,
 * and the result is read off the device.
 */
@RunWith(AndroidJUnit4::class)
class PushTapAcceptanceTest {

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val device get() = UiDevice.getInstance(instrumentation)

    private val notifications: NotificationManager
        get() = instrumentation.targetContext
            .getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    @Before
    fun signedInWithNotificationsAllowed() {
        grantNotificationPermission()
        notifications.cancelAll()
        device.pressHome()
        val result = runBlocking {
            app.container.signIn(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
        }
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        subject = DevInstance.deliverMail(
            subject = "Tap acceptance ${System.currentTimeMillis()}",
            body = "The quarterly report is ready for review.",
        )
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val found = runBlocking {
                app.container.session.value!!.syncEngine.syncAll()
                app.container.store.inboxEmails().first().any { it.subject == subject }
            }
            if (found) break
            Thread.sleep(POLL_MS)
        }
    }

    private lateinit var subject: String

    @After
    fun backToTheLauncher() {
        device.pressHome()
    }

    @Test
    fun tappingTheNotificationOpensTheThread() {
        val target = runBlocking {
            app.container.store.inboxEmails().first().first { it.subject == subject }
        }

        injectPush(instrumentation.targetContext.applicationContext, payloadFor(target))

        // The system posts asynchronously; give it a moment to appear.
        val posted = waitForNotification()
        assertNotNull("no notification was posted for the push", posted)

        posted!!.notification.contentIntent.send()

        assertTrue(
            "the reading pane must open on the thread the push named",
            device.wait(Until.hasObject(By.textContains(target.subject)), TIMEOUT_MS),
        )
        assertTrue(
            "the thread's own screen is showing, not the inbox",
            device.wait(Until.hasObject(By.desc("Back")), TIMEOUT_MS),
        )
        captureDeviceScreen("12-notification-tap-opens-thread")
    }

    /**
     * The shade's Reply action (REQ-AND-PUSH-21): the composer opens on
     * the message, addressed to its sender, with the quote in the body.
     */
    @Test
    fun theReplyActionOpensTheComposerOnTheMessage() {
        val target = runBlocking {
            app.container.store.inboxEmails().first().first { it.subject == subject }
        }

        injectPush(instrumentation.targetContext.applicationContext, payloadFor(target))
        val posted = waitForNotification()
        assertNotNull("no notification was posted for the push", posted)

        posted!!.notification.actions.orEmpty()
            .first { it.title.toString() == MailNotifier.REPLY_TITLE }
            .actionIntent.send()

        assertTrue(
            "the composer must open on a reply to the message",
            device.wait(Until.hasObject(By.textContains("Re: " + target.subject)), TIMEOUT_MS),
        )
        assertTrue(
            "the quote must be prepared in the body",
            device.wait(Until.hasObject(By.textContains("wrote:")), TIMEOUT_MS),
        )
        captureDeviceScreen("14-notification-reply-opens-compose")
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
        const val TIMEOUT_MS = 30_000L
        const val POST_TIMEOUT_MS = 10_000L
        const val POLL_MS = 250L
    }
}
