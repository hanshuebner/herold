package com.netzhansa.herold.shared.jmap

import com.netzhansa.herold.shared.auth.AuthClient
import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.sync.SyncEngine
import com.netzhansa.herold.shared.sync.SyncStatus
import com.netzhansa.herold.shared.sync.SyncTypes
import io.ktor.client.HttpClient
import io.ktor.client.engine.okhttp.OkHttp
import kotlinx.coroutines.runBlocking
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * Drives the real device-token grant and a full sync pass against a LIVE
 * ephemeral herold, on the host JVM with the same OkHttp Ktor engine the app
 * uses on-device. It is the fast counterpart of the emulator acceptance run:
 * it proves the wire contract (auth, session, Mailbox/get, Email/query +
 * Email/get, state strings) without an AVD.
 *
 *   scripts/dev-instance.sh start
 *   HEROLD_DEV_INSTANCE_URL=http://127.0.0.1:<port> \
 *     ./gradlew :shared:testDebugUnitTest --tests '*JmapClientLiveTest*'
 *
 * With HEROLD_DEV_INSTANCE_URL unset the test skips its live assertions so
 * `:shared:build` stays green in CI, which runs no herold.
 */
class JmapClientLiveTest {
    @Test
    fun signsInAndSyncsTheSeededAccount() = runBlocking {
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
            val signIn = AuthClient(httpClient, tokenStore)
                .signIn(baseUrl, "alice@example.local", "testpass123...")
            assertTrue(signIn is SignInResult.Success, "sign-in failed: $signIn")
            assertTrue(
                (signIn as SignInResult.Success).token.startsWith("hk_"),
                "expected an hk_... bearer token",
            )

            val client = JmapClient(httpClient, baseUrl, tokenStore)
            val session = client.session()
            assertTrue(session.mailAccountIds().isNotEmpty(), "session advertised no mail account")

            val store = FakeLocalStore()
            val engine = SyncEngine(client, store)
            assertEquals(SyncStatus.Idle, engine.syncAll(), "sync pass failed")

            val accountId = session.mailAccountId
            assertNotNull(accountId)
            assertNotNull(
                store.mailboxList().firstOrNull { it.role == MailboxRoles.INBOX },
                "no inbox in ${store.mailboxList()}",
            )
            assertNotNull(store.syncState(accountId, SyncTypes.MAILBOX), "no Mailbox state string persisted")
            assertNotNull(store.syncState(accountId, SyncTypes.EMAIL), "no Email state string persisted")

            // A second pass must go through Foo/changes and stay green.
            assertEquals(SyncStatus.Idle, engine.syncAll(), "incremental pass failed")
        } finally {
            httpClient.close()
        }
    }
}
