package com.netzhansa.herold.android

import android.content.Intent
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
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
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.android.ui.compose.ComposeScreen
import com.netzhansa.herold.android.ui.inbox.InboxScreen
import com.netzhansa.herold.android.ui.outbox.OutboxScreen
import com.netzhansa.herold.android.ui.search.SearchScreen
import com.netzhansa.herold.android.ui.settings.SettingsScreen
import com.netzhansa.herold.android.ui.signin.SignInScreen
import com.netzhansa.herold.android.push.MailNotifier
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
 */
class MainActivity : ComponentActivity() {
    private val container: AppContainer by lazy { (application as HeroldApplication).container }

    /**
     * The thread this activity's launch intent named, if it came from a
     * notification tap. Held per activity, so the instance that received
     * the tap is the one that opens the thread.
     */
    private val threadTarget = MutableStateFlow<Pair<String, String>?>(null)

    /**
     * The message a notification's Reply action named, if the launch
     * intent carried one (REQ-AND-PUSH-21).
     */
    private val replyTarget = MutableStateFlow<Pair<String, String>?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        routeNotificationTap(intent)
        setContent {
            HeroldTheme {
                Surface(modifier = Modifier.fillMaxSize()) {
                    HeroldApp(container, threadTarget, replyTarget)
                }
            }
        }
    }

    /** A tap on a notification while the shell is already running. */
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        routeNotificationTap(intent)
    }

    /**
     * Takes the thread a notification names and hands it to the shell
     * (REQ-AND-PUSH-13). The thread renders from the local store, which the
     * push's reconcile pass has already filled.
     */
    private fun routeNotificationTap(intent: Intent?) {
        val accountId = intent?.getStringExtra(MailNotifier.EXTRA_ACCOUNT_ID) ?: return
        val replyTo = intent.getStringExtra(MailNotifier.EXTRA_REPLY_EMAIL_ID)
        if (replyTo != null) {
            replyTarget.value = accountId to replyTo
            return
        }
        val threadId = intent.getStringExtra(MailNotifier.EXTRA_THREAD_ID) ?: return
        threadTarget.value = accountId to threadId
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
    threadTarget: MutableStateFlow<Pair<String, String>?> = MutableStateFlow(null),
    replyTarget: MutableStateFlow<Pair<String, String>?> = MutableStateFlow(null),
) {
    val session by container.session.collectAsStateSafely(null)
    val restored by container.restored.collectAsStateSafely(false)
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

        else -> {
            val navController = rememberNavController()
            ForegroundSync(session = current)
            PushEngagement(container = container, session = current)

            // A notification tap names a thread; open it once the shell is up.
            val target by threadTarget.collectAsStateSafely(null)
            LaunchedEffect(target) {
                target?.let { (accountId, threadId) ->
                    navController.navigate("thread/$accountId/$threadId")
                    threadTarget.value = null
                }
            }

            // A send taken back inside its undo window comes back as the
            // compose it was, on the screen the user is on (issue #354).
            val resumed by container.composeResume.collectAsStateSafely(null)
            LaunchedEffect(resumed) {
                if (resumed != null) navController.navigate("compose-resume")
            }

            // The shade's Reply action names a message; open the composer
            // on it, quote prepared (REQ-AND-PUSH-21).
            val reply by replyTarget.collectAsStateSafely(null)
            LaunchedEffect(reply) {
                reply?.let { (accountId, emailId) ->
                    navController.navigate("compose/${ComposeMode.REPLY.name}/$accountId/$emailId")
                    replyTarget.value = null
                }
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
                        onSignOut = { scope.launch { container.signOut() } },
                    )
                }
                composable("settings") {
                    SettingsScreen(onBack = { navController.popBackStack() })
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
                        onBack = { navController.popBackStack() },
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
        container.push.registerCurrentToken()
    }
}

private const val RECONNECT_DELAY_MS = 5_000L
