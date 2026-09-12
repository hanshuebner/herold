package com.netzhansa.herold.android

import android.content.Context
import com.netzhansa.herold.shared.actions.FilterActions
import com.netzhansa.herold.shared.actions.MailActions
import com.netzhansa.herold.shared.actions.UndoCenter
import com.netzhansa.herold.shared.compose.AddressBook
import com.netzhansa.herold.shared.compose.Composer
import com.netzhansa.herold.android.auth.UnlockController
import com.netzhansa.herold.shared.auth.AuthClient
import com.netzhansa.herold.shared.auth.Credential
import com.netzhansa.herold.shared.auth.CredentialsClient
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import com.netzhansa.herold.shared.auth.OAuthClient
import com.netzhansa.herold.shared.auth.OAuthClientConfig
import com.netzhansa.herold.shared.auth.OAuthSignIn
import com.netzhansa.herold.shared.auth.OAuthSignInResult
import com.netzhansa.herold.shared.auth.SessionAuthenticator
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.createHttpClient
import com.netzhansa.herold.shared.jmap.EventSourceClient
import com.netzhansa.herold.shared.jmap.ImageProxyClient
import com.netzhansa.herold.android.push.PushController
import com.netzhansa.herold.android.work.OutboxWorker
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.links.ComposePrefill
import com.netzhansa.herold.shared.llm.Transparency
import com.netzhansa.herold.shared.outbox.ComposePayload
import com.netzhansa.herold.shared.outbox.FileBlobSpool
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.push.PushRegistrar
import com.netzhansa.herold.shared.mail.UnsubscribeClient
import com.netzhansa.herold.shared.search.MailSearch
import com.netzhansa.herold.shared.store.LocalStore
import com.netzhansa.herold.shared.store.SqlDelightLocalStore
import com.netzhansa.herold.shared.store.createDatabase
import com.netzhansa.herold.shared.store.DatabaseDriverFactory
import com.netzhansa.herold.shared.sync.AndroidConnectivityMonitor
import com.netzhansa.herold.shared.sync.SyncEngine
import com.netzhansa.herold.shared.sync.offlineIndication
import io.ktor.client.HttpClient
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch

/** The default herold deployment; editable on the sign-in screen. */
const val DEFAULT_BASE_URL = "https://mail.netzhansa.com"

/** How long sign-out waits for the push subscription to be dropped. */
private const val PUSH_UNREGISTER_TIMEOUT_MS = 5_000L

/**
 * Everything a signed-in session owns. It exists only while a bearer token
 * does; signing out drops it along with the local database's contents
 * (REQ-AND-AUTH-21).
 */
class SessionScope(
    val baseUrl: String,
    val client: JmapClient,
    val syncEngine: SyncEngine,
    /** The outbox's submitter; the shell watches its failures. */
    val drainer: OutboxDrainer,
    /**
     * Asks for a drain of the outbox, off any screen's lifetime. The
     * delay is how long the first attempt waits, which is what holds a
     * send inside its undo window (issue #354).
     */
    val requestDrain: (delayMs: Long) -> Unit,
    val actions: MailActions,
    /** Filter-rule writes: the filters screen, mute and block (suite REQ-FLT-20). */
    val filters: FilterActions,
    /** The RFC 8058 one-click POST, off the session's own client (REQ-UNS-20). */
    val unsubscribe: UnsubscribeClient,
    /** The prompts, the models and the per-message classifier detail (suite G7). */
    val transparency: Transparency,
    val eventSource: EventSourceClient,
    val imageProxy: ImageProxyClient,
    val pushRegistrar: PushRegistrar,
    val composer: Composer,
    val addressBook: AddressBook,
    val search: MailSearch,
    /** The account's active sessions and grants (REQ-AND-AUTH-22). */
    val credentials: CredentialsClient,
)

/**

 * A compose a share, a `mailto:` link, an unsubscribe or a shortcut asked
 * for (REQ-AND-SYS-01/03/22, REQ-UNS-22), waiting for the shell to open
 * the composer on it. [attachments] are the shared files' content URIs;
 * they go up the composer's own attachment path once it is on screen.
 */
data class ComposeHandoff(
    val prefill: ComposePrefill,
    val attachments: List<String> = emptyList(),
)

/**
 * The message "Create filter from this message" was invoked on, waiting
 * for the editor to open on it (suite REQ-FLT-32).
 */
data class FilterSeed(val accountId: String, val fromEmail: String, val subject: String)

/** Where the sign-in screen is in the authorization-code flow. */
sealed interface SignInState {
    data object Idle : SignInState

    /** The Custom Tab has the foreground; the app is waiting for the redirect. */
    data object AwaitingBrowser : SignInState

    /** The callback arrived; the code is being exchanged for tokens. */
    data object Exchanging : SignInState

    data class Failed(val message: String) : SignInState
}

/**
 * The app's object graph. The local store outlives a session so the inbox
 * renders from it before the first network call (REQ-AND-SYNC-03); the JMAP
 * client and the sync engine are rebuilt per sign-in because the base URL is
 * chosen there.
 */
class AppContainer(context: Context) {

    private val appContext: Context = context.applicationContext

    private val httpClient: HttpClient = createHttpClient()

    /**
     * The scope work outlives a screen in: an outbox drain runs here, so
     * navigating away from the screen that started an action does not
     * cancel the change the user already saw applied (issue #345).
     */
    private val appScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    /** Undo offers an action parks for the list that shows them (issue #345). */
    val undo = UndoCenter()

    val tokenStore = KeystoreTokenStore(context)

    val store: LocalStore = SqlDelightLocalStore(
        database = createDatabase(DatabaseDriverFactory(context)),
        dispatcher = Dispatchers.IO,
        now = { System.currentTimeMillis() },
    )

    /** Attachment bytes a queued send still has to upload (REQ-AND-SYNC-21). */
    val spool = FileBlobSpool(context.applicationContext)

    /** The durable queue of pending mutations (REQ-AND-SYNC-20..25). */
    val outbox = Outbox(store) { System.currentTimeMillis() }

    /**
     * A compose the user took back within its undo window, waiting for
     * the shell to reopen the composer on it (issue #354).
     */
    val composeResume = MutableStateFlow<ComposePayload?>(null)

    /**
     * What a share, a mailto: link, an unsubscribe or a shortcut opens
     * the composer on (REQ-AND-SYS-01/03, REQ-UNS-22).
     */
    val composeHandoff = MutableStateFlow<ComposeHandoff?>(null)

    /** What the filter editor opens with when a message seeded it. */
    val filterSeed = MutableStateFlow<FilterSeed?>(null)

    /** The platform's connectivity, which drives the drain and the chip. */
    private val connectivity = AndroidConnectivityMonitor(context.applicationContext, appScope)

    /**
     * Whether the shell says the phone is offline. It lags the radio by
     * the grace period, so a drop the user would not have noticed does
     * not flash a chip at them (REQ-AND-SYNC-30).
     */
    val offline: StateFlow<Boolean> =
        connectivity.online.offlineIndication().stateIn(appScope, SharingStarted.Eagerly, false)

    private val authClient = AuthClient(httpClient, tokenStore)

    /** The unlock the token release waits on when the user turned it on. */
    val unlock = UnlockController(context)

    private val oauthConfig = OAuthClientConfig.DEFAULT

    private val oauthClient = OAuthClient(httpClient, oauthConfig)

    /** The authorization-code flow's two halves, either side of the browser. */
    private val oauthSignIn = OAuthSignIn(
        oauth = oauthClient,
        tokens = tokenStore,
        pending = tokenStore,
        config = oauthConfig,
        now = { System.currentTimeMillis() },
    )

    private val _signInState = MutableStateFlow<SignInState>(SignInState.Idle)
    val signInState: StateFlow<SignInState> = _signInState.asStateFlow()

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

    init {
        // A connection returning is what the queue has been waiting for
        // (REQ-AND-SYNC-22); the drain follows it without the user
        // having to open anything.
        appScope.launch {
            connectivity.online.collect { up -> if (up) session.value?.requestDrain?.invoke(0) }
        }
    }

    /**
     * The account whose cached mail holds [threadId]. A link that carries
     * only the Suite's thread id - an App Link, a shared thread URL - does
     * not name an account, and the store is what knows which one it is.
     * With nothing cached the first account answers, and its sync engine
     * fetches the thread when the screen opens (issue #339).
     */
    suspend fun accountHolding(threadId: String): String? {
        val accounts = store.accountList()
        accounts.forEach { account ->
            if (store.threadEmailList(account.id, threadId).isNotEmpty()) return account.id
        }
        return accounts.firstOrNull()?.id
    }

    /** The server the last sign-in was against, offered again on the sign-in screen. */
    suspend fun rememberedBaseUrl(): String? = tokenStore.baseUrl()

    /** Re-opens the session a stored token already authorises (token survives process death). */
    suspend fun restore() {
        if (_session.value == null) {
            val token = tokenStore.currentToken()
            val baseUrl = tokenStore.baseUrl()
            if (!token.isNullOrBlank() && !baseUrl.isNullOrBlank()) {
                unlock.lockIfEnabled()
                _session.value = buildSession(baseUrl)
            }
        }
        _restored.value = true
    }

    /**
     * Mints an authorization request against [baseUrl] and returns the
     * URL for the Custom Tab (REQ-AND-AUTH-01). The PKCE verifier is in
     * Keystore-backed storage before the browser opens, so the exchange
     * survives this process being killed behind it.
     */
    suspend fun beginSignIn(baseUrl: String): String {
        val normalised = baseUrl.trim().trimEnd('/')
        val url = oauthSignIn.begin(normalised)
        _signInState.value = SignInState.AwaitingBrowser
        return url
    }

    /**
     * Takes the redirect the browser came back on and finishes the
     * exchange (REQ-AND-AUTH-02). Runs on the container's scope so the
     * callback activity can finish immediately.
     */
    fun completeSignIn(callbackUri: String) {
        _signInState.value = SignInState.Exchanging
        appScope.launch {
            when (val result = oauthSignIn.complete(callbackUri)) {
                is OAuthSignInResult.Success -> openSession(result.baseUrl)
                is OAuthSignInResult.Failed -> _signInState.value = SignInState.Failed(result.message)
            }
        }
    }

    /** The user left the Custom Tab without authorising. */
    suspend fun abandonSignIn() {
        oauthSignIn.abandon()
        _signInState.value = SignInState.Idle
    }

    /** Clears a message the sign-in screen has shown. */
    fun clearSignInError() {
        if (_signInState.value is SignInState.Failed) _signInState.value = SignInState.Idle
    }

    /**
     * The debug-build fallback onto the device-token grant, for the
     * emulator harness and for a device with no browser. The release
     * sign-in is the Custom Tab flow above.
     */
    suspend fun signInWithPassword(
        baseUrl: String,
        email: String,
        password: String,
        totpCode: String?,
    ): SignInResult {
        val normalised = baseUrl.trim().trimEnd('/')
        val result = authClient.signIn(normalised, email.trim(), password, totpCode)
        if (result is SignInResult.Success) openSession(normalised)
        return result
    }

    /**
     * Opens the session the freshly stored token authorises: notes which
     * principal it belongs to, drops another principal's cached mail,
     * and records this device's own grant so the sessions screen can
     * mark and revoke it (REQ-AND-AUTH-21/22).
     */
    private suspend fun openSession(baseUrl: String) {
        tokenStore.setBaseUrl(baseUrl)
        val session = buildSession(baseUrl)
        val username = runCatching { session.client.session().username }.getOrNull()
        val previous = tokenStore.principal()
        if (!username.isNullOrBlank() && !previous.isNullOrBlank() && previous != username) {
            // A different account on the same install: the cached mail
            // of the previous one does not carry over (REQ-AND-AUTH-21).
            store.clearAll()
        }
        if (!username.isNullOrBlank()) tokenStore.setPrincipal(username)
        tokenStore.setGrantId(
            runCatching { session.credentials.ownGrantId(oauthConfig.clientId) }.getOrNull(),
        )
        _signInState.value = SignInState.Idle
        _session.value = session
    }

    /**
     * The session ended server-side - the grant revoked, the refresh
     * token reused or expired. The credential is already gone; the
     * cached mail stays until a different principal signs in
     * (REQ-AND-AUTH-04/20).
     */
    private suspend fun sessionLost() {
        _session.value = null
        accountScope.value = null
        tokenStore.setGrantId(null)
        _signInState.value = SignInState.Failed("Your session ended. Sign in again.")
    }

    /** Revokes this device's grant server-side and clears the account's local rows. */
    suspend fun signOut() {
        val current = _session.value
        // Drop the push subscription first: it is bound to the principal
        // whose token is about to be forgotten (REQ-AND-PUSH-02). It is
        // bounded: signing out must not wait on the push transport,
        // which on a cold process is still bringing itself up.
        runCatching { withTimeoutOrNull(PUSH_UNREGISTER_TIMEOUT_MS) { push.unregister() } }
        // Then the grant itself, which is what makes the token stop
        // working everywhere rather than only on this device
        // (REQ-AND-AUTH-21).
        val grantId = tokenStore.grantId()
        if (current != null && grantId != null) {
            runCatching { current.credentials.revoke(Credential.KIND_OAUTH2_GRANT, grantId) }
        }
        _session.value = null
        accountScope.value = null
        tokenStore.setGrantId(null)
        tokenStore.setPrincipal(null)
        authClient.signOut()
        oauthSignIn.abandon()
        unlock.unlocked()
        _signInState.value = SignInState.Idle
        store.clearAll()
    }

    /**
     * Drains the outbox and, when something reached the server, folds the
     * result back in: the Sent copy of a message the drain submitted is
     * what the user expects to see next.
     */
    private fun drain(syncEngine: SyncEngine, delayMs: Long) {
        // The background job covers what this pass cannot: the app being
        // closed or killed before the queue is empty (REQ-AND-SYNC-31).
        OutboxWorker.schedule(appContext, delayMs)
        appScope.launch {
            if (delayMs > 0) delay(delayMs)
            val outcome = syncEngine.drainOutbox()
            if (outcome.submitted > 0) syncEngine.syncAll()
        }
        Unit
    }

    private fun buildSession(baseUrl: String): SessionScope {
        // Every network caller takes its token from here, so one
        // refresh serves them all and a refused refresh ends the
        // session once (REQ-AND-AUTH-04).
        val authenticator = SessionAuthenticator(
            store = tokenStore,
            oauth = oauthClient,
            baseUrl = baseUrl,
            now = { System.currentTimeMillis() },
            unlockGate = unlock,
            onSessionLost = { sessionLost() },
        )
        val client = JmapClient(httpClient, baseUrl, authenticator)
        val composer = Composer(client, outbox, spool, { System.currentTimeMillis() })
        val drainer = OutboxDrainer(
            api = client,
            store = store,
            outbox = outbox,
            spool = spool,
            composer = composer,
            now = { System.currentTimeMillis() },
        )
        val syncEngine = SyncEngine(
            api = client,
            store = store,
            outbox = outbox,
            drainer = drainer,
            now = { System.currentTimeMillis() },
        )
        val requestDrain: (Long) -> Unit = { delayMs -> drain(syncEngine, delayMs) }
        return SessionScope(
            baseUrl = baseUrl,
            client = client,
            syncEngine = syncEngine,
            drainer = drainer,
            requestDrain = requestDrain,
            actions = MailActions(store, outbox) { requestDrain(0) },
            filters = FilterActions(store, outbox) { requestDrain(0) },
            // The unsubscribe POST goes out on the plain client: no auth
            // plugin, no cookie storage, nothing of the account on it.
            unsubscribe = UnsubscribeClient(httpClient),
            transparency = Transparency(client),
            eventSource = EventSourceClient(httpClient, client),
            imageProxy = ImageProxyClient(httpClient, client),
            pushRegistrar = PushRegistrar(
                api = client,
                store = store,
                now = { System.currentTimeMillis() },
                newDeviceClientId = { "herold-android-" + java.util.UUID.randomUUID() },
            ),
            composer = composer,
            addressBook = AddressBook(client),
            search = MailSearch(client, store),
            credentials = CredentialsClient(httpClient, baseUrl, authenticator),
        )
    }
}
