package com.netzhansa.herold.shared.push

import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.JmapException
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The registration request shape and the rotation rule (REQ-AND-PUSH-01/02),
 * against the scripted JMAP fake.
 */
class PushRegistrarTest {

    private val api = FakeJmapApi()
    private val store = FakeLocalStore()
    private val registrar = PushRegistrar(
        api = api,
        store = store,
        now = { 1_700_000_000_000 },
        newDeviceClientId = { "device-1" },
    )

    @Test
    fun registersTheTokenAsAnFcmSubscriptionAndRemembersTheId() = runTest {
        api.pushSubscriptionId = "7"

        val outcome = registrar.register("fcm-token-a")

        assertEquals(RegistrationOutcome.Registered("7"), outcome)
        val (create, destroy) = api.pushSetCalls.single()
        assertNotNull(create)
        assertEquals("device-1", create.deviceClientId)
        assertEquals("fcm-token-a", create.fcmToken)
        assertEquals(listOf("Email", "Message", "Conversation"), create.types)
        // Parity with the Suite's registration: rules and quiet hours are
        // left to the server's defaults until a settings surface sets them.
        assertNull(create.notificationRules)
        assertNull(create.quietHours)
        assertTrue(destroy.isEmpty())

        val stored = store.pushRegistration()!!
        assertEquals("7", stored.subscriptionId)
        assertEquals("device-1", stored.deviceClientId)
        assertEquals(PushRegistrar.fingerprint("fcm-token-a"), stored.tokenFingerprint)
        // The token itself is never persisted.
        assertTrue(!stored.tokenFingerprint.contains("fcm-token-a"))
    }

    @Test
    fun reRegisteringTheSameTokenMakesNoCall() = runTest {
        registrar.register("fcm-token-a")
        val outcome = registrar.register("fcm-token-a")

        assertEquals(RegistrationOutcome.Unchanged, outcome)
        assertEquals(1, api.pushSetCalls.size)
    }

    @Test
    fun aRotatedTokenReRegistersAndDestroysTheStaleSubscription() = runTest {
        api.pushSubscriptionId = "7"
        registrar.register("fcm-token-a")
        api.pushSubscriptionId = "8"

        val outcome = registrar.register("fcm-token-b")

        assertEquals(RegistrationOutcome.Registered("8"), outcome)
        val (create, destroy) = api.pushSetCalls.last()
        assertEquals("fcm-token-b", create!!.fcmToken)
        // The device client id is stable across a rotation.
        assertEquals("device-1", create.deviceClientId)
        assertEquals(listOf("7"), destroy)
        assertEquals("8", store.pushRegistration()!!.subscriptionId)
    }

    @Test
    fun aRejectedCreateLeavesNoStoredRegistration() = runTest {
        api.pushRejection = "fcmToken is required when kind is \"fcm\""

        val outcome = registrar.register("fcm-token-a")

        assertTrue(outcome is RegistrationOutcome.Rejected)
        assertNull(store.pushRegistration())
    }

    @Test
    fun anUnreachableServerLeavesTheRegistrationForTheNextAttempt() = runTest {
        api.pushFailure = JmapException("connection refused")

        val outcome = registrar.register("fcm-token-a")

        assertTrue(outcome is RegistrationOutcome.Failed)
        assertNull(store.pushRegistration())

        api.pushFailure = null
        assertTrue(registrar.register("fcm-token-a") is RegistrationOutcome.Registered)
    }

    @Test
    fun unregisterDestroysTheSubscriptionAndDropsTheRow() = runTest {
        api.pushSubscriptionId = "7"
        registrar.register("fcm-token-a")

        registrar.unregister()

        assertEquals(listOf("7"), api.pushSetCalls.last().second)
        assertNull(api.pushSetCalls.last().first)
        assertNull(store.pushRegistration())
    }

    @Test
    fun theVerificationCodeTheCreateReturnedIsEchoedBackAtOnce() = runTest {
        api.pushSubscriptionId = "7"
        api.pushVerificationCode = "code-123"

        registrar.register("fcm-token-a")

        val patch = api.pushUpdateCalls.single()
        assertEquals(setOf("7"), patch.keys)
        assertEquals("code-123", patch.getValue("7")["verificationCode"]?.jsonPrimitive?.content)
    }

    @Test
    fun theHandshakeArrivingOverThePushChannelIsEchoedBackToo() = runTest {
        api.pushSubscriptionId = "7"
        registrar.register("fcm-token-a")

        assertTrue(registrar.confirmVerification("7", "code-from-push"))

        val patch = api.pushUpdateCalls.single()
        assertEquals("code-from-push", patch.getValue("7")["verificationCode"]?.jsonPrimitive?.content)
    }

    @Test
    fun aBlankTokenIsNotRegistered() = runTest {
        assertTrue(registrar.register("") is RegistrationOutcome.Failed)
        assertTrue(api.pushSetCalls.isEmpty())
    }
}
