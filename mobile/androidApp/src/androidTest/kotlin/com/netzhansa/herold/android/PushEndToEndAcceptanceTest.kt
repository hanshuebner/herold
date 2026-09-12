package com.netzhansa.herold.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.android.push.MailNotifier
import com.netzhansa.herold.shared.push.RegistrationOutcome
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import java.net.HttpURLConnection
import java.net.URL

/**
 * The whole push path against a real herold, with Google's leg replaced by
 * the in-tree fake FCM every dev instance runs (`scripts/dev-instance.sh`,
 * re #334): the client registers its token, a mail is delivered over SMTP,
 * herold's dispatcher sends the push, and the payload the fake recorded -
 * the bytes the server actually emitted, not a fixture - is handed to the
 * messaging service, which renders the notification.
 *
 * This is what makes the payload contract self-checking: a dispatcher-side
 * field rename fails here rather than on the maintainer's phone.
 */
@RunWith(AndroidJUnit4::class)
class PushEndToEndAcceptanceTest {

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val notifications: NotificationManager
        get() = instrumentation.targetContext
            .getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    private val token = "fcm-e2e-${System.currentTimeMillis()}"

    @Before
    fun signedInAndRegistered() {
        grantNotificationPermission()
        notifications.cancelAll()
        val result = runBlocking {
            app.container.signInWithPassword(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
        }
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        runBlocking {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.session.value!!.pushRegistrar.unregister()
        }
    }

    @After
    fun subscriptionRemoved() {
        runBlocking { app.container.session.value?.pushRegistrar?.unregister() }
    }

    @Test
    fun aDeliveredMailReachesTheDeviceAsANotification() = runBlocking {
        val fake = DevInstance.fakeFcmAddr
        clearFakeMessages(fake)

        val registration = app.container.session.value!!.pushRegistrar.register(token)
        assertTrue("registration rejected: $registration", registration is RegistrationOutcome.Registered)

        // herold's verification handshake reaches the fake first; the
        // registrar already echoed the code the create returned, so the
        // subscription is eligible for delivery.
        val handshake = awaitMessage(fake) { it.data.containsKey("verification") }
        assertNotNull("no PushVerification handshake was sent", handshake)

        val subject = DevInstance.deliverMail(
            subject = "End to end ${System.currentTimeMillis()}",
            body = "The push should carry this message's sender, subject and preview.",
        )

        val push = awaitAny(fake) { message ->
            message.data["payload"]?.contains(subject) == true
        }
        assertNotNull("herold sent no push for the delivered mail", push)
        assertEquals("mail wakes the device promptly", "HIGH", push!!.priority)

        // The bytes the server emitted, handed to the service as the SDK
        // would hand them over.
        injectPush(instrumentation.targetContext.applicationContext, push.data.getValue("payload"))

        val posted = awaitNotification()
        assertNotNull("the push did not render as a notification", posted)
        val title = posted!!.extras.getString(Notification.EXTRA_TITLE).orEmpty()
        val body = posted.extras.getString(Notification.EXTRA_TEXT).orEmpty()
        assertTrue("the sender's display name is the title: $title", title.contains("Bob"))
        assertTrue("the subject is the body: $body", body.contains(subject))
        assertEquals("mail", posted.channelId)
        captureDeviceScreen("13-end-to-end-push")
    }

    /**
     * The presentation bullet list of issue #348, end to end: a message
     * with an image attachment, delivered over SMTP, pushed by herold,
     * recorded by the fake, and rendered by the device - app icon, decoded
     * sender, avatar, subject and preview, attachment chips, Reply.
     */
    @Test
    fun aMailWithAnImageAttachmentRendersTheGmailFields() = runBlocking {
        val fake = DevInstance.fakeFcmAddr
        clearFakeMessages(fake)

        val registration = app.container.session.value!!.pushRegistrar.register(token)
        assertTrue("registration rejected: $registration", registration is RegistrationOutcome.Registered)
        assertNotNull(
            "no PushVerification handshake was sent",
            awaitMessage(fake) { it.data.containsKey("verification") },
        )

        // An encoded-word display name, which is what an older server puts
        // in the payload verbatim and the client decodes (issue #347).
        val subject = "Photo ${System.currentTimeMillis()}"
        DevInstance.deliverMailWithImage(
            subject = subject,
            bytes = sampleJpeg(),
            name = "beach.jpg",
            inline = false,
            from = "=?utf-8?B?QsO2YiBFeGFtcGxl?= <bob@example.local>",
        )

        val push = awaitAny(fake) { it.data["payload"]?.contains(subject) == true }
        assertNotNull("herold sent no push for the delivered mail", push)
        injectPush(instrumentation.targetContext.applicationContext, push!!.data.getValue("payload"))

        val posted = awaitNotification()
        assertNotNull("the push did not render as a notification", posted)
        val title = posted!!.extras.getString(Notification.EXTRA_TITLE).orEmpty()
        assertTrue("the sender is decoded into the title: $title", title.contains("B\u00F6b Example"))
        assertEquals("the subject is the body", subject, posted.extras.getString(Notification.EXTRA_TEXT))
        assertNotNull("the sender's avatar is the large icon", posted.getLargeIcon())
        assertNotNull("the attachment chips need the custom expanded view", posted.bigContentView)

        val actions = posted.actions.orEmpty().map { it.title.toString() }
        assertEquals(
            listOf(MailNotifier.ARCHIVE_TITLE, MailNotifier.MARK_READ_TITLE, MailNotifier.REPLY_TITLE),
            actions,
        )

        // The chips are drawn from the attachment list the bounded
        // reconcile fetched, so it must be in the store by now.
        val stored = app.container.store.inboxEmails().first().first { it.subject == subject }
        assertTrue(
            "the reconcile must have fetched the attachment list",
            stored.attachments.any { !it.isInline && it.name == "beach.jpg" },
        )

        // The heads-up is the collapsed presentation; the shade, expanded,
        // is where the chips show.
        captureDeviceScreen("15-attachment-push-collapsed")
        showExpandedShade("16-attachment-push-expanded")
    }

    /** A small JPEG, so the chip has a real thumbnail to draw. */
    private fun sampleJpeg(): ByteArray {
        val bitmap = android.graphics.Bitmap.createBitmap(240, 160, android.graphics.Bitmap.Config.ARGB_8888)
        android.graphics.Canvas(bitmap).apply {
            drawColor(0xFF2E7D32.toInt())
            drawCircle(80f, 80f, 50f, android.graphics.Paint().apply { color = 0xFFFFC107.toInt() })
        }
        val out = java.io.ByteArrayOutputStream()
        bitmap.compress(android.graphics.Bitmap.CompressFormat.JPEG, 90, out)
        return out.toByteArray()
    }

    /** Opens the shade and expands the notification for the screenshot. */
    private fun showExpandedShade(name: String) {
        val device = androidx.test.uiautomator.UiDevice.getInstance(instrumentation)
        device.openNotification()
        device.waitForIdle()
        val expander = device.findObject(androidx.test.uiautomator.By.res("android", "expand_button"))
        expander?.click()
        device.waitForIdle()
        captureDeviceScreen(name)
        device.pressBack()
    }

    @Test
    fun aTokenFcmReportsAsUnregisteredCostsTheSubscription() = runBlocking {
        val fake = DevInstance.fakeFcmAddr
        clearFakeMessages(fake)
        val registration = app.container.session.value!!.pushRegistrar.register(token)
        assertTrue("registration rejected: $registration", registration is RegistrationOutcome.Registered)
        val id = (registration as RegistrationOutcome.Registered).subscriptionId

        // What a reinstall looks like from the server's side: FCM answers
        // the next send for this token with UNREGISTERED, and herold drops
        // the subscription rather than sending to a dead token.
        unregisterAtFake(fake, token)
        DevInstance.deliverMail(
            subject = "Stale token ${System.currentTimeMillis()}",
            body = "This send is answered with 404 UNREGISTERED.",
        )

        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        var live = true
        while (System.currentTimeMillis() < deadline && live) {
            live = subscriptionIds().contains(id)
            if (live) Thread.sleep(POLL_MS)
        }
        assertTrue("herold must drop a subscription FCM reports as unregistered", !live)
    }

    /** Every subscription id herold holds for this principal. */
    private suspend fun subscriptionIds(): List<String> {
        val client = app.container.session.value!!.client
        val response = client.batch(
            listOf(
                com.netzhansa.herold.shared.jmap.JmapClient.MethodCall(
                    "PushSubscription/get",
                    kotlinx.serialization.json.buildJsonObject { },
                    "c0",
                ),
            ),
            listOf(com.netzhansa.herold.shared.jmap.Capability.CORE),
        ).single()
        return response.args["list"]?.jsonArray.orEmpty()
            .map { it.jsonObject["id"]?.jsonPrimitive?.content.orEmpty() }
    }

    private fun unregisterAtFake(addr: String, token: String) {
        val connection = URL("http://$addr/unregister").openConnection() as HttpURLConnection
        connection.requestMethod = "POST"
        connection.doOutput = true
        connection.outputStream.use { it.write("""{"token":"$token"}""".toByteArray()) }
        connection.inputStream.use { it.readBytes() }
    }

    // ---- the fake's status API ----------------------------------------

    private data class FakeMessage(
        val token: String,
        val data: Map<String, String>,
        val priority: String,
    )

    private fun clearFakeMessages(addr: String) {
        val connection = URL("http://$addr/messages").openConnection() as HttpURLConnection
        connection.requestMethod = "DELETE"
        connection.inputStream.use { it.readBytes() }
    }

    /** Waits for a recorded message for this install's token that matches. */
    private fun awaitMessage(addr: String, matches: (FakeMessage) -> Boolean): FakeMessage? =
        awaitAny(addr) { it.token == token && matches(it) }

    /**
     * Waits for any recorded message that matches, whichever subscription
     * it went to. The FCM SDK can hand the app a rotated token while a
     * test's own token is registered, and a rotation replaces this
     * install's subscription; the payload herold emitted is the same
     * either way, and the payload is what this checks.
     */
    private fun awaitAny(addr: String, matches: (FakeMessage) -> Boolean): FakeMessage? {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            fakeMessages(addr).firstOrNull(matches)?.let { return it }
            Thread.sleep(POLL_MS)
        }
        return null
    }

    private fun fakeMessages(addr: String): List<FakeMessage> {
        val text = URL("http://$addr/messages").readText()
        return Json.parseToJsonElement(text).jsonArray.map { element ->
            val row = element.jsonObject
            FakeMessage(
                token = row["token"]?.jsonPrimitive?.content.orEmpty(),
                data = row["data"]?.jsonObject.orEmpty()
                    .mapValues { (_, value) -> value.jsonPrimitive.content },
                priority = row["android_priority"]?.jsonPrimitive?.content.orEmpty(),
            )
        }
    }

    private fun awaitNotification(): Notification? {
        val deadline = System.currentTimeMillis() + POST_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            notifications.activeNotifications.firstOrNull {
                it.notification.flags and Notification.FLAG_GROUP_SUMMARY == 0
            }?.let { return it.notification }
            Thread.sleep(POLL_MS)
        }
        return null
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val POST_TIMEOUT_MS = 10_000L
        const val POLL_MS = 250L
    }
}
