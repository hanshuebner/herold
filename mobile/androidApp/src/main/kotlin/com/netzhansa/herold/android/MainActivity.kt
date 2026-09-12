package com.netzhansa.herold.android

import android.content.Intent
import android.os.Build
import android.os.Bundle
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.fragment.app.FragmentActivity
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import com.netzhansa.herold.android.auth.LockScreen
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.android.ui.compose.ComposeScreen
import com.netzhansa.herold.android.ui.filters.FilterEditorScreen
import com.netzhansa.herold.android.ui.filters.FiltersScreen
import com.netzhansa.herold.android.ui.inbox.InboxScreen
import com.netzhansa.herold.android.ui.outbox.OutboxScreen
import com.netzhansa.herold.android.ui.search.SearchScreen
import com.netzhansa.herold.android.ui.settings.SessionsScreen
import com.netzhansa.herold.android.ui.settings.SettingsScreen
import com.netzhansa.herold.android.ui.settings.TransparencyScreen
import com.netzhansa.herold.android.ui.signin.SignInScreen
import com.netzhansa.herold.android.links.IntentRouting
import com.netzhansa.herold.android.links.LaunchRequest
import com.netzhansa.herold.shared.links.AppDestination
import com.netzhansa.herold.shared.links.ComposePrefill
import com.netzhansa.herold.android.ui.theme.HeroldTheme
import com.netzhansa.herold.android.ui.thread.ThreadScreen
import com.netzhansa.herold.shared.compose.ComposeMode
import com.netzhansa.herold.shared.sync.SyncStatus
import com.netzhansa.herold.shared.sync.SyncTypes
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch

/**
 * The single activity the whole shell lives in (REQ-AND-NAV-01). Predictive
 * back comes from the platform: the activity opts into the back-gesture
 * animation through `android:enableOnBackInvokedCallback` and Navigation
 * Compose animates the popped destination.
 *
 * It is a FragmentActivity because BiometricPrompt, the unlock
 * REQ-AND-AUTH-11 gates the token behind, is hosted by one.
 */
class MainActivity : FragmentActivity() {
    private val container: AppContainer by lazy { (application as HeroldApplication).container }

    /**
     * What the intent this activity was started with asks for: a
     * notification's thread or reply target, a share, a mailto:, or a
     * deep link. Held per activity, so the instance that received the
     * intent is the one that acts on it.
     */
    private val launchRequest = MutableStateFlow<LaunchRequest?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        launchRequest.value = IntentRouting.resolve(intent)
        setContent {
            HeroldTheme {
                Surface(modifier = Modifier.fillMaxSize()) {
                    HeroldApp(container, launchRequest)
                }
            }
        }
    }

    override fun onStart() {
        super.onStart()
        container.unlock.onForegrounded(System.currentTimeMillis())
    }

    override fun onStop() {
        super.onStop()
        container.unlock.onBackgrounded(System.currentTimeMillis())
    }

    /**
     * A notification tap, a share or a deep link arriving while the shell
     * is already running (REQ-AND-PUSH-13, REQ-AND-SYS-01/10).
     */
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        IntentRouting.resolve(intent)?.let { launchRequest.value = it }
    }
}

/**
 * Sign-in or the mail shell, decided by whether a token is held. The local
 * store is read either way, so a signed-in cold start paints the inbox
 * before the first network call (REQ-AND-SYNC-03).
 */
@Composable
fun HeroldApp(
    container: AppContainer,
    launchRequest: MutableStateFlow<LaunchRequest?> = MutableStateFlow(null),
) {
    val session by container.session.collectAsStateSafely(null)
    val restored by container.restored.collectAsStateSafely(false)
    val locked by container.unlock.locked.collectAsStateSafely(false)
    val scope = rememberCoroutineScope()

    LaunchedEffect(Unit) { container.restore() }

    val current = session
    when {
        !restored -> Box(
            modifier = Modifier.fillMaxSize().testTag("app-restoring"),
            contentAlignment = Alignment.Center,
        ) {
            CircularProgressIndicator()
        }

        current == null -> SignInScreen(container)

        // The unlock is ahead of every screen and every network call:
        // the token is not released until it succeeds (REQ-AND-AUTH-11).
        locked -> LockScreen(container.unlock)

        else -> {
            val navController = rememberNavController()
            // Filters and the transparency page are per principal: the
            // scoped account when the user picked one, the primary
            // otherwise (suite REQ-MAIL-SUB-02).
            val accounts by container.store.accounts().collectAsStateSafely(emptyList())
            val scoped by container.accountScope.collectAsStateSafely(null)
            val filterAccount = scoped ?: accounts.firstOrNull { it.isPrimary }?.id ?: ""

            ForegroundSync(session = current)
            PushEngagement(container = container, session = current)

            // A notification tap, a share, a mailto: or a deep link names
            // a destination; open it once the shell is up
            // (REQ-AND-SYS-10, REQ-AND-PUSH-13/21).
            val request by launchRequest.collectAsStateSafely(null)
            LaunchedEffect(request) {
                val pending = request ?: return@LaunchedEffect
                when (val destination = pending.destination) {
                    is AppDestination.Thread -> {
                        val account = destination.accountId
                            ?: container.accountHolding(destination.threadId)
                        if (account != null) {
                            navController.navigate("thread/$account/${destination.threadId}")
                        }
                    }

                    is AppDestination.Reply ->
                        navController.navigate(
                            "compose/${ComposeMode.REPLY.name}/${destination.accountId}/${destination.emailId}",
                        )

                    is AppDestination.Compose -> {
                        container.composeHandoff.value = ComposeHandoff(
                            prefill = destination.prefill,
                            attachments = pending.attachments.map { it.toString() },
                        )
                        navController.navigate("compose-handoff")
                    }

                    is AppDestination.Settings -> navController.navigate("settings")

                    AppDestination.Inbox -> navController.popBackStack("inbox", inclusive = false)
                }
                launchRequest.value = null
            }

            // A send taken back inside its undo window comes back as the
            // compose it was, on the screen the user is on (issue #354).
            val resumed by container.composeResume.collectAsStateSafely(null)
            LaunchedEffect(resumed) {
                if (resumed != null) navController.navigate("compose-resume")
            }

            NavHost(navController = navController, startDestination = "inbox") {
                composable("inbox") {
                    InboxScreen(
                        container = container,
                        session = current,
                        onOpenThread = { accountId, threadId ->
                            navController.navigate("thread/$accountId/$threadId")
                        },
                        onCompose = { navController.navigate("compose/NEW/-/-") },
                        onSearch = { navController.navigate("search") },
                        onOutbox = { navController.navigate("outbox") },
                        onSettings = { navController.navigate("settings") },
                        onFilters = { navController.navigate("filters") },
                        onSignOut = { scope.launch { container.signOut() } },
                    )
                }
                composable("settings") {
                    SettingsScreen(
                        unlock = container.unlock,
                        onSessions = { navController.navigate("sessions") },
                        onTransparency = { navController.navigate("transparency") },
                        onBack = { navController.popBackStack() },
                    )
                }
                composable("sessions") {
                    SessionsScreen(
                        container = container,
                        session = current,
                        onBack = { navController.popBackStack() },
                    )
                }
                composable("compose-resume") {
                    ComposeScreen(
                        container = container,
                        session = current,
                        mode = ComposeMode.NEW,
                        accountId = null,
                        parentEmailId = null,
                        accountScope = container.accountScope.value,
                        onClose = { navController.popBackStack() },
                        resume = resumed,
                    )
                }
                // A share, a mailto: or a compose shortcut, opened on
                // what it handed over (REQ-AND-SYS-01/03/22).
                composable("compose-handoff") {
                    ComposeScreen(
                        container = container,
                        session = current,
                        mode = ComposeMode.NEW,
                        accountId = null,
                        parentEmailId = null,
                        accountScope = container.accountScope.value,
                        onClose = { navController.popBackStack() },
                        handoff = container.composeHandoff.value,
                    )
                }
                composable("outbox") {
                    OutboxScreen(
                        container = container,
                        session = current,
                        onBack = { navController.popBackStack() },
                    )
                }
                composable("search") {
                    SearchScreen(
                        container = container,
                        session = current,
                        accountScope = container.accountScope.value,
                        onOpenThread = { accountId, threadId ->
                            navController.navigate("thread/$accountId/$threadId")
                        },
                        onBack = { navController.popBackStack() },
                    )
                }
                composable(
                    route = "compose/{mode}/{accountId}/{emailId}",
                    arguments = listOf(
                        navArgument("mode") { type = NavType.StringType },
                        navArgument("accountId") { type = NavType.StringType },
                        navArgument("emailId") { type = NavType.StringType },
                    ),
                ) { entry ->
                    val accountId = entry.arguments?.getString("accountId")?.takeIf { it != "-" }
                    val emailId = entry.arguments?.getString("emailId")?.takeIf { it != "-" }
                    ComposeScreen(
                        container = container,
                        session = current,
                        mode = runCatching {
                            ComposeMode.valueOf(entry.arguments?.getString("mode").orEmpty())
                        }.getOrDefault(ComposeMode.NEW),
                        accountId = accountId,
                        parentEmailId = emailId,
                        accountScope = container.accountScope.value,
                        onClose = { navController.popBackStack() },
                    )
                }
                composable(
                    route = "thread/{accountId}/{threadId}",
                    arguments = listOf(
                        navArgument("accountId") { type = NavType.StringType },
                        navArgument("threadId") { type = NavType.StringType },
                    ),
                ) { entry ->
                    ThreadScreen(
                        container = container,
                        session = current,
                        accountId = entry.arguments?.getString("accountId").orEmpty(),
                        threadId = entry.arguments?.getString("threadId").orEmpty(),
                        onCompose = { mode, emailId ->
                            val account = entry.arguments?.getString("accountId").orEmpty()
                            navController.navigate("compose/${mode.name}/$account/$emailId")
                        },
                        onCreateFilter = { fromEmail, subject ->
                            val account = entry.arguments?.getString("accountId").orEmpty()
                            container.filterSeed.value = FilterSeed(account, fromEmail, subject)
                            navController.navigate("filter-new/$account")
                        },
                        onComposeTo = { to, subject, body ->
                            container.composeHandoff.value = ComposeHandoff(
                                ComposePrefill(to = listOf(to), subject = subject, body = body),
                            )
                            navController.navigate("compose-unsubscribe")
                        },
                        onBack = { navController.popBackStack() },
                    )
                }
                composable("filters") {
                    FiltersScreen(
                        container = container,
                        session = current,
                        accountId = filterAccount,
                        onEdit = { ruleId ->
                            navController.navigate("filter-edit/${filterAccount}/$ruleId")
                        },
                        onCreate = {
                            container.filterSeed.value = null
                            navController.navigate("filter-new/${filterAccount}")
                        },
                        onBack = { navController.popBackStack() },
                    )
                }
                composable(
                    route = "filter-new/{accountId}",
                    arguments = listOf(navArgument("accountId") { type = NavType.StringType }),
                ) { entry ->
                    val account = entry.arguments?.getString("accountId").orEmpty()
                    val seed = container.filterSeed.value?.takeIf { it.accountId == account }
                    FilterEditorScreen(
                        container = container,
                        session = current,
                        accountId = account,
                        ruleId = null,
                        seedFrom = seed?.fromEmail,
                        seedSubject = seed?.subject,
                        onClose = {
                            container.filterSeed.value = null
                            navController.popBackStack()
                        },
                    )
                }
                composable(
                    route = "filter-edit/{accountId}/{ruleId}",
                    arguments = listOf(
                        navArgument("accountId") { type = NavType.StringType },
                        navArgument("ruleId") { type = NavType.StringType },
                    ),
                ) { entry ->
                    FilterEditorScreen(
                        container = container,
                        session = current,
                        accountId = entry.arguments?.getString("accountId").orEmpty(),
                        ruleId = entry.arguments?.getString("ruleId"),
                        seedFrom = null,
                        seedSubject = null,
                        onClose = { navController.popBackStack() },
                    )
                }
                composable("transparency") {
                    TransparencyScreen(
                        session = current,
                        accountId = filterAccount,
                        onBack = { navController.popBackStack() },
                    )
                }
                composable("compose-unsubscribe") {
                    ComposeScreen(
                        container = container,
                        session = current,
                        mode = ComposeMode.NEW,
                        accountId = null,
                        parentEmailId = null,
                        accountScope = container.accountScope.value,
                        onClose = {
                            container.composeHandoff.value = null
                            navController.popBackStack()
                        },
                        handoff = container.composeHandoff.value,
                    )
                }
            }
        }
    }
}

/**
 * Holds the EventSource connection open while the shell is foregrounded and
 * reconciles the named account when a `StateChange` arrives; the connection
 * is dropped on background, where FCM becomes the wake channel
 * (REQ-AND-SYNC-11, REQ-AND-NAV-21).
 */
@Composable
private fun ForegroundSync(session: SessionScope) {
    val lifecycleOwner = LocalLifecycleOwner.current
    LaunchedEffect(session) {
        lifecycleOwner.lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            while (true) {
                runCatching {
                    session.eventSource.stateChanges().collect { event ->
                        event.changed.forEach { (accountId, states) ->
                            session.syncEngine.syncAccount(
                                accountId,
                                states.keys.filter { it in SyncTypes.ALL },
                            )
                        }
                    }
                }
                kotlinx.coroutines.delay(RECONNECT_DELAY_MS)
            }
        }
    }
}

/**
 * Asks for the notification permission after the user has engaged - the
 * first successful sync - rather than at first launch, and registers the FCM
 * token once it is granted (REQ-AND-PUSH-01/03). A denial is remembered, so
 * the prompt does not return on every launch; settings re-offers it.
 */
@Composable
private fun PushEngagement(container: AppContainer, session: SessionScope) {
    val context = LocalContext.current
    val status by session.syncEngine.status.collectAsStateSafely(SyncStatus.Idle)
    var synced by remember { mutableStateOf(false) }
    var sawSyncing by remember { mutableStateOf(false) }
    var granted by remember {
        mutableStateOf(
            Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
                androidx.core.content.ContextCompat.checkSelfPermission(
                    context,
                    android.Manifest.permission.POST_NOTIFICATIONS,
                ) == android.content.pm.PackageManager.PERMISSION_GRANTED,
        )
    }

    val launcher = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { allowed ->
        granted = allowed
        container.push.permissionRequested = true
        container.push.permissionDenied = !allowed
    }

    LaunchedEffect(status) {
        if (status is SyncStatus.Syncing) sawSyncing = true
        if (sawSyncing && status is SyncStatus.Idle) synced = true
    }

    LaunchedEffect(synced, granted) {
        if (!synced) return@LaunchedEffect
        if (!granted) {
            if (!container.push.permissionRequested && !container.push.permissionDenied) {
                launcher.launch(android.Manifest.permission.POST_NOTIFICATIONS)
            }
            return@LaunchedEffect
        }
        container.push.registerCurrentTransport()
    }
}

private const val RECONNECT_DELAY_MS = 5_000L
