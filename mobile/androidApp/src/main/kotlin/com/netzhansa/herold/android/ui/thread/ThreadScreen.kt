package com.netzhansa.herold.android.ui.thread

import android.annotation.SuppressLint
import android.content.Intent
import android.os.Message
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Forward
import androidx.compose.material.icons.automirrored.filled.Reply
import androidx.compose.material.icons.automirrored.filled.ReplyAll
import androidx.compose.material.icons.filled.Archive
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.MarkEmailUnread
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.outlined.StarBorder
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.AssistChip
import androidx.compose.material3.AssistChipDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.diag.DiagLog
import com.netzhansa.herold.android.push.ActiveThread
import com.netzhansa.herold.android.push.MailNotifier
import com.netzhansa.herold.android.links.ExternalBrowser
import com.netzhansa.herold.android.media.ImageScaling
import com.netzhansa.herold.android.ui.common.SnoozeSheet
import com.netzhansa.herold.android.ui.common.bottomSystemBarsPadding
import com.netzhansa.herold.android.ui.common.StatusIndicator
import com.netzhansa.herold.android.ui.common.UndoOffers
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.actions.PendingAction
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.actions.SnoozeWakeMessages
import com.netzhansa.herold.shared.actions.UndoMessages
import com.netzhansa.herold.shared.compose.ComposeMode
import com.netzhansa.herold.shared.compose.HtmlText
import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.links.AppLinks
import com.netzhansa.herold.shared.links.BodyLinkAction
import com.netzhansa.herold.shared.links.BodyLinks
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.actions.FilterActions
import com.netzhansa.herold.shared.mail.BodyPreference
import com.netzhansa.herold.shared.mail.BodyVariant
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import com.netzhansa.herold.shared.mail.ListHeaders
import com.netzhansa.herold.shared.mail.MessageDates
import com.netzhansa.herold.shared.mail.SenderAvatar
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.outbox.PendingMessage
import com.netzhansa.herold.shared.outbox.pendingMessagesIn
import com.netzhansa.herold.shared.sync.SyncStatus
import com.netzhansa.herold.shared.sync.appStatus
import com.netzhansa.herold.shared.mail.UnsubscribeMessages
import com.netzhansa.herold.shared.mail.UnsubscribeOffer
import com.netzhansa.herold.shared.push.MailNotification
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.datetime.Clock
import kotlinx.datetime.TimeZone
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import java.io.ByteArrayInputStream
import java.util.concurrent.atomic.AtomicBoolean

/**
 * The thread accordion: every message of the conversation, collapsed except
 * the newest, with the body rendered in a JavaScript-free WebView. Inline
 * images resolve out of the blob cache through the JMAP download endpoint;
 * remote images stay blocked until the user asks for them.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ThreadScreen(
    container: AppContainer,
    session: SessionScope,
    accountId: String,
    threadId: String,
    onCompose: (mode: ComposeMode, emailId: String) -> Unit,
    /** Opens the filter editor seeded from a message (suite REQ-FLT-32). */
    onCreateFilter: (fromEmail: String, subject: String) -> Unit,
    /** Opens the composer on a `mailto:` unsubscribe (REQ-UNS-22). */
    onComposeTo: (to: String, subject: String, body: String) -> Unit,
    /** Opens the outbox, which is where a queued message is acted on (issue #369). */
    onOutbox: () -> Unit,
    /** Opens the diagnostics screen, which the status indicator leads to (issue #421). */
    onDiagnostics: () -> Unit,
    onReportProblem: () -> Unit,
    onBack: () -> Unit,
) {
    val messages by container.store.threadEmails(accountId, threadId).collectAsStateSafely(emptyList())
    val queued by container.outbox.entries.collectAsStateSafely(emptyList())
    // A draft reply belongs to its conversation and is rendered at the end
    // of it, with Edit reopening the composer on it (issue #371).
    val drafts = remember(messages) { messages.filter { it.keywords.contains(Keywords.DRAFT) } }
    val conversation = remember(messages) { messages.filterNot { it.keywords.contains(Keywords.DRAFT) } }
    // What this conversation has waiting: a reply written offline shows
    // here until the drain has put the server's copy in the store
    // (issue #369).
    val pending = remember(queued, accountId, threadId) {
        queued.filter { it.isPending }.pendingMessagesIn(accountId, threadId)
    }
    val mailboxes by container.store.mailboxes().collectAsStateSafely(emptyList())
    val rules by container.store.managedRules().collectAsStateSafely(emptyList())
    // What the app bar's dot says here, derived exactly as the list
    // derives it (REQ-AND-SYNC-30, issue #421).
    val offline by container.offline.collectAsStateSafely(false)
    val syncStatus by session.syncEngine.status.collectAsStateSafely(SyncStatus.Idle)
    val status = appStatus(
        offline = offline,
        sync = syncStatus,
        pendingOutbox = queued.count { it.isPending },
        failedOutbox = queued.count { it.state == OutboxState.FAILED },
    )
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val snackbar = remember { SnackbarHostState() }
    var expandedId by remember { mutableStateOf<String?>(null) }
    var loadRemoteImages by remember { mutableStateOf(false) }
    var snoozing by remember { mutableStateOf(false) }
    var fetching by remember { mutableStateOf(false) }
    var unavailable by remember { mutableStateOf(false) }
    var viewing by remember { mutableStateOf<Attachment?>(null) }
    var inspecting by remember { mutableStateOf<String?>(null) }
    var blocking by remember { mutableStateOf<String?>(null) }
    var unsubscribing by remember { mutableStateOf(false) }
    val darkTheme = isSystemInDarkTheme()

    /**
     * What a tap on a link in a message body does (issue #425). Every
     * navigation the body attempts arrives here and is acted on outside
     * the WebView, so the message stays on screen whatever the link is.
     */
    val openBodyLink: (String) -> Unit = { url ->
        when (val action = BodyLinks.route(url)) {
            is BodyLinkAction.OpenExternally -> ExternalBrowser.open(context, action.url)
            is BodyLinkAction.Compose -> onComposeTo(
                action.prefill.to.joinToString(", "),
                action.prefill.subject,
                action.prefill.body,
            )

            is BodyLinkAction.HandOff -> ExternalBrowser.view(context, action.uri)
            BodyLinkAction.Ignore -> Unit
        }
    }

    // What an image is decoded for: the column it is drawn in, never its
    // own resolution (issue #341).
    val displayWidthPx = with(LocalDensity.current) {
        LocalConfiguration.current.screenWidthDp.dp.toPx().toInt()
    }

    /**
     * The attachment's bytes: from the spool while the message that
     * carries it is still queued, from the blob cache once the server
     * holds it, downloading it once (issue #380).
     */
    suspend fun blobOf(attachment: Attachment): ByteArray? = runCatching {
        PendingMessage.spoolHandle(attachment.blobId)?.let { handle ->
            return@runCatching container.spool.read(handle)
        }
        session.syncEngine.blob(accountId, attachment.blobId, attachment.type, attachment.name)
    }.onFailure { failure ->
        // A part the store or the network cannot produce is drawn as
        // unavailable; it never takes the screen with it (issue #420).
        DiagLog.e(
            THREAD_TAG,
            "the part ${attachment.blobId} could not be loaded: ${failure::class.simpleName}: ${failure.message}",
        )
    }.getOrNull()

    /**
     * What a body's `cid:` reference resolves to: the part's bytes,
     * decoded at the width the column draws them at (issue #341). A part
     * that cannot be loaded or decoded resolves to nothing and the body
     * draws the image as missing, which is what keeps a newsletter with
     * an oversized inline image readable (issue #420). The type comes
     * back with the bytes, since scaling an image can change the format
     * it is written in (issue #445).
     */
    fun inlineImageOf(attachments: List<Attachment>, cid: String): Pair<String, ByteArray>? {
        val attachment = attachments.firstOrNull {
            it.cid?.trim('<', '>') == cid || it.name == cid
        } ?: return null
        return runBlocking(Dispatchers.IO) {
            val bytes = blobOf(attachment) ?: return@runBlocking null
            runCatching { ImageScaling.forDisplay(attachment.type, bytes, displayWidthPx) }
                .onFailure {
                    DiagLog.e(
                        THREAD_TAG,
                        "the inline part ${attachment.blobId} could not be decoded: ${it.message}",
                    )
                }
                .getOrNull()
        }
    }

    // A thread reached from search or a notification can be outside the
    // synced set, and a thread the fill partly covered can be missing a
    // member that lives in a mailbox the device does not sync (issue
    // #339, #461, REQ-AND-SYNC-01); the store is still the source of
    // truth, so the sync engine completes it there and the screen renders
    // from there. The spinner is reserved for the case nothing is cached
    // yet - a thread already showing something completes quietly.
    LaunchedEffect(accountId, threadId) {
        fetching = container.store.threadEmailList(accountId, threadId).isEmpty()
        val held = session.syncEngine.ensureThread(accountId, threadId)
        fetching = false
        unavailable = !held
    }

    // A push for the thread on screen reconciles but posts no notification,
    // and any notification already in the shade for it is withdrawn
    // (architecture 04-push.md).
    DisposableEffect(accountId, threadId) {
        ActiveThread.entered(accountId, threadId)
        MailNotifier.cancel(context, MailNotification.tagFor(accountId, threadId))
        onDispose { ActiveThread.left(accountId, threadId) }
    }

    // The conversation's wake time, when the server holds one for it
    // (suite REQ-SNZ-12): the indicator states it and offers the edit and
    // the cancel.
    val snoozedUntil = conversation.firstNotNullOfOrNull { it.snoozedUntil }

    // The reminder that brought the conversation back (issue #470). The
    // server clears the marker when the conversation is read, which
    // opening it does, so the banner is latched for the visit: the answer
    // to "why is this here again" stays up while it is being read, and is
    // gone the next time the conversation is opened.
    var wokeFor by remember(accountId, threadId) { mutableStateOf<String?>(null) }
    val wokeForOnServer = conversation.firstNotNullOfOrNull { it.snoozeWokeFor }
    LaunchedEffect(wokeForOnServer) {
        if (wokeForOnServer != null) wokeFor = wokeForOnServer
    }

    /** The address a block and a seeded filter act on. */
    val newestSender = conversation.lastOrNull()?.fromEmail.orEmpty()

    /**
     * The conversation's unsubscribe mechanism, from the newest message
     * that advertises one (REQ-UNS-11): the affordance belongs to the
     * thread even though the header is per message.
     */
    val offer = UnsubscribeOffer.of(conversation)

    val newestPending = pending.lastOrNull()

    val newest = conversation.lastOrNull()
    LaunchedEffect(newest?.id) {
        val target = conversation.lastOrNull { it.isUnread } ?: newest
        if (target != null) {
            expandedId = target.id
            session.syncEngine.loadBody(accountId, target.id)
            if (target.isUnread) session.actions.setSeen(listOf(target), true)
        }
        Unit
    }

    // A queued reply is the newest message in the conversation, so it is
    // the one the thread opens on (issue #380). It follows the effect
    // above, which opens the newest message the store holds.
    LaunchedEffect(newestPending?.entryId, newest?.id) {
        newestPending?.let { expandedId = PendingMessage.ID_PREFIX + it.entryId }
    }

    /**
     * This screen's undo surface, told apart from the list's so an offer
     * handed on as the view pops is shown where the user lands (issue #378).
     */
    val undoSurface = remember { Any() }

    /**
     * An action that takes the conversation off this screen: the local
     * write is already in the store, the offer is parked for the list, and
     * the view pops back so the undo appears where the user lands
     * (issue #345). Popping back happens on the main thread, which a
     * coroutine resumed off it must return to.
     */
    suspend fun leaveWith(action: PendingAction, message: String) {
        container.undo.offer(message, action, session.actions, handOnFrom = undoSurface)
        withContext(Dispatchers.Main.immediate) { onBack() }
    }

    // A send started here returns here, so the "Sending / Undo" offer is
    // raised on this screen as well as on the list (issue #368). The
    // offers this screen hands on as it pops are left for the list.
    UndoOffers(container = container, snackbar = snackbar, surface = undoSurface)

    /** What the conversation's subject reads as, wherever it is shown. */
    val subject = messages.firstOrNull { it.subject.isNotBlank() }?.subject
        ?: if (messages.isEmpty() && unavailable) "Conversation" else "(no subject)"

    /**
     * The mailboxes and labels the conversation sits in, named beside the
     * subject the way the suite names them on a thread (issue #428).
     */
    val chips = remember(conversation, mailboxes, accountId) {
        val held = conversation.flatMap { it.mailboxIds }.toSet()
        mailboxes.filter { it.accountId == accountId && it.id in held }
            .sortedWith(compareBy({ it.role == null }, { it.name }))
            .map { it.name }
    }

    /** What the system sheet is handed when the conversation is shared. */
    fun shareConversation() {
        context.startActivity(
            Intent.createChooser(
                Intent(Intent.ACTION_SEND).apply {
                    type = "text/plain"
                    putExtra(Intent.EXTRA_SUBJECT, subject)
                    putExtra(Intent.EXTRA_TEXT, AppLinks.suiteThreadUrl(session.baseUrl, threadId))
                },
                "Share conversation",
            ),
        )
    }

    /**
     * Marks [from] and every later message unread and leaves the
     * conversation, which is what the reader asks for when they mark a
     * conversation they are finished with (issue #428).
     */
    fun markUnreadFrom(from: Email?) {
        val index = if (from == null) 0 else conversation.indexOfFirst { it.id == from.id }.coerceAtLeast(0)
        val target = conversation.drop(index)
        if (target.isEmpty()) return
        scope.launch {
            session.actions.setSeen(target, false)
            withContext(Dispatchers.Main.immediate) { onBack() }
        }
    }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbar, modifier = Modifier.testTag("thread-snackbar")) },
        topBar = {
            TopAppBar(
                modifier = Modifier.testTag("thread-app-bar"),
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("thread-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                // The subject is a heading in the content, so the bar
                // carries the conversation's actions alone (issue #428).
                title = {},
                actions = {
                    // The same fixed slot the list carries, so the
                    // conversation does not move when the connection
                    // drops or a sync starts (REQ-AND-SYNC-30).
                    StatusIndicator(status = status, onOpenDiagnostics = onDiagnostics)
                    IconButton(
                        onClick = { scope.launch { leaveWith(session.actions.archiveLocally(conversation, mailboxes), UndoMessages.ARCHIVED) } },
                        modifier = Modifier.testTag("thread-archive"),
                    ) {
                        Icon(Icons.Filled.Archive, contentDescription = "Archive")
                    }
                    IconButton(
                        onClick = { scope.launch { leaveWith(session.actions.deleteLocally(conversation, mailboxes), UndoMessages.DELETED) } },
                        modifier = Modifier.testTag("thread-delete"),
                    ) {
                        Icon(Icons.Filled.Delete, contentDescription = "Delete")
                    }
                    IconButton(
                        onClick = { markUnreadFrom(conversation.firstOrNull()) },
                        modifier = Modifier.testTag("thread-unread"),
                    ) {
                        Icon(Icons.Filled.MarkEmailUnread, contentDescription = "Mark unread")
                    }
                    ThreadOverflow(
                        muted = rules.any { FilterActions.isThreadMuteRule(it, threadId) },
                        onMute = { muted ->
                            scope.launch {
                                session.filters.setMuted(accountId, threadId, muted)
                                snackbar.showSnackbar(if (muted) "Conversation muted" else "Conversation unmuted")
                            }
                        },
                        onSnooze = { snoozing = true },
                        onShare = { shareConversation() },
                        onReportProblem = onReportProblem,
                    )
                },
            )
        },
        bottomBar = {
            ReplyBar(
                enabled = conversation.isNotEmpty(),
                onReply = { conversation.lastOrNull()?.let { onCompose(ComposeMode.REPLY, it.id) } },
                onReplyAll = { conversation.lastOrNull()?.let { onCompose(ComposeMode.REPLY_ALL, it.id) } },
                onForward = { conversation.lastOrNull()?.let { onCompose(ComposeMode.FORWARD, it.id) } },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding)) {
        wokeFor?.let { WokeFromSnoozeBanner(wakeAt = it) }
        snoozedUntil?.let { wakeAt ->
            SnoozedIndicator(
                wakeAt = wakeAt,
                onEdit = { snoozing = true },
                onCancel = { scope.launch { session.actions.unsnooze(conversation) } },
            )
        }
        offer?.let { current ->
            UnsubscribeBar(
                busy = unsubscribing,
                onClick = {
                    when (val mechanism = current.mechanism) {
                        is ListHeaders.Mechanism.OneClick -> scope.launch {
                            // REQ-UNS-30: no confirmation; that is the
                            // point of RFC 8058.
                            unsubscribing = true
                            val result = session.unsubscribe.postOneClick(mechanism.url)
                            unsubscribing = false
                            snackbar.showSnackbar(
                                if (result.ok) {
                                    UnsubscribeMessages.success(current.senderDisplay)
                                } else {
                                    UnsubscribeMessages.FAILED
                                },
                            )
                        }

                        is ListHeaders.Mechanism.Https -> ExternalBrowser.open(context, mechanism.url)

                        is ListHeaders.Mechanism.Mailto -> {
                            val fields = ListHeaders.parseMailto(mechanism.url)
                            onComposeTo(fields.to, fields.subject, fields.body)
                        }

                        is ListHeaders.Mechanism.HttpOnly -> scope.launch {
                            snackbar.showSnackbar(UnsubscribeMessages.CLEARTEXT)
                        }
                    }
                },
            )
        }
        if (messages.isEmpty() && fetching) {
            Box(
                modifier = Modifier.fillMaxWidth().padding(32.dp).testTag("thread-loading"),
                contentAlignment = Alignment.Center,
            ) {
                CircularProgressIndicator()
            }
        }
        if (messages.isEmpty() && unavailable) {
            Text(
                text = "This conversation could not be loaded. Connect and try again.",
                modifier = Modifier.fillMaxWidth().padding(24.dp).testTag("thread-unavailable"),
            )
        }
        LazyColumn(
            modifier = Modifier.fillMaxSize().testTag("thread-messages"),
        ) {
            item(key = "subject") {
                SubjectBlock(
                    subject = subject,
                    chips = chips,
                    flagged = conversation.any { it.isFlagged },
                    onToggleStar = {
                        val flagged = conversation.any { it.isFlagged }
                        scope.launch { session.actions.setFlagged(conversation, !flagged) }
                    },
                )
            }
            items(conversation, key = { it.id }) { message ->
                MessageCard(
                    message = message,
                    expanded = expandedId == message.id,
                    darkTheme = darkTheme,
                    loadRemoteImages = loadRemoteImages,
                    onToggle = {
                        expandedId = if (expandedId == message.id) null else message.id
                        if (expandedId == message.id) {
                            scope.launch {
                                session.syncEngine.loadBody(accountId, message.id)
                                if (message.isUnread) session.actions.setSeen(listOf(message), true)
                            }
                        }
                    },
                    onShowRemoteImages = { loadRemoteImages = true },
                    resolveRemoteImage = { url ->
                        runBlocking(Dispatchers.IO) {
                            session.imageProxy.fetch(url)?.let { it.contentType to it.bytes }
                        }
                    },
                    resolveInlineImage = { cid -> inlineImageOf(message.attachments, cid) },
                    loadBlob = { attachment -> blobOf(attachment) },
                    onOpenAttachment = { attachment -> viewing = attachment },
                    onLink = openBodyLink,
                    actions = MessageActions(
                        onReply = { onCompose(ComposeMode.REPLY, message.id) },
                        onReplyAll = { onCompose(ComposeMode.REPLY_ALL, message.id) },
                        onForward = { onCompose(ComposeMode.FORWARD, message.id) },
                        onStar = { scope.launch { session.actions.setFlagged(listOf(message), !message.isFlagged) } },
                        onMarkUnreadFrom = { markUnreadFrom(message) },
                        onBlock = { blocking = message.fromEmail },
                        onCreateFilter = { onCreateFilter(message.fromEmail, message.subject) },
                        onInspect = { inspecting = message.id },
                    ),
                )
                HorizontalDivider()
            }
            items(drafts, key = { "draft-" + it.id }) { draft ->
                DraftMessageCard(
                    draft = draft,
                    onEdit = { onCompose(ComposeMode.EDIT_DRAFT, draft.id) },
                    onDiscard = {
                        scope.launch { session.drafts.discardSaved(draft.accountId, draft.id) }
                    },
                )
                HorizontalDivider()
            }
            items(pending, key = { "pending-" + it.entryId }) { message ->
                // The queued message is read the way a sent one is: same
                // composable, same accordion, with a chip stating what it
                // is waiting for and opening the outbox (issue #380).
                val queued = message.asEmail()
                MessageCard(
                    message = queued,
                    expanded = expandedId == queued.id,
                    darkTheme = darkTheme,
                    loadRemoteImages = loadRemoteImages,
                    onToggle = { expandedId = if (expandedId == queued.id) null else queued.id },
                    onShowRemoteImages = { loadRemoteImages = true },
                    resolveRemoteImage = { url ->
                        runBlocking(Dispatchers.IO) {
                            session.imageProxy.fetch(url)?.let { it.contentType to it.bytes }
                        }
                    },
                    resolveInlineImage = { cid ->
                        inlineImageOf(queued.attachments, cid)
                    },
                    loadBlob = { attachment -> blobOf(attachment) },
                    onOpenAttachment = { attachment -> viewing = attachment },
                    onLink = openBodyLink,
                    tag = "thread-pending-${message.entryId}",
                    status = {
                        PendingMarker(message = message, onOpenOutbox = onOutbox)
                    },
                )
                HorizontalDivider()
            }
        }
        }
    }

    viewing?.let { attachment ->
        AttachmentViewer(
            attachment = attachment,
            maxEdgePx = displayWidthPx,
            loadBlob = { blobOf(it) },
            onDismiss = { viewing = null },
        )
    }

    inspecting?.let { id ->
        LlmInspectSheet(
            session = session,
            accountId = accountId,
            emailId = id,
            onDismiss = { inspecting = null },
        )
    }

    blocking?.let { address ->
        AlertDialog(
            onDismissRequest = { blocking = null },
            title = { Text("Block $address?") },
            text = { Text("Mail from this sender goes straight to Trash. You can undo this in Filters.") },
            confirmButton = {
                TextButton(
                    onClick = {
                        blocking = null
                        scope.launch {
                            session.filters.blockSender(accountId, address)
                            snackbar.showSnackbar("Blocked $address")
                        }
                    },
                    modifier = Modifier.testTag("thread-block-confirm"),
                ) { Text("Block") }
            },
            dismissButton = { TextButton(onClick = { blocking = null }) { Text("Cancel") } },
        )
    }

    if (snoozing) {
        SnoozeSheet(
            onDismiss = { snoozing = false },
            onPick = { wakeAt ->
                snoozing = false
                scope.launch {
                    if (snoozedUntil != null) {
                        // Editing the wake time of a conversation that is
                        // already snoozed keeps it on screen: it is not in
                        // the list this view would return to.
                        session.actions.snooze(messages, wakeAt)
                    } else {
                        leaveWith(session.actions.snoozeLocally(messages, wakeAt), UndoMessages.SNOOZED)
                    }
                }
            },
        )
    }
}

/**
 * A draft answer to this conversation, rendered at the end of it the way
 * the Suite threads drafts (`docs/design/web/requirements/19-drafts.md`,
 * issue #371). Edit reopens the composer on it; sending it removes it;
 * Discard throws it away, which is what a reader reaches for once the
 * snackbar the save offered has come down.
 */
@Composable
private fun DraftMessageCard(draft: Email, onEdit: () -> Unit, onDiscard: () -> Unit) {
    ListItem(
        headlineContent = {
            Text(
                text = draft.fromEmail.ifBlank { "You" },
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.Bold,
            )
        },
        supportingContent = {
            Column {
                Text(
                    text = "to " + draft.toLine.ifBlank { "(no recipient)" },
                    style = MaterialTheme.typography.bodySmall,
                )
                Text(
                    text = draft.preview.ifBlank {
                        HtmlText.toPlainText(draft.bodyHtml ?: draft.bodyText.orEmpty()).trim()
                    }.take(PENDING_PREVIEW_CHARS),
                    style = MaterialTheme.typography.bodySmall,
                    maxLines = 2,
                )
            }
        },
        trailingContent = {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    text = PendingMessage.MARKER_DRAFT,
                    style = MaterialTheme.typography.labelMedium,
                    color = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.testTag("thread-draft-marker-${draft.id}"),
                )
                TextButton(
                    onClick = onEdit,
                    modifier = Modifier.testTag("thread-draft-edit-${draft.id}"),
                ) {
                    Text("Edit")
                }
                IconButton(
                    onClick = onDiscard,
                    modifier = Modifier.testTag("thread-draft-discard-${draft.id}"),
                ) {
                    Icon(Icons.Filled.Delete, contentDescription = "Discard this draft")
                }
            }
        },
        modifier = Modifier
            .fillMaxWidth()
            .clickable { onEdit() }
            .testTag("thread-draft-${draft.id}"),
    )
}

/**
 * What a waiting message is waiting for (issue #369): the marker on the
 * message, which is also the way to the outbox, where the entry is
 * retried or discarded (issue #380).
 */
@Composable
private fun PendingMarker(message: PendingMessage, onOpenOutbox: () -> Unit) {
    val failed = message.failure != null
    AssistChip(
        onClick = onOpenOutbox,
        label = {
            Text(
                text = message.marker,
                style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.testTag("thread-pending-marker-${message.entryId}"),
            )
        },
        colors = AssistChipDefaults.assistChipColors(
            labelColor = if (failed) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary,
        ),
        modifier = Modifier.testTag("thread-pending-outbox-${message.entryId}"),
    )
}

/** How much of a waiting message's body the thread previews. */
private const val PENDING_PREVIEW_CHARS = 200

/**
 * The conversation's overflow: what applies to the whole conversation
 * rather than to one of its messages - mute, snooze, share, and the way
 * to report a problem (suite REQ-MAIL-136, REQ-SNZ-01, REQ-AND-SYS-02).
 * The per-message entries live on the card (issue #428).
 */
@Composable
private fun ThreadOverflow(
    muted: Boolean,
    onMute: (Boolean) -> Unit,
    onSnooze: () -> Unit,
    onShare: () -> Unit,
    onReportProblem: () -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    IconButton(onClick = { open = true }, modifier = Modifier.testTag("thread-overflow")) {
        Icon(Icons.Filled.MoreVert, contentDescription = "More")
    }
    DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
        DropdownMenuItem(
            text = { Text(if (muted) "Unmute conversation" else "Mute conversation") },
            onClick = {
                open = false
                onMute(!muted)
            },
            modifier = Modifier.testTag("thread-mute"),
        )
        DropdownMenuItem(
            text = { Text("Snooze") },
            onClick = {
                open = false
                onSnooze()
            },
            modifier = Modifier.testTag("thread-snooze"),
        )
        DropdownMenuItem(
            text = { Text("Share") },
            onClick = {
                open = false
                onShare()
            },
            modifier = Modifier.testTag("thread-share"),
        )
        DropdownMenuItem(
            text = { Text("Report a problem") },
            onClick = {
                open = false
                onReportProblem()
            },
            modifier = Modifier.testTag("thread-report-problem"),
        )
    }
}

/**
 * What a message on the card can be acted on with (issue #428). A
 * message the server does not hold yet carries none of these: it is
 * answered from the outbox, not from the conversation.
 */
private class MessageActions(
    val onReply: () -> Unit,
    val onReplyAll: () -> Unit,
    val onForward: () -> Unit,
    val onStar: () -> Unit,
    val onMarkUnreadFrom: () -> Unit,
    val onBlock: () -> Unit,
    val onCreateFilter: () -> Unit,
    val onInspect: () -> Unit,
)

/**
 * One message's own overflow: the answers, the star, marking the
 * conversation unread from here, and the entries that belong to the
 * message rather than to the conversation - blocking its sender, a
 * filter seeded from it, and what the classifier made of it (suite
 * REQ-MAIL-138, REQ-FLT-32, G7).
 */
@Composable
private fun MessageOverflow(message: Email, actions: MessageActions) {
    var open by remember { mutableStateOf(false) }
    IconButton(
        onClick = { open = true },
        modifier = Modifier.size(ACTION_ICON_DP.dp).testTag("message-overflow-${message.id}"),
    ) {
        Icon(Icons.Filled.MoreVert, contentDescription = "More", modifier = Modifier.size(ICON_DP.dp))
    }
    DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
        DropdownMenuItem(
            text = { Text("Reply") },
            onClick = {
                open = false
                actions.onReply()
            },
            modifier = Modifier.testTag("message-menu-reply-${message.id}"),
        )
        DropdownMenuItem(
            text = { Text("Reply all") },
            onClick = {
                open = false
                actions.onReplyAll()
            },
            modifier = Modifier.testTag("message-menu-reply-all-${message.id}"),
        )
        DropdownMenuItem(
            text = { Text("Forward") },
            onClick = {
                open = false
                actions.onForward()
            },
            modifier = Modifier.testTag("message-menu-forward-${message.id}"),
        )
        DropdownMenuItem(
            text = { Text(if (message.isFlagged) "Unstar this message" else "Star this message") },
            onClick = {
                open = false
                actions.onStar()
            },
            modifier = Modifier.testTag("message-menu-star-${message.id}"),
        )
        DropdownMenuItem(
            text = { Text("Mark unread from here") },
            onClick = {
                open = false
                actions.onMarkUnreadFrom()
            },
            modifier = Modifier.testTag("message-menu-unread-${message.id}"),
        )
        DropdownMenuItem(
            text = { Text("Block ${message.fromEmail}") },
            onClick = {
                open = false
                actions.onBlock()
            },
            enabled = message.fromEmail.isNotBlank(),
            modifier = Modifier.testTag("thread-block"),
        )
        DropdownMenuItem(
            text = { Text("Create filter from this message") },
            onClick = {
                open = false
                actions.onCreateFilter()
            },
            modifier = Modifier.testTag("thread-create-filter"),
        )
        DropdownMenuItem(
            text = { Text("Why is this here?") },
            onClick = {
                open = false
                actions.onInspect()
            },
            modifier = Modifier.testTag("thread-why"),
        )
    }
}

/**
 * The conversation's heading: the subject over the mailboxes and labels
 * it sits in, with the star at its right. It is the first row of the
 * scrolling content, so it leaves the screen with the conversation
 * (issue #428).
 */
@Composable
private fun SubjectBlock(
    subject: String,
    chips: List<String>,
    flagged: Boolean,
    onToggleStar: () -> Unit,
) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(start = 16.dp, end = 4.dp, top = 8.dp, bottom = 4.dp),
        verticalAlignment = Alignment.Top,
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = subject,
                style = MaterialTheme.typography.headlineSmall,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.testTag("thread-title"),
            )
            if (chips.isNotEmpty()) {
                Row(
                    modifier = Modifier
                        .padding(top = 6.dp)
                        .horizontalScroll(rememberScrollState()),
                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    chips.forEach { name -> LabelChip(name) }
                }
            }
        }
        IconButton(onClick = onToggleStar, modifier = Modifier.testTag("thread-star")) {
            Icon(
                imageVector = if (flagged) Icons.Filled.Star else Icons.Outlined.StarBorder,
                contentDescription = if (flagged) "Unstar" else "Star",
            )
        }
    }
}

/** One mailbox or label the conversation sits in. */
@Composable
private fun LabelChip(name: String) {
    Surface(
        color = MaterialTheme.colorScheme.surfaceVariant,
        shape = MaterialTheme.shapes.small,
        modifier = Modifier.testTag("thread-label-$name"),
    ) {
        Text(
            text = name,
            style = MaterialTheme.typography.labelSmall,
            maxLines = 1,
            modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp),
        )
    }
}

/**
 * The Unsubscribe affordance, between the subject and the action bar
 * (REQ-UNS-10). It is present whenever the conversation advertises a
 * mechanism, including a cleartext one, which on tap surfaces the refusal
 * rather than opening it (REQ-UNS-03/04).
 */
@Composable
private fun UnsubscribeBar(busy: Boolean, onClick: () -> Unit) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        TextButton(
            onClick = onClick,
            enabled = !busy,
            modifier = Modifier.testTag("thread-unsubscribe"),
        ) {
            Text(UnsubscribeMessages.BUTTON)
        }
    }
}

/**
 * Why the conversation is back: a reminder set for [wakeAt] fell due and
 * the server released it into the inbox (issue #470, suite REQ-SNZ-11).
 * The banner stands until the conversation is read, which is when the
 * server clears the wake marker.
 */
@Composable
private fun WokeFromSnoozeBanner(wakeAt: String) {
    val label = SnoozeWakeMessages.label(wakeAt, Clock.System.now(), TimeZone.currentSystemDefault())
    Surface(
        color = MaterialTheme.colorScheme.tertiaryContainer,
        modifier = Modifier.fillMaxWidth().testTag("thread-woke"),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Icon(Icons.Filled.Schedule, contentDescription = null)
            Text(
                text = SnoozeWakeMessages.banner(label),
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.weight(1f).testTag("thread-woke-for"),
            )
        }
    }
}

/**
 * The snoozed banner: when the conversation wakes, with the edit and the
 * cancel next to it (suite REQ-SNZ-12). Cancelling clears `snoozedUntil`,
 * which herold pairs with the `$snoozed` keyword, so the conversation is
 * back in the inbox at once.
 */
@Composable
private fun SnoozedIndicator(
    wakeAt: String,
    onEdit: () -> Unit,
    onCancel: () -> Unit,
) {
    val zone = TimeZone.currentSystemDefault()
    val wakeLabel = SnoozeClock.parseWake(wakeAt)
        ?.let { SnoozeClock.describe(it, Clock.System.now(), zone) }
        ?: wakeAt
    Surface(
        color = MaterialTheme.colorScheme.secondaryContainer,
        modifier = Modifier.fillMaxWidth().testTag("thread-snoozed"),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Icon(Icons.Filled.Schedule, contentDescription = null)
            Text(
                text = "Snoozed until $wakeLabel",
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.weight(1f).testTag("thread-snoozed-until"),
            )
            TextButton(onClick = onEdit, modifier = Modifier.testTag("thread-snooze-edit")) {
                Text("Edit")
            }
            TextButton(onClick = onCancel, modifier = Modifier.testTag("thread-snooze-cancel")) {
                Text("Cancel")
            }
        }
    }
}

/**
 * Reply, reply-all and forward for the conversation, as pills pinned
 * below it and acting on its newest message - the one a reply answers
 * (suite REQ-MAIL-30, issue #428).
 */
@Composable
private fun ReplyBar(
    enabled: Boolean,
    onReply: () -> Unit,
    onReplyAll: () -> Unit,
    onForward: () -> Unit,
) {
    // The surface paints to the bottom of the screen; the row inside it
    // stands above whatever the system holds there, so no pill lands
    // under the gesture handle (issue #428).
    Surface(tonalElevation = 2.dp, modifier = Modifier.testTag("thread-reply-surface")) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .bottomSystemBarsPadding()
                .padding(horizontal = 12.dp, vertical = 8.dp)
                .testTag("thread-reply-bar"),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            ReplyPill("Reply", Icons.AutoMirrored.Filled.Reply, enabled, onReply, "thread-reply")
            ReplyPill("Reply all", Icons.AutoMirrored.Filled.ReplyAll, enabled, onReplyAll, "thread-reply-all")
            ReplyPill("Forward", Icons.AutoMirrored.Filled.Forward, enabled, onForward, "thread-forward")
        }
    }
}

@Composable
private fun RowScope.ReplyPill(
    label: String,
    icon: androidx.compose.ui.graphics.vector.ImageVector,
    enabled: Boolean,
    onClick: () -> Unit,
    tag: String,
) {
    OutlinedButton(
        onClick = onClick,
        enabled = enabled,
        shape = CircleShape,
        contentPadding = PaddingValues(horizontal = 8.dp, vertical = 6.dp),
        modifier = Modifier.weight(1f).testTag(tag),
    ) {
        Icon(icon, contentDescription = null, modifier = Modifier.size(ICON_DP.dp))
        Spacer(Modifier.width(6.dp))
        Text(text = label, style = MaterialTheme.typography.labelLarge, maxLines = 1)
    }
}

/** The avatar circle: the sender's initials over the colour of their address. */
@Composable
private fun SenderAvatarCircle(name: String, address: String, tag: String) {
    Box(
        modifier = Modifier
            .size(AVATAR_DP.dp)
            .clip(CircleShape)
            .background(Color(SenderAvatar.colourFor(address)))
            .testTag(tag),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = SenderAvatar.initialsFor(name, address),
            color = Color.White,
            style = MaterialTheme.typography.titleMedium,
        )
    }
}

/**
 * One message of the conversation (issue #428): the sender's avatar, the
 * display name with the date beside it, the recipients line that opens
 * onto the full addresses and timestamp, and the message's own reply and
 * overflow. Collapsed it keeps its one-line preview; expanded it renders
 * the body, the remote-image bar and the attachments below this header.
 */
@Composable
private fun MessageCard(
    message: Email,
    expanded: Boolean,
    darkTheme: Boolean,
    loadRemoteImages: Boolean,
    onToggle: () -> Unit,
    onShowRemoteImages: () -> Unit,
    resolveRemoteImage: (String) -> Pair<String, ByteArray>?,
    resolveInlineImage: (String) -> Pair<String, ByteArray>?,
    loadBlob: suspend (Attachment) -> ByteArray?,
    onOpenAttachment: (Attachment) -> Unit,
    /** Where a link in the body goes (issue #425). */
    onLink: (String) -> Unit,
    /** What this message can be acted on with; absent for a queued one. */
    actions: MessageActions? = null,
    /** What the message is tagged with, for the instrumented checks. */
    tag: String = "message-${message.id}",
    /** The state marker a message that is not on the server yet carries. */
    status: (@Composable () -> Unit)? = null,
) {
    val zone = TimeZone.currentSystemDefault()
    var detailed by remember(message.id) { mutableStateOf(false) }
    Column(modifier = Modifier.fillMaxWidth().testTag(tag)) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .clickable(onClick = onToggle)
                .padding(horizontal = 12.dp, vertical = 8.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            SenderAvatarCircle(
                name = message.fromName,
                address = message.fromEmail,
                tag = "message-avatar-${message.id}",
            )
            Column(modifier = Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        text = message.senderDisplay.ifBlank { message.fromEmail },
                        style = MaterialTheme.typography.titleSmall,
                        fontWeight = FontWeight.Bold,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f, fill = false),
                    )
                    Spacer(Modifier.width(8.dp))
                    Text(
                        text = MessageDates.short(message.receivedAt, Clock.System.now().toEpochMilliseconds(), zone),
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1,
                        modifier = Modifier.testTag("message-date-${message.id}"),
                    )
                }
                Row(
                    modifier = Modifier
                        .clickable { detailed = !detailed }
                        .testTag("message-recipients-${message.id}"),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(
                        text = "to " + message.toLine.ifBlank { "(no recipient)" },
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f, fill = false),
                    )
                    Icon(
                        imageVector = if (detailed) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore,
                        contentDescription = if (detailed) "Hide details" else "Show details",
                        tint = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.size(CHEVRON_DP.dp),
                    )
                }
                if (detailed) {
                    MessageDetails(message = message, zone = zone)
                }
                if (!expanded) {
                    Text(
                        text = message.preview,
                        style = MaterialTheme.typography.bodySmall,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
            status?.invoke()
            actions?.let {
                IconButton(
                    onClick = it.onReply,
                    modifier = Modifier.size(ACTION_ICON_DP.dp).testTag("message-reply-${message.id}"),
                ) {
                    Icon(
                        Icons.AutoMirrored.Filled.Reply,
                        contentDescription = "Reply",
                        modifier = Modifier.size(ICON_DP.dp),
                    )
                }
                MessageOverflow(message = message, actions = it)
            }
        }

        if (expanded) {
            // Which half of a multipart/alternative the reader asked
            // for, once they have asked (issue #430).
            var readerChose by remember(message.id) { mutableStateOf<BodyVariant?>(null) }
            BoxWithConstraints(modifier = Modifier.fillMaxWidth()) {
                // What the card gives the body, in the CSS pixels the
                // WebView lays the document out in: at the document's
                // own `width=device-width, initial-scale=1` one CSS
                // pixel is one density-independent pixel.
                val content = HtmlSanitizer.contentWidthCssPx(maxWidth.value.toInt())
                val htmlBody = message.bodyHtml?.let {
                    HtmlSanitizer.sanitize(
                        it,
                        loadRemoteImages,
                        collapseQuotes = true,
                        fitToWidthCssPx = content,
                    )
                }
                val choice = BodyPreference.choose(
                    hasHtml = htmlBody != null,
                    text = message.bodyText,
                    minimumWidthCssPx = htmlBody?.minimumWidthCssPx ?: 0,
                    contentWidthCssPx = content,
                    readerChose = readerChose,
                )
                val body = when (choice.show) {
                    BodyVariant.Html -> htmlBody
                    BodyVariant.Text -> message.bodyText?.let {
                        HtmlSanitizer.sanitize(
                            HtmlSanitizer.fromPlainText(it, collapseQuotes = true),
                            loadRemoteImages,
                        )
                    }
                }

                Column(modifier = Modifier.fillMaxWidth()) {
                    if (body == null) {
                        Text(
                            text = "Not downloaded. Connect to load this message.",
                            modifier = Modifier.padding(12.dp).testTag("message-body-missing-${message.id}"),
                        )
                    } else {
                        if (body.blockedRemoteImages && !loadRemoteImages) {
                            Row(
                                modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp),
                                verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
                            ) {
                                Text(
                                    text = "Remote images are blocked until you ask for them",
                                    style = MaterialTheme.typography.bodySmall,
                                    modifier = Modifier.weight(1f),
                                )
                                TextButton(
                                    onClick = onShowRemoteImages,
                                    modifier = Modifier.testTag("show-images-${message.id}"),
                                ) {
                                    Text("Show images")
                                }
                            }
                        }
                        choice.offer?.let { other ->
                            BodyVariantBar(
                                messageId = message.id,
                                offer = other,
                                onSwitch = { readerChose = other },
                            )
                        }
                        MessageBodyWebView(
                            html = HtmlSanitizer.document(body.html, darkTheme, content),
                            imageSources = loadRemoteImages to message.attachments.map { it.blobId },
                            resolveInlineImage = resolveInlineImage,
                            resolveRemoteImage = if (loadRemoteImages) resolveRemoteImage else { _ -> null },
                            onLink = onLink,
                            modifier = Modifier.fillMaxWidth().testTag("message-body-${message.id}"),
                        )
                    }
                }
            }

            if (message.attachments.any { !it.isInline }) {
                Text(
                    text = "Attachments",
                    style = MaterialTheme.typography.titleSmall,
                    modifier = Modifier.padding(start = 12.dp, top = 8.dp),
                )
                message.attachments.filter { !it.isInline }.forEach { attachment ->
                    AttachmentRow(
                        attachment = attachment,
                        loadBlob = loadBlob,
                        onOpen = { onOpenAttachment(attachment) },
                    )
                }
            }
        }
    }
}

/**
 * The control that swaps a message's two bodies (issue #430).
 *
 * A `multipart/alternative` whose HTML cannot narrow to the card is read
 * as its text alternative, and the sender's own layout is one tap away;
 * from there the text is one tap back.
 */
@Composable
private fun BodyVariantBar(messageId: String, offer: BodyVariant, onSwitch: () -> Unit) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = when (offer) {
                BodyVariant.Html -> "Shown as text: the sender's layout is wider than the screen"
                BodyVariant.Text -> "Shown as the sender laid it out"
            },
            style = MaterialTheme.typography.bodySmall,
            modifier = Modifier.weight(1f),
        )
        TextButton(
            onClick = onSwitch,
            modifier = Modifier.testTag(
                when (offer) {
                    BodyVariant.Html -> "show-html-$messageId"
                    BodyVariant.Text -> "show-text-$messageId"
                },
            ),
        ) {
            Text(
                when (offer) {
                    BodyVariant.Html -> "Show original"
                    BodyVariant.Text -> "Show text"
                },
            )
        }
    }
}

/**
 * Who the message went to and when, as the chevron opens it: the full
 * to and cc addresses and the timestamp the short date stands for
 * (issue #428).
 */
@Composable
private fun MessageDetails(message: Email, zone: TimeZone) {
    Column(modifier = Modifier.padding(top = 4.dp, bottom = 2.dp).testTag("message-details-${message.id}")) {
        Text(
            text = "from " + message.fromAddress.format(),
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Text(
            text = "to " + message.toAddresses.joinToString(", ") { it.format() }
                .ifBlank { message.toLine.ifBlank { "(no recipient)" } },
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        if (message.ccAddresses.isNotEmpty()) {
            Text(
                text = "cc " + message.ccAddresses.joinToString(", ") { it.format() },
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        Text(
            text = MessageDates.full(message.receivedAt, zone),
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.testTag("message-timestamp-${message.id}"),
        )
    }
}

/**
 * The body surface: a WebView with JavaScript, file access and content
 * access off. Inline images are served from the local blob cache by
 * intercepting the scheme the sanitiser rewrote `cid:` onto; every other
 * network load is refused, so opening a message makes no request the user
 * did not ask for.
 *
 * The WebView shows one document - the message - and never navigates.
 * Once that document is up, every navigation it attempts is handed to
 * [onLink] and refused, including the ones a `target="_blank"` anchor
 * raises as a new window, so a tapped link opens outside the reading pane
 * and the message stays on screen (issue #425). Long-press keeps the
 * platform's own link menu.
 *
 * The client is built once, in the factory block, and lives as long as
 * the WebView does, while what it answers a request with changes while
 * the message is on screen: the remote-image choice swaps
 * [resolveRemoteImage], and attachments arriving after the body first
 * rendered change what [resolveInlineImage] can produce. Every callback
 * therefore reads the value current at request time (issue #440).
 *
 * Internal so the instrumented checks can drive the surface on its own,
 * with resolvers they control and no network.
 */
@SuppressLint("SetJavaScriptEnabled")
@Composable
internal fun MessageBodyWebView(
    html: String,
    /**
     * What the resolvers below can produce for this document as it
     * stands: the remote-image choice, and the parts the message holds.
     * A new value reloads the document, so images that became
     * resolvable after it was first shown are asked for again
     * (issue #440).
     */
    imageSources: Any,
    resolveInlineImage: (String) -> Pair<String, ByteArray>?,
    resolveRemoteImage: (String) -> Pair<String, ByteArray>?,
    onLink: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    val link by rememberUpdatedState(onLink)
    val inlineImage by rememberUpdatedState(resolveInlineImage)
    val remoteImage by rememberUpdatedState(resolveRemoteImage)
    // One loading of the body: a new instance is what the update block
    // below reacts to, and the composable makes one whenever the markup
    // or what the resolvers can serve changes.
    val document = remember(html, imageSources) { BodyDocument(html) }
    // True while the message document itself is loading, which is the one
    // navigation this WebView performs.
    val rendering = remember { AtomicBoolean(true) }
    AndroidView(
        modifier = modifier,
        factory = { context ->
            WebView(context).apply {
                settings.javaScriptEnabled = false
                settings.allowFileAccess = false
                settings.allowContentAccess = false
                settings.loadsImagesAutomatically = true
                // The document's own viewport - `width=device-width,
                // initial-scale=1` from HtmlSanitizer.document - reaches
                // layout only with the wide viewport on. With it, the
                // layout viewport is the card's width and the body's text
                // is the size the pane asks for; the document is written
                // to fit that width before it is loaded, and what still
                // exceeds it scrolls sideways within the body surface
                // (issue #430).
                settings.useWideViewPort = true
                settings.loadWithOverviewMode = true
                // A `target="_blank"` anchor asks for a window rather than
                // navigating; with this on it arrives at onCreateWindow,
                // where it takes the same route as any other link.
                settings.setSupportMultipleWindows(true)
                isVerticalScrollBarEnabled = false
                webViewClient = object : WebViewClient() {
                    override fun shouldOverrideUrlLoading(
                        view: WebView?,
                        request: WebResourceRequest?,
                    ): Boolean {
                        if (rendering.get()) return false
                        link(request?.url?.toString().orEmpty())
                        return true
                    }

                    override fun onPageFinished(view: WebView?, url: String?) {
                        rendering.set(false)
                    }

                    override fun shouldInterceptRequest(
                        view: WebView?,
                        request: WebResourceRequest?,
                    ): WebResourceResponse? {
                        val url = request?.url?.toString() ?: return null
                        if (url.startsWith(HtmlSanitizer.INLINE_SCHEME)) {
                            val cid = url.removePrefix(HtmlSanitizer.INLINE_SCHEME)
                            val resolved = inlineImage(cid) ?: return blocked("inline", url)
                            return serve(resolved)
                        }
                        if (url.startsWith("http://") || url.startsWith("https://")) {
                            val resolved = remoteImage(url) ?: return blocked("remote", url)
                            return serve(resolved)
                        }
                        // What is left is the document's own `data:` URL
                        // and the favicon it implies: nothing to serve,
                        // and nothing worth a line.
                        return refused()
                    }

                    private fun serve(resolved: Pair<String, ByteArray>) = WebResourceResponse(
                        resolved.first.substringBefore(';'),
                        null,
                        ByteArrayInputStream(resolved.second),
                    )

                    /**
                     * What a request the pane will not serve gets: an
                     * empty body, which the document paints as a broken
                     * image. The line states which resolver refused and
                     * names the request's origin rather than its URL, so
                     * a bug report says what happened without carrying a
                     * sender's tracking path (issue #440).
                     */
                    private fun blocked(reason: String, url: String): WebResourceResponse {
                        DiagLog.i(THREAD_TAG, "body image blocked ($reason) from ${originOf(url)}")
                        return refused()
                    }

                    private fun refused() =
                        WebResourceResponse("text/plain", "utf-8", ByteArrayInputStream(ByteArray(0)))
                }
                webChromeClient = object : WebChromeClient() {
                    /**
                     * A window the body asked for. The URL reaches the app
                     * only through the window it was promised, so a
                     * throwaway WebView takes the navigation, reports the
                     * URL and is discarded; nothing of it is ever shown.
                     */
                    override fun onCreateWindow(
                        view: WebView,
                        isDialog: Boolean,
                        isUserGesture: Boolean,
                        resultMsg: Message,
                    ): Boolean {
                        val relay = WebView(view.context)
                        relay.webViewClient = object : WebViewClient() {
                            override fun shouldOverrideUrlLoading(
                                inner: WebView?,
                                request: WebResourceRequest?,
                            ): Boolean {
                                link(request?.url?.toString().orEmpty())
                                inner?.post { inner.destroy() }
                                return true
                            }
                        }
                        (resultMsg.obj as WebView.WebViewTransport).webView = relay
                        resultMsg.sendToTarget()
                        return true
                    }
                }
            }
        },
        update = { webView ->
            rendering.set(true)
            webView.loadDataWithBaseURL(null, document.html, "text/html", "utf-8", null)
        },
    )
}

/**
 * One loading of the body document. The reading pane makes a new
 * instance for every document it wants on screen, including a reload of
 * the same markup with more images available to it (issue #440).
 */
private class BodyDocument(val html: String)

/**
 * One attachment: name, type and size, with a bounded thumbnail for an
 * image. The thumbnail comes from a sampled decode of the cached blob, so
 * a camera photo costs a few hundred kilobytes of bitmap rather than its
 * full resolution (issue #341, suite REQ-ATT-20/21). Tapping opens it.
 */
@Composable
private fun AttachmentRow(
    attachment: Attachment,
    loadBlob: suspend (Attachment) -> ByteArray?,
    onOpen: () -> Unit,
) {
    val isImage = ImageScaling.isImage(attachment.type)
    // Three states, because a part that cannot be produced must say so
    // rather than leave an empty chip (issue #420): still loading, the
    // thumbnail, or unavailable.
    val thumbnail by produceState<AttachmentPreview>(AttachmentPreview.Loading, attachment.blobId) {
        if (!isImage) {
            value = AttachmentPreview.None
            return@produceState
        }
        value = withContext(Dispatchers.IO) {
            val bitmap = loadBlob(attachment)?.let {
                runCatching { ImageScaling.thumbnail(it, THUMBNAIL_PX) }.getOrNull()
            }
            if (bitmap == null) AttachmentPreview.Unavailable else AttachmentPreview.Ready(bitmap)
        }
    }
    val unavailable = thumbnail is AttachmentPreview.Unavailable
    ListItem(
        leadingContent = {
            (thumbnail as? AttachmentPreview.Ready)?.let { ready ->
                Image(
                    bitmap = ready.bitmap,
                    contentDescription = attachment.name,
                    contentScale = ContentScale.Crop,
                    modifier = Modifier
                        .size(THUMBNAIL_DP.dp)
                        .testTag("attachment-thumbnail-${attachment.name}"),
                )
            }
        },
        headlineContent = { Text(attachment.name) },
        supportingContent = {
            val detail = "${attachment.type} - ${formatBytes(attachment.size)}"
            if (unavailable) {
                Text(
                    text = "$detail - unavailable",
                    modifier = Modifier.testTag("attachment-unavailable-${attachment.name}"),
                )
            } else {
                Text(detail)
            }
        },
        modifier = Modifier
            .clickable(enabled = isImage && !unavailable, onClick = onOpen)
            .testTag("attachment-${attachment.name}"),
    )
}

/** The full image, decoded at the screen's width rather than its own. */
@Composable
private fun AttachmentViewer(
    attachment: Attachment,
    maxEdgePx: Int,
    loadBlob: suspend (Attachment) -> ByteArray?,
    onDismiss: () -> Unit,
) {
    val image by produceState<AttachmentPreview>(AttachmentPreview.Loading, attachment.blobId) {
        value = withContext(Dispatchers.IO) {
            val bitmap = loadBlob(attachment)?.let {
                runCatching { ImageScaling.thumbnail(it, maxEdgePx) }.getOrNull()
            }
            if (bitmap == null) AttachmentPreview.Unavailable else AttachmentPreview.Ready(bitmap)
        }
    }
    Dialog(onDismissRequest = onDismiss) {
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .clickable(onClick = onDismiss)
                .testTag("attachment-viewer"),
            contentAlignment = Alignment.Center,
        ) {
            when (val state = image) {
                is AttachmentPreview.Ready -> Image(
                    bitmap = state.bitmap,
                    contentDescription = attachment.name,
                    contentScale = ContentScale.Fit,
                    modifier = Modifier.fillMaxWidth(),
                )

                AttachmentPreview.Unavailable, AttachmentPreview.None -> Text(
                    text = "This attachment could not be loaded.",
                    modifier = Modifier.padding(24.dp).testTag("attachment-viewer-unavailable"),
                )

                AttachmentPreview.Loading -> CircularProgressIndicator()
            }
        }
    }
}

private fun formatBytes(size: Long): String = when {
    size >= 1_000_000 -> "${(size / 100_000) / 10.0} MB"
    size >= 1_000 -> "${size / 1_000} kB"
    else -> "$size B"
}

/**
 * Where an attachment's picture stands: still being read, drawn, or not
 * to be had - a blob the store and the server both failed to produce,
 * or one that would not decode (issue #420).
 */
private sealed interface AttachmentPreview {
    data object Loading : AttachmentPreview

    /** Nothing to draw, because the part is not an image. */
    data object None : AttachmentPreview

    data class Ready(val bitmap: ImageBitmap) : AttachmentPreview

    data object Unavailable : AttachmentPreview
}

/** A chip's thumbnail: 56 dp on screen, decoded to a little more than that. */
private const val THUMBNAIL_DP = 56
private const val THUMBNAIL_PX = 256

/** The avatar circle's diameter, and the sizes the card's icons work in. */
private const val AVATAR_DP = 40
private const val ACTION_ICON_DP = 32
private const val ICON_DP = 20
private const val CHEVRON_DP = 18

/** What the reading pane's lines say in the diagnostic ring. */
private const val THREAD_TAG = "herold.thread"

/**
 * The scheme and host of a request the body made, which is as much of it
 * as a diagnostic line keeps: the rest of a remote image's URL is the
 * sender's own path and can identify the reader.
 */
private fun originOf(url: String): String {
    val scheme = url.substringBefore("://", missingDelimiterValue = "")
    if (scheme.isEmpty()) return url.substringBefore(':') + ":"
    return scheme + "://" + url.removePrefix("$scheme://").substringBefore('/')
}
