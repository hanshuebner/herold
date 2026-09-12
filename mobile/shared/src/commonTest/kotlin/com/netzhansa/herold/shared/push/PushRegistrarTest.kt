package com.netzhansa.herold.shared.push

import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.FcmSubscriptionCreate
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.jmap.UnifiedPushSubscriptionCreate
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs
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
        val (created, destroy) = api.pushSetCalls.single()
        val create = assertIs<FcmSubscriptionCreate>(created)
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
        val (created, destroy) = api.pushSetCalls.last()
        val create = assertIs<FcmSubscriptionCreate>(created)
        assertEquals("fcm-token-b", create.fcmToken)
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

    // -- UnifiedPush (REQ-AND-PUSH-04) ---------------------------------

    private val keys = WebPushKeys.generate()

    @Test
    fun registersTheDistributorEndpointWithTheSubscriptionKeys() = runTest {
        api.pushSubscriptionId = "9"

        val outcome = registrar.registerUnifiedPush("https://push.example.test/UP/abc", keys)

        assertEquals(RegistrationOutcome.Registered("9"), outcome)
        val (created, destroy) = api.pushSetCalls.single()
        val create = assertIs<UnifiedPushSubscriptionCreate>(created)
        assertEquals("device-1", create.deviceClientId)
        assertEquals("https://push.example.test/UP/abc", create.endpoint)
        assertEquals(keys.p256dh, create.p256dh)
        assertEquals(keys.auth, create.auth)
        assertEquals(listOf("Email", "Message", "Conversation"), create.types)
        assertTrue(destroy.isEmpty())

        val stored = store.pushRegistration()!!
        assertEquals("unifiedpush", stored.transport)
        // Neither the endpoint nor any key material reaches the database.
        assertTrue(!stored.tokenFingerprint.contains("push.example.test"))
    }

    @Test
    fun reRegisteringTheSameEndpointMakesNoCall() = runTest {
        registrar.registerUnifiedPush("https://push.example.test/UP/abc", keys)
        val outcome = registrar.registerUnifiedPush("https://push.example.test/UP/abc", keys)

        assertEquals(RegistrationOutcome.Unchanged, outcome)
        assertEquals(1, api.pushSetCalls.size)
    }

    @Test
    fun aRotatedEndpointReRegistersAndDestroysTheStaleSubscription() = runTest {
        api.pushSubscriptionId = "9"
        registrar.registerUnifiedPush("https://push.example.test/UP/abc", keys)
        api.pushSubscriptionId = "10"

        val outcome = registrar.registerUnifiedPush("https://push.example.test/UP/def", keys)

        assertEquals(RegistrationOutcome.Registered("10"), outcome)
        val (created, destroy) = api.pushSetCalls.last()
        assertEquals("https://push.example.test/UP/def", assertIs<UnifiedPushSubscriptionCreate>(created).endpoint)
        assertEquals(listOf("9"), destroy)
    }

    @Test
    fun switchingTransportRegistersAnewAndDestroysTheOldSubscription() = runTest {
        api.pushSubscriptionId = "7"
        registrar.register("fcm-token-a")
        api.pushSubscriptionId = "11"

        val outcome = registrar.registerUnifiedPush("https://push.example.test/UP/abc", keys)

        assertEquals(RegistrationOutcome.Registered("11"), outcome)
        assertEquals(listOf("7"), api.pushSetCalls.last().second)
        assertEquals("unifiedpush", store.pushRegistration()!!.transport)

        api.pushSubscriptionId = "12"
        assertEquals(RegistrationOutcome.Registered("12"), registrar.register("fcm-token-a"))
        assertEquals(listOf("11"), api.pushSetCalls.last().second)
        assertEquals("fcm", store.pushRegistration()!!.transport)
    }

    @Test
    fun aBlankEndpointIsNotRegistered() = runTest {
        assertTrue(registrar.registerUnifiedPush("", keys) is RegistrationOutcome.Failed)
        assertTrue(api.pushSetCalls.isEmpty())
    }
}
