package com.netzhansa.herold.android

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.push.PushRegistrar
import com.netzhansa.herold.shared.push.RegistrationOutcome
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The registration half of REQ-AND-PUSH-01/02 against a real herold: the
 * `PushSubscription/set { create }` the client sends is accepted as a
 * kind="fcm" subscription, and a rotated token replaces it rather than
 * accumulating.
 *
 * The token is a synthetic string. Nothing here needs the Firebase SDK -
 * what is under test is the wire shape herold answers, which is the part
 * that can drift.
 */
@RunWith(AndroidJUnit4::class)
class PushRegistrationAcceptanceTest {

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private lateinit var registrar: PushRegistrar
    private lateinit var client: JmapClient

    @Before
    fun signedIn() {
        val result = runBlocking {
            app.container.signIn(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
        }
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        val session = app.container.session.value!!
        registrar = session.pushRegistrar
        client = session.client
        runBlocking { registrar.unregister() }
    }

    @After
    fun subscriptionRemoved() {
        runBlocking { registrar.unregister() }
    }

    @Test
    fun theFcmTokenIsAcceptedAsASubscriptionAndARotationReplacesIt() = runBlocking {
        val first = registrar.register("fcm-token-first")
        assertTrue("registration rejected: $first", first is RegistrationOutcome.Registered)
        val firstId = (first as RegistrationOutcome.Registered).subscriptionId

        val registered = subscriptions().first { it.first == firstId }
        assertEquals("fcm", registered.second)
        assertEquals("fcm-token-first", registered.third)
        assertEquals(firstId, app.container.store.pushRegistration()!!.subscriptionId)

        // Re-registering the same token is a no-op.
        assertEquals(RegistrationOutcome.Unchanged, registrar.register("fcm-token-first"))

        val second = registrar.register("fcm-token-second")
        assertTrue("re-registration rejected: $second", second is RegistrationOutcome.Registered)
        val secondId = (second as RegistrationOutcome.Registered).subscriptionId

        val live = subscriptions()
        assertTrue("the stale subscription must be gone", live.none { it.first == firstId })
        assertEquals("fcm-token-second", live.first { it.first == secondId }.third)

        registrar.unregister()
        assertTrue("unregister must destroy it", subscriptions().none { it.first == secondId })
        assertNull(app.container.store.pushRegistration())
    }

    /** Every subscription herold holds for this principal: id, kind, token. */
    private suspend fun subscriptions(): List<Triple<String, String, String>> {
        val response = client.batch(
            listOf(JmapClient.MethodCall("PushSubscription/get", buildJsonObject { }, "c0")),
            listOf(Capability.CORE),
        ).single()
        return response.args["list"]?.jsonArray.orEmpty().map { element ->
            val row = element.jsonObject
            Triple(
                row["id"]?.jsonPrimitive?.content.orEmpty(),
                row["kind"]?.jsonPrimitive?.content.orEmpty(),
                row["fcmToken"]?.jsonPrimitive?.content.orEmpty(),
            )
        }
    }
}
