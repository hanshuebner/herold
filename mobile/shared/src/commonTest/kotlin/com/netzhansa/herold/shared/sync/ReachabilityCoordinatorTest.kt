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
import io.ktor.client.engine.mock.respond
import io.ktor.http.HttpStatusCode
import io.ktor.http.headersOf
import io.ktor.utils.io.ByteReadChannel
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

    /**
     * The defect on the seam as it stood before [ReachabilityCoordinator]:
     * a stale platform reading plus [Reachability] alone, combined the
     * way `AppContainer.offline` does. Nothing here writes the platform
     * reading back, so three resolution failures followed by a recovery
     * leave the indication latched - the shape the report's device log
     * shows and what the fix's coordinator exists to close.
     */
    @Test
    fun withoutTheCoordinatorAStaleReadingLatchesEvenAfterTheTransportRecovers() = runTest {
        val reachability = Reachability()
        val connectivity = FakeConnectivityMonitor(initial = false)

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

        repeat(3) { runCatching { client.session() } }
        client.session()
        assertTrue(reachability.reachable.value, "the transport reached the server")
        assertTrue(
            offline.first(),
            "with nothing to write the platform reading back, the stale reading outlives the recovery",
        )
    }

    /**
     * A response that completes the round trip but is not the server's
     * own answer - a captive portal's login page at 200 - must not
     * report the network validated (issue #479): [JmapClient.send]
     * marks [Reachability.reachable] on any answered status, which the
     * offline chip alone reads, but the platform report follows
     * [Reachability.confirmed], which only a decoded descriptor or
     * method response moves.
     */
    @Test
    fun aCaptivePortalPageAt200DoesNotReportTheNetworkValidated() = runTest {
        val reachability = Reachability()
        val connectivity = FakeConnectivityMonitor(initial = false)
        ReachabilityCoordinator(reachability, connectivity, backgroundScope)

        val http = FakeHttp.client {
            respond(
                content = ByteReadChannel("<html><body>Sign in to this network</body></html>"),
                status = HttpStatusCode.OK,
                headers = headersOf("Content-Type", "text/html"),
            )
        }
        val client = JmapClient(
            http,
            "https://mail.example",
            StoredTokenProvider(InMemoryTokenStore().apply { store("device-token-1") }),
            reachability,
        )

        // A prior transport failure, the state the report's device was
        // in when the network answered again with a portal page rather
        // than the server.
        reachability.unreachable()
        testScheduler.runCurrent()
        val callsBeforeThePortal = connectivity.reachableCalls
        runCatching { client.session() }
        testScheduler.runCurrent()
        assertEquals(
            callsBeforeThePortal,
            connectivity.reachableCalls,
            "the round trip completed, but nothing decoded as JMAP",
        )
        assertFalse(connectivity.online.value, "the platform's own reading is left exactly as it was")

        // A genuine session response afterwards still clears it.
        val genuineHttp = FakeHttp.client { respondJson(coordinatorTestDescriptorJson) }
        val genuineClient = JmapClient(
            genuineHttp,
            "https://mail.example",
            StoredTokenProvider(InMemoryTokenStore().apply { store("device-token-2") }),
            reachability,
        )
        genuineClient.session()
        testScheduler.runCurrent()
        assertEquals(
            callsBeforeThePortal + 1,
            connectivity.reachableCalls,
            "a decoded session response is what moves it",
        )
        assertTrue(connectivity.online.value)
    }
}
