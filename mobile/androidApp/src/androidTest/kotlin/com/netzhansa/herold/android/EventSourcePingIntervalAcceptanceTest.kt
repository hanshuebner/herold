package com.netzhansa.herold.android

import androidx.test.ext.junit.runners.AndroidJUnit4
import com.netzhansa.herold.shared.auth.AuthClient
import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.createHttpClient
import com.netzhansa.herold.shared.jmap.EventSourceClient
import com.netzhansa.herold.shared.jmap.JmapClient
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The event stream's read timeout against a short, deterministic ping
 * interval (issue #493): a healthy idle stream can go a full ping period
 * between frames, so a read timeout shorter than that reconnects a
 * connection that never actually failed. This holds the stream open past
 * two ping periods -- the bound the client is supposed to tolerate -- and
 * asserts the collector never saw a failure.
 */
@RunWith(AndroidJUnit4::class)
class EventSourcePingIntervalAcceptanceTest {

    @Test
    fun theStreamStaysOpenAcrossTwoPingPeriods() = runBlocking {
        val httpClient = createHttpClient()
        val tokenStore = InMemoryTokenStore()
        val signIn = AuthClient(httpClient, tokenStore).signIn(
            DevInstance.baseUrl,
            DevInstance.email,
            DevInstance.password,
        )
        check(signIn is SignInResult.Success) { "dev-instance sign-in failed: $signIn" }
        val client = JmapClient(httpClient, DevInstance.baseUrl, tokenStore)
        val eventSource = EventSourceClient(httpClient, client)

        val failure = AtomicReference<Throwable?>(null)
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        val job = scope.launch {
            try {
                eventSource.stateChanges(types = emptyList(), pingSeconds = PING_SECONDS).collect {}
            } catch (t: Throwable) {
                failure.set(t)
            }
        }
        try {
            // Longer than two ping periods: the bound the client's read
            // timeout is supposed to clear (EventSourceClient.kt,
            // eventStreamReadTimeoutMillis). A timeout derived from one
            // ping period, or the engine's own default, would already have
            // ended the collector with an exception by here.
            delay(HOLD_MS)
            assertTrue(
                "the stream ended before the ${HOLD_MS}ms hold: ${failure.get()}",
                job.isActive,
            )
            assertNull("the collector saw a failure: ${failure.get()}", failure.get())
        } finally {
            job.cancelAndJoin()
            httpClient.close()
        }
    }

    private companion object {
        /**
         * Short enough to keep the test's run time reasonable; the server
         * accepts any `ping=` value (internal/protojmap/push.go).
         */
        const val PING_SECONDS = 12
        const val HOLD_MS = (PING_SECONDS * 2 + 5) * 1000L
    }
}
