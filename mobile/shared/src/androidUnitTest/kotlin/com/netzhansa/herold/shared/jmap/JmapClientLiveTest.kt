package com.netzhansa.herold.shared.jmap

import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import io.ktor.client.HttpClient
import io.ktor.client.engine.okhttp.OkHttp
import kotlinx.coroutines.runBlocking
import kotlin.test.Test
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * Exercises the real device-token + Mailbox/get path against a LIVE
 * ephemeral herold dev instance -- the deterministic analog of the
 * puppeteer rule for this slice (docs/design/android/implementation-plan.md
 * "Verification model"): it proves the auth bootstrap and a live JMAP read
 * work end to end, without needing an Android emulator.
 *
 * Runs on the host JVM (this is `shared`'s `androidUnitTest` source set, the
 * android-target local-unit-test variant, not an instrumented test), using
 * the same OkHttp Ktor engine the app uses on-device.
 *
 * Start an ephemeral instance first and point this test at it:
 *
 *   scripts/dev-instance.sh start
 *   # note the printed BACKEND_URL=http://127.0.0.1:<port>
 *   HEROLD_DEV_INSTANCE_URL=http://127.0.0.1:<port> \
 *     ./gradlew :shared:testDebugUnitTest --tests '*JmapClientLiveTest*'
 *
 * If HEROLD_DEV_INSTANCE_URL is unset, the test passes trivially (skips the
 * live assertion) so `:shared:build` stays green without a running instance
 * -- wiring dev-instance provisioning into the Gradle test task itself is
 * the CI-gating emulator harness, deferred to that later increment
 * (docs/design/android implementation-plan.md, Phase 0 "Remaining work").
 */
class JmapClientLiveTest {
    @Test
    fun signsInAndFetchesMailboxes() = runBlocking {
        val baseUrl = System.getenv("HEROLD_DEV_INSTANCE_URL")
        if (baseUrl.isNullOrBlank()) {
            println(
                "JmapClientLiveTest: HEROLD_DEV_INSTANCE_URL not set, skipping the " +
                    "live dev-instance check (see scripts/dev-instance.sh).",
            )
            return@runBlocking
        }

        val httpClient = HttpClient(OkHttp)
        try {
            val tokenStore = InMemoryTokenStore()
            val client = JmapClient(httpClient, baseUrl, tokenStore)

            val token = client.signIn("alice@example.local", "testpass123...")
            assertTrue(token.startsWith("hk_"), "expected an hk_... bearer token, got: $token")

            val mailboxes = client.fetchMailboxes()
            assertTrue(mailboxes.isNotEmpty(), "expected a non-empty mailbox list, got: $mailboxes")

            val inbox = mailboxes.firstOrNull { it.name == "INBOX" }
            assertNotNull(inbox, "expected an INBOX mailbox in: $mailboxes")
        } finally {
            httpClient.close()
        }
    }
}
