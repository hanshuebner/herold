package com.netzhansa.herold.android

import android.content.Context
import com.netzhansa.herold.shared.actions.MailActions
import com.netzhansa.herold.shared.actions.UndoCenter
import com.netzhansa.herold.shared.compose.AddressBook
import com.netzhansa.herold.shared.compose.Composer
import com.netzhansa.herold.shared.auth.AuthClient
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.createHttpClient
import com.netzhansa.herold.shared.jmap.EventSourceClient
import com.netzhansa.herold.shared.jmap.ImageProxyClient
import com.netzhansa.herold.android.push.PushController
import com.netzhansa.herold.android.work.OutboxWorker
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.outbox.ComposePayload
import com.netzhansa.herold.shared.outbox.FileBlobSpool
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.push.PushRegistrar
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
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch

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
    /** The outbox's submitter; the shell watches its failures. */
    val drainer: OutboxDrainer,
    /**
     * Asks for a drain of the outbox, off any screen's lifetime. The
     * delay is how long the first attempt waits, which is what holds a
     * send inside its undo window (issue #354).
     */
    val requestDrain: (delayMs: Long) -> Unit,
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
        val client = JmapClient(httpClient, baseUrl, tokenStore)
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
        )
    }
}
