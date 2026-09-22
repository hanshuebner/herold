package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.auth.FakeHttp
import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import com.netzhansa.herold.shared.auth.StoredTokenProvider
import com.netzhansa.herold.shared.auth.respondJson
import com.netzhansa.herold.shared.fake.FakeConnectivityMonitor
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.JmapAccount
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.jmap.JmapSession
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

private val coordinatorTestDescriptor = JmapSession(
    capabilities = mapOf(Capability.MAIL to JsonObject(emptyMap())),
    accounts = mapOf(
        "acct-a" to JmapAccount(
            name = "alice",
            accountCapabilities = mapOf(Capability.MAIL to JsonObject(emptyMap())),
        ),
    ),
    primaryAccounts = mapOf(Capability.MAIL to "acct-a"),
    username = "alice@example.local",
    apiUrl = "https://mail.example/jmap",
)

private val coordinatorTestDescriptorJson =
    Json.encodeToString(JmapSession.serializer(), coordinatorTestDescriptor)

/**
 * Recovering from a run of host-resolution failures on a network the
 * platform still calls validated (issue #479).
 *
 * The reported phone kept failing to resolve the server's name while
 * its radio stayed up; the platform delivers a network reading only on
 * its own callback, and neither a resolution failure nor its recovery
 * prompts one. Without [ReachabilityCoordinator] a platform reading
 * that predates the run never updates, and the offline indication -
 * which needs both the platform's reading and the transport's own
 * record of reaching the server - stays latched even once the host
 * resolves again.
 */
class ReachabilityCoordinatorTest {

    @Test
    fun aRunOfResolutionFailuresThenARecoveredHostClearsOfflineWithoutAFreshPlatformCallback() = runTest {
        val reachability = Reachability()
        // The platform's last delivered reading, predating the run of
        // failures below and never updated by a fresh callback - the
        // shape the report's device log shows.
        val connectivity = FakeConnectivityMonitor(initial = false)
        ReachabilityCoordinator(reachability, connectivity, backgroundScope)

        var remainingFailures = 3
        val http = FakeHttp.client {
            if (remainingFailures > 0) {
                remainingFailures--
                throw RuntimeException("Unable to resolve host \"mail.example\": No address associated with hostname")
            }
            respondJson(coordinatorTestDescriptorJson)
        }
        val client = JmapClient(
            http,
            "https://mail.example",
            StoredTokenProvider(InMemoryTokenStore().apply { store("device-token-1") }),
            reachability,
        )

        val offline = combine(connectivity.online, reachability.reachable) { up, reached -> up && reached }
            .offlineIndication()

        // Three resolution failures, the run the report's 5s/10s/20s
        // backoff shows.
        repeat(3) { runCatching { client.session() } }
        assertTrue(offline.first(), "resolution kept failing on the platform's stale reading")

        // The host resolves again. No fresh platform callback arrives -
        // FakeConnectivityMonitor.set is never called in this test.
        client.session()
        assertTrue(reachability.reachable.value, "the transport reached the server")
        assertFalse(
            offline.first(),
            "a reach clears the indication even on a platform reading that predates the recovery",
        )
        // Once for the initial reachable state, once for the recovery.
        assertEquals(2, connectivity.reachableCalls)
        assertEquals(1, connectivity.unreachableCalls)
    }

    @Test
    fun aReachDoesNotMaskARadioThatIsGenuinelyDown() = runTest {
        val reachability = Reachability()
        val connectivity = FakeConnectivityMonitor(initial = true)
        ReachabilityCoordinator(reachability, connectivity, backgroundScope)

        val offline = combine(connectivity.online, reachability.reachable) { up, reached -> up && reached }
            .offlineIndication()
        assertFalse(offline.first(), "starts reachable on a validated network")

        // The radio itself goes down - a real platform callback, not a
        // transport failure.
        connectivity.set(false)
        assertTrue(offline.first(), "a platform callback reporting the network down is not overruled")
    }
}
