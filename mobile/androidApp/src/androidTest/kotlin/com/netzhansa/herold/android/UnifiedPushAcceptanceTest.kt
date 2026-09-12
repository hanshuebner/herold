package com.netzhansa.herold.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.push.PushTransportChoice
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.io.BufferedReader
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.net.Socket

/**
 * The UnifiedPush transport end to end (issue #229), against a real
 * herold and a real distributor: the in-tree fake one
 * (`mobile/fakeDistributor`), which publishes its endpoint on a device
 * port the harness forwards to the host, so the dev instance POSTs to it
 * over the loopback its `[server.push.network]` allowlists.
 *
 * What is under test is everything between the two: the registration
 * herold accepts as kind="unifiedpush", the aes128gcm envelope the
 * server encrypts and the device decrypts, the notification it renders,
 * the 410 that costs a subscription, and the transport switch.
 *
 * Setup, once per emulator (see mobile/README.md):
 *
 *     ./gradlew :fakeDistributor:installDebug
 *     adb shell am start -n com.netzhansa.herold.fakedistributor/.MainActivity
 *     adb forward tcp:19280 tcp:19280
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class UnifiedPushAcceptanceTest {

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val notifications: NotificationManager
        get() = instrumentation.targetContext
            .getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    @Before
    fun signedInWithADistributorInstalled() {
        grantNotificationPermission()
        notifications.cancelAll()
        assertTrue(
            "install the fake distributor first: ./gradlew :fakeDistributor:installDebug",
            app.container.push.distributors().contains(DISTRIBUTOR),
        )
        distributorControl("/control/live")
        runBlocking {
            if (app.container.session.value == null) {
                val result = app.container.signInWithPassword(
                    DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
                )
                assertTrue("sign-in failed: $result", result is SignInResult.Success)
            }
            app.container.session.value!!.syncEngine.syncAll()
            app.container.session.value!!.pushRegistrar.unregister()
        }
        app.container.push.choice = PushTransportChoice.UNIFIED_PUSH
    }

    @After
    fun subscriptionRemoved() {
        runBlocking { app.container.session.value?.pushRegistrar?.unregister() }
        distributorControl("/control/live")
    }

    /**
     * Registration: the endpoint the distributor hands out reaches herold
     * as a kind="unifiedpush" subscription, and the server reads it back
     * with the endpoint on it.
     */
    @Test
    fun t90_theDistributorEndpointIsRegisteredAsAUnifiedPushSubscription() = runBlocking {
        val id = registerOverUnifiedPush()

        val subscription = subscriptions().first { it.id == id }
        assertEquals("unifiedpush", subscription.kind)
        assertTrue(
            "the registered endpoint is the distributor's: ${subscription.url}",
            subscription.url.startsWith("http://127.0.0.1:19280/UP?token="),
        )
        assertEquals("", subscription.fcmToken)
        assertEquals("unifiedpush", app.container.store.pushRegistration()!!.transport)
    }

    /**
     * Delivery: a mail delivered over SMTP is pushed by herold to the
     * distributor's endpoint, decrypted on the device, and posted as the
     * notification the FCM transport posts.
     */
    @Test
    fun t91_aDeliveredMailArrivesThroughTheDistributorAsANotification() = runBlocking {
        registerOverUnifiedPush()
        val before = distributorDeliveries()
        notifications.cancelAll()

        val subject = "UnifiedPush ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            body = "This push goes through the distributor, encrypted end to end.",
        )

        val posted = awaitNotification(subject)
        assertNotNull("no notification arrived through the distributor", posted)
        val title = posted!!.extras.getString(Notification.EXTRA_TITLE).orEmpty()
        assertTrue("the sender is the title: $title", title.contains("Bob"))
        assertEquals("mail", posted.channelId)
        assertTrue(
            "the distributor must have carried the push",
            distributorDeliveries() > before,
        )
        captureDeviceScreen("90-unifiedpush-notification")
    }

    /** A distributor answering 410 costs the subscription (RFC 8030 section 7.3). */
    @Test
    fun t92_anEndpointAnsweringGoneCostsTheSubscription() = runBlocking {
        val id = registerOverUnifiedPush()
        distributorControl("/control/gone")

        DevInstance.deliverMail(
            subject = "Gone ${System.currentTimeMillis()}",
            body = "The distributor answers 410 to this one.",
        )

        val dropped = await(GONE_TIMEOUT_MS) { subscriptions().none { it.id == id } }
        assertTrue("herold kept a subscription whose endpoint answered 410", dropped)
    }

    /**
     * Switching the transport in settings: the subscription held over the
     * old transport is destroyed and a new one takes its place.
     */
    @Test
    fun t93_switchingTheTransportReRegistersAndDropsTheOldSubscription() = runBlocking {
        val unifiedPushId = registerOverUnifiedPush()

        app.container.push.switchTransport(PushTransportChoice.FCM)
        assertTrue(
            "the UnifiedPush subscription must be gone",
            await(TIMEOUT_MS) { subscriptions().none { it.id == unifiedPushId } },
        )
        val fcm = app.container.store.pushRegistration()
        // A checkout without Firebase credentials cannot register over
        // FCM; the run says which half it exercised.
        android.util.Log.i("herold.acceptance", "fcmAvailable=${app.container.push.fcmAvailable}")
        if (app.container.push.fcmAvailable) {
            assertNotNull("the FCM registration replaces it", fcm)
            assertEquals("fcm", fcm!!.transport)
            assertEquals("fcm", subscriptions().first { it.id == fcm.subscriptionId }.kind)
        }
        assertNull("the distributor's endpoint is given back", app.container.tokenStore.pushEndpoint())

        // And back: a second switch registers over the distributor again,
        // with an id of its own.
        app.container.push.switchTransport(PushTransportChoice.UNIFIED_PUSH)
        val back = awaitUnifiedPushRegistration()
        assertTrue("switching back registers anew", back != unifiedPushId)
        if (fcm != null) {
            assertTrue(
                "the FCM subscription must be gone",
                subscriptions().none { it.id == fcm.subscriptionId },
            )
        }
    }

    // -- the harness ----------------------------------------------------

    /** Asks the distributor for an endpoint and waits for herold to hold it. */
    private suspend fun registerOverUnifiedPush(): String {
        app.container.push.registerCurrentTransport()
        return awaitUnifiedPushRegistration()
    }

    private suspend fun awaitUnifiedPushRegistration(): String {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val held = app.container.store.pushRegistration()
            if (held != null && held.transport == "unifiedpush") return held.subscriptionId
            Thread.sleep(POLL_MS)
        }
        error("no UnifiedPush registration within ${TIMEOUT_MS}ms")
    }

    private suspend fun await(timeoutMs: Long, condition: suspend () -> Boolean): Boolean {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < deadline) {
            if (condition()) return true
            Thread.sleep(POLL_MS)
        }
        return false
    }

    /** The notification the delivery posts, or null when none arrives in time. */
    private fun awaitNotification(subject: String): Notification? {
        val deadline = System.currentTimeMillis() + NOTIFICATION_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val posted = notifications.activeNotifications.firstOrNull { active ->
                active.notification.extras.getString(Notification.EXTRA_TEXT).orEmpty().contains(subject)
            }
            if (posted != null) return posted.notification
            Thread.sleep(POLL_MS)
        }
        return null
    }

    private data class Subscription(
        val id: String,
        val kind: String,
        val url: String,
        val fcmToken: String,
    )

    /** Every subscription herold holds for this principal. */
    private suspend fun subscriptions(): List<Subscription> {
        val client: JmapClient = app.container.session.value!!.client
        val response = client.batch(
            listOf(JmapClient.MethodCall("PushSubscription/get", buildJsonObject { }, "c0")),
            listOf(Capability.CORE),
        ).single()
        return response.args["list"]?.jsonArray.orEmpty().map { element ->
            val row = element.jsonObject
            Subscription(
                id = row["id"]?.jsonPrimitive?.content.orEmpty(),
                kind = row["kind"]?.jsonPrimitive?.content.orEmpty(),
                url = row["url"]?.jsonPrimitive?.content.orEmpty(),
                fcmToken = row["fcmToken"]?.jsonPrimitive?.content.orEmpty(),
            )
        }
    }

    /** How many pushes the distributor has carried. */
    private fun distributorDeliveries(): Int =
        Regex("\"delivered\":(\\d+)").find(distributorControl("/control/state"))
            ?.groupValues?.get(1)?.toInt() ?: 0

    /**
     * The distributor's control surface, reached on the device's own
     * loopback - the same listener the host reaches through the forwarded
     * port.
     */
    private fun distributorControl(path: String): String =
        Socket("127.0.0.1", 19280).use { socket ->
            socket.soTimeout = 5_000
            OutputStreamWriter(socket.getOutputStream()).apply {
                write("POST $path HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
                flush()
            }
            BufferedReader(InputStreamReader(socket.getInputStream())).readText()
        }

    private companion object {
        const val DISTRIBUTOR = "com.netzhansa.herold.fakedistributor"
        const val TIMEOUT_MS = 30_000L

        /** The dispatcher polls the change feed, then retries the POST. */
        const val GONE_TIMEOUT_MS = 60_000L
        const val NOTIFICATION_TIMEOUT_MS = 60_000L
        const val POLL_MS = 500L
    }
}
