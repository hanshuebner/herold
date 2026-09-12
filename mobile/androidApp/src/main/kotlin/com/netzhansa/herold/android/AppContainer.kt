package com.netzhansa.herold.android

import android.content.Context
import com.netzhansa.herold.shared.actions.MailActions
import com.netzhansa.herold.shared.compose.AddressBook
import com.netzhansa.herold.shared.compose.Composer
import com.netzhansa.herold.shared.auth.AuthClient
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.createHttpClient
import com.netzhansa.herold.shared.jmap.EventSourceClient
import com.netzhansa.herold.shared.jmap.ImageProxyClient
import com.netzhansa.herold.android.push.PushController
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.push.PushRegistrar
import com.netzhansa.herold.shared.search.MailSearch
import com.netzhansa.herold.shared.store.LocalStore
import com.netzhansa.herold.shared.store.SqlDelightLocalStore
import com.netzhansa.herold.shared.store.createDatabase
import com.netzhansa.herold.shared.store.DatabaseDriverFactory
import com.netzhansa.herold.shared.sync.SyncEngine
import io.ktor.client.HttpClient
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/** The default herold deployment; editable on the sign-in screen. */
const val DEFAULT_BASE_URL = "https://mail.netzhansa.com"

/**
 * Everything a signed-in session owns. It exists only while a bearer token
 * does; signing out drops it along with the local database's contents
 * (REQ-AND-AUTH-21).
 */
class SessionScope(
    val baseUrl: String,
    val client: JmapClient,
    val syncEngine: SyncEngine,
    val actions: MailActions,
    val eventSource: EventSourceClient,
    val imageProxy: ImageProxyClient,
    val pushRegistrar: PushRegistrar,
    val composer: Composer,
    val addressBook: AddressBook,
    val search: MailSearch,
)

/**
 * The app's object graph. The local store outlives a session so the inbox
 * renders from it before the first network call (REQ-AND-SYNC-03); the JMAP
 * client and the sync engine are rebuilt per sign-in because the base URL is
 * chosen there.
 */
class AppContainer(context: Context) {

    private val httpClient: HttpClient = createHttpClient()

    val tokenStore = KeystoreTokenStore(context)

    val store: LocalStore = SqlDelightLocalStore(
        database = createDatabase(DatabaseDriverFactory(context)),
        dispatcher = Dispatchers.IO,
        now = { System.currentTimeMillis() },
    )

    private val authClient = AuthClient(httpClient, tokenStore)

    /** Push registration and the memory of a declined permission (REQ-AND-PUSH-01/03). */
    val push: PushController = PushController(context.applicationContext, this)

    private val _session = MutableStateFlow<SessionScope?>(null)
    val session: StateFlow<SessionScope?> = _session.asStateFlow()

    /**
     * The account the shell is scoped to, null for the combined view
     * (suite REQ-MAIL-SUB-02). It is held here because compose and search
     * are scoped by it as well, not only the message list.
     */
    val accountScope = MutableStateFlow<String?>(null)

    /** True once [restore] has run, so the shell does not flash the sign-in screen. */
    private val _restored = MutableStateFlow(false)
    val restored: StateFlow<Boolean> = _restored.asStateFlow()

    /** Re-opens the session a stored token already authorises (token survives process death). */
    suspend fun restore() {
        val token = tokenStore.currentToken()
        val baseUrl = tokenStore.baseUrl()
        if (!token.isNullOrBlank() && !baseUrl.isNullOrBlank()) {
            _session.value = buildSession(baseUrl)
        }
        _restored.value = true
    }

    suspend fun signIn(baseUrl: String, email: String, password: String, totpCode: String?): SignInResult {
        val normalised = baseUrl.trim().trimEnd('/')
        val result = authClient.signIn(normalised, email.trim(), password, totpCode)
        if (result is SignInResult.Success) {
            tokenStore.setBaseUrl(normalised)
            _session.value = buildSession(normalised)
        }
        return result
    }

    /** Clears the token and every server-derived row for the account. */
    suspend fun signOut() {
        // Drop the push subscription first: it is bound to the principal
        // whose token is about to be forgotten (REQ-AND-PUSH-02).
        runCatching { push.unregister() }
        _session.value = null
        accountScope.value = null
        authClient.signOut()
        store.clearAll()
    }

    private fun buildSession(baseUrl: String): SessionScope {
        val client = JmapClient(httpClient, baseUrl, tokenStore)
        return SessionScope(
            baseUrl = baseUrl,
            client = client,
            syncEngine = SyncEngine(client, store, now = { System.currentTimeMillis() }),
            actions = MailActions(client, store),
            eventSource = EventSourceClient(httpClient, client),
            imageProxy = ImageProxyClient(httpClient, client),
            pushRegistrar = PushRegistrar(
                api = client,
                store = store,
                now = { System.currentTimeMillis() },
                newDeviceClientId = { "herold-android-" + java.util.UUID.randomUUID() },
            ),
            composer = Composer(client),
            addressBook = AddressBook(client),
            search = MailSearch(client, store),
        )
    }
}
