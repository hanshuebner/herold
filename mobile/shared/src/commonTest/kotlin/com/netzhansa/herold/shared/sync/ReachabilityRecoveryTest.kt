package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.auth.FakeHttp
import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import com.netzhansa.herold.shared.auth.StoredTokenProvider
import com.netzhansa.herold.shared.auth.respondJson
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.JmapAccount
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.jmap.JmapSession
import io.ktor.client.HttpClient
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

private val descriptor = JmapSession(
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

private val descriptorJson = Json.encodeToString(JmapSession.serializer(), descriptor)

/**
 * Coming back from a failure the shell showed as offline (issue #433).
 *
 * The phone met a name that would not resolve, the indicator turned, and
 * the passes that followed have to turn it back - including a pass that
 * never ran to its end because the shell called it off under it, which
 * is what left the reported device saying "Offline" with a completed
 * sync in its log.
 */
class ReachabilityRecoveryTest {

    private suspend fun client(reachability: Reachability, http: HttpClient) = JmapClient(
        http,
        "https://mail.example",
        StoredTokenProvider(InMemoryTokenStore().apply { store("device-token-1") }),
        reachability,
    )

    @Test
    fun aFailingPassThenASucceedingPassLeavesTheStateReachable() = runTest {
        val api = FakeJmapApi(descriptor)
        val reachability = Reachability()
        val engine = SyncEngine(api, FakeLocalStore(), reachability = reachability)

        api.readFailure = RuntimeException("Unable to resolve host \"mail.example\"")
        assertTrue(engine.syncAll() is SyncStatus.Failed)
        assertFalse(reachability.reachable.value, "a name that would not resolve is the server out of reach")

        api.readFailure = null
        assertEquals(SyncStatus.Idle, engine.syncAll())
        assertTrue(reachability.reachable.value, "a pass that ran through reached the server")
    }

    @Test
    fun theIndicatorReturnsToOnlineOnceASyncSucceedsAgain() = runTest {
        val api = FakeJmapApi(descriptor)
        val reachability = Reachability()
        val engine = SyncEngine(api, FakeLocalStore(), reachability = reachability)
        val online = MutableStateFlow(true)
        // What the shell renders: the radio and what the client's own
        // requests met, the two halves of the offline indication
        // (REQ-AND-SYNC-30).
        val offline = combine(online, reachability.reachable) { up, reached -> up && reached }
            .offlineIndication()

        api.readFailure = RuntimeException("Unable to resolve host \"mail.example\"")
        engine.syncAll()
        assertTrue(offline.first(), "the failure the user saw is the indicator turning on")

        api.readFailure = null
        engine.syncAll()
        assertFalse(offline.first(), "the indicator clears with the pass that got through")
    }

    @Test
    fun aPassCalledOffAfterTheServerAnsweredLeavesTheStateReachable() = runTest {
        val reachability = Reachability()
        // The failure at 05:05 in the report: the indicator is on.
        reachability.unreachable()

        val answered = CompletableDeferred<Unit>()
        val held = CompletableDeferred<Unit>()
        val http = FakeHttp.client { request ->
            if (request.url.encodedPath.endsWith("/.well-known/jmap")) {
                respondJson(descriptorJson)
            } else {
                // The pass is still in its first method call when the
                // shell calls it off.
                answered.complete(Unit)
                held.await()
                respondJson("{\"methodResponses\":[]}")
            }
        }
        val engine = SyncEngine(client(reachability, http), FakeLocalStore(), reachability = reachability)

        val pass = launch { engine.syncAll() }
        answered.await()
        pass.cancel()
        pass.join()
        held.complete(Unit)

        assertEquals(SyncStatus.Idle, engine.status.value)
        assertTrue(
            reachability.reachable.value,
            "the descriptor came back, so the server was reached whatever became of the pass",
        )
    }

    @Test
    fun aRequestThatGotThroughClearsWhatTheFailedOneRecorded() = runTest {
        val reachability = Reachability()
        var resolvable = false
        val http = FakeHttp.client {
            if (!resolvable) throw RuntimeException("Unable to resolve host \"mail.example\"")
            respondJson(descriptorJson)
        }
        val client = client(reachability, http)

        runCatching { client.session() }
        assertFalse(reachability.reachable.value, "nothing reached the server")

        resolvable = true
        client.session()
        assertTrue(reachability.reachable.value, "the transport is what says the server answered")
    }
}
