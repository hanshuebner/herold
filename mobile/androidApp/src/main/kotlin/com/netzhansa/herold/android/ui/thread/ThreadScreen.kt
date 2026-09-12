package com.netzhansa.herold.android.ui.thread

import android.annotation.SuppressLint
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.foundation.Image
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Forward
import androidx.compose.material.icons.automirrored.filled.Reply
import androidx.compose.material.icons.automirrored.filled.ReplyAll
import androidx.compose.material.icons.filled.Archive
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.outlined.StarBorder
import androidx.compose.material3.AlertDialog
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
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.push.ActiveThread
import com.netzhansa.herold.android.push.MailNotifier
import com.netzhansa.herold.android.media.ImageScaling
import com.netzhansa.herold.android.ui.common.SnoozeSheet
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.actions.PendingAction
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.actions.UndoMessages
import com.netzhansa.herold.shared.compose.ComposeMode
import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.actions.FilterActions
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import com.netzhansa.herold.shared.mail.ListHeaders
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
    onBack: () -> Unit,
) {
    val messages by container.store.threadEmails(accountId, threadId).collectAsStateSafely(emptyList())
    val mailboxes by container.store.mailboxes().collectAsStateSafely(emptyList())
    val rules by container.store.managedRules().collectAsStateSafely(emptyList())
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

    // What an image is decoded for: the column it is drawn in, never its
    // own resolution (issue #341).
    val displayWidthPx = with(LocalDensity.current) {
        LocalConfiguration.current.screenWidthDp.dp.toPx().toInt()
    }

    /** The attachment's bytes from the blob cache, downloading them once. */
    suspend fun blobOf(attachment: Attachment): ByteArray? =
        session.syncEngine.blob(accountId, attachment.blobId, attachment.type, attachment.name)

    // A thread reached from search or a notification can be outside the
    // synced set; the store is still the source of truth, so the sync
    // engine fetches it into the store and the screen renders from there
    // (issue #339, REQ-AND-SYNC-01).
    LaunchedEffect(accountId, threadId) {
        if (container.store.threadEmailList(accountId, threadId).isNotEmpty()) return@LaunchedEffect
        fetching = true
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
    val snoozedUntil = messages.firstNotNullOfOrNull { it.snoozedUntil }

    /** The address a block and a seeded filter act on. */
    val newestSender = messages.lastOrNull()?.fromEmail.orEmpty()

    /**
     * The conversation's unsubscribe mechanism, from the newest message
     * that advertises one (REQ-UNS-11): the affordance belongs to the
     * thread even though the header is per message.
     */
    val offer = UnsubscribeOffer.of(messages)

    val newest = messages.lastOrNull()
    LaunchedEffect(newest?.id) {
        val target = messages.lastOrNull { it.isUnread } ?: newest
        if (target != null) {
            expandedId = target.id
            session.syncEngine.loadBody(accountId, target.id)
            if (target.isUnread) session.actions.setSeen(listOf(target), true)
        }
        Unit
    }

    /**
     * An action that takes the conversation off this screen: the local
     * write is already in the store, the offer is parked for the list, and
     * the view pops back so the undo appears where the user lands
     * (issue #345). Popping back happens on the main thread, which a
     * coroutine resumed off it must return to.
     */
    suspend fun leaveWith(action: PendingAction, message: String) {
        container.undo.offer(message, action, session.actions)
        withContext(Dispatchers.Main.immediate) { onBack() }
    }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbar, modifier = Modifier.testTag("thread-snackbar")) },
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("thread-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = {
                    Text(
                        text = when {
                            messages.isNotEmpty() ->
                                messages.firstOrNull { it.subject.isNotBlank() }?.subject ?: "(no subject)"

                            unavailable -> "Conversation"
                            else -> ""
                        },
                        maxLines = 1,
                        modifier = Modifier.testTag("thread-title"),
                    )
                },
                actions = {
                    val flagged = messages.any { it.isFlagged }
                    IconButton(
                        onClick = { scope.launch { session.actions.setFlagged(messages, !flagged) } },
                        modifier = Modifier.testTag("thread-star"),
                    ) {
                        Icon(
                            imageVector = if (flagged) Icons.Filled.Star else Icons.Outlined.StarBorder,
                            contentDescription = if (flagged) "Unstar" else "Star",
                        )
                    }
                    IconButton(onClick = { snoozing = true }, modifier = Modifier.testTag("thread-snooze")) {
                        Icon(Icons.Filled.Schedule, contentDescription = "Snooze")
                    }
                    IconButton(
                        onClick = { scope.launch { leaveWith(session.actions.archiveLocally(messages, mailboxes), UndoMessages.ARCHIVED) } },
                        modifier = Modifier.testTag("thread-archive"),
                    ) {
                        Icon(Icons.Filled.Archive, contentDescription = "Archive")
                    }
                    ThreadOverflow(
                        muted = rules.any { FilterActions.isThreadMuteRule(it, threadId) },
                        sender = newestSender,
                        onMute = { muted ->
                            scope.launch {
                                session.filters.setMuted(accountId, threadId, muted)
                                snackbar.showSnackbar(if (muted) "Conversation muted" else "Conversation unmuted")
                            }
                        },
                        onBlock = { blocking = newestSender },
                        onCreateFilter = {
                            val newest = messages.lastOrNull()
                            onCreateFilter(newest?.fromEmail.orEmpty(), newest?.subject.orEmpty())
                        },
                        onInspect = { inspecting = (messages.lastOrNull { it.id == expandedId } ?: messages.lastOrNull())?.id },
                    )
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding)) {
        snoozedUntil?.let { wakeAt ->
            SnoozedIndicator(
                wakeAt = wakeAt,
                onEdit = { snoozing = true },
                onCancel = { scope.launch { session.actions.unsnooze(messages) } },
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

                        is ListHeaders.Mechanism.Https -> openInBrowser(context, mechanism.url)

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
        ReplyBar(
            enabled = messages.isNotEmpty(),
            onReply = { messages.lastOrNull()?.let { onCompose(ComposeMode.REPLY, it.id) } },
            onReplyAll = { messages.lastOrNull()?.let { onCompose(ComposeMode.REPLY_ALL, it.id) } },
            onForward = { messages.lastOrNull()?.let { onCompose(ComposeMode.FORWARD, it.id) } },
        )
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
            items(messages, key = { it.id }) { message ->
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
                    resolveInlineImage = { cid ->
                        val attachment = message.attachments.firstOrNull {
                            it.cid?.trim('<', '>') == cid || it.name == cid
                        }
                        attachment?.let {
                            runBlocking(Dispatchers.IO) {
                                blobOf(it)?.let { bytes ->
                                    // Inline images keep their place in the
                                    // body and are handed to the WebView at
                                    // the width it will draw them at.
                                    it.type to ImageScaling.forDisplay(bytes, displayWidthPx)
                                }
                            }
                        }
                    },
                    loadBlob = { attachment -> blobOf(attachment) },
                    onOpenAttachment = { attachment -> viewing = attachment },
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
 * The conversation's overflow: the organise actions that are not worth a
 * toolbar slot - mute, block, a filter seeded from the message, and what
 * the classifier made of it (suite REQ-MAIL-136/138, G7).
 */
@Composable
private fun ThreadOverflow(
    muted: Boolean,
    sender: String,
    onMute: (Boolean) -> Unit,
    onBlock: () -> Unit,
    onCreateFilter: () -> Unit,
    onInspect: () -> Unit,
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
            text = { Text("Block $sender") },
            onClick = {
                open = false
                onBlock()
            },
            enabled = sender.isNotBlank(),
            modifier = Modifier.testTag("thread-block"),
        )
        DropdownMenuItem(
            text = { Text("Create filter from this message") },
            onClick = {
                open = false
                onCreateFilter()
            },
            modifier = Modifier.testTag("thread-create-filter"),
        )
        DropdownMenuItem(
            text = { Text("Why is this here?") },
            onClick = {
                open = false
                onInspect()
            },
            modifier = Modifier.testTag("thread-why"),
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

/** Hands an unsubscribe URL to the browser; the phone never renders it. */
private fun openInBrowser(context: android.content.Context, url: String) {
    runCatching {
        context.startActivity(
            android.content.Intent(android.content.Intent.ACTION_VIEW, android.net.Uri.parse(url))
                .addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK),
        )
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
 * Reply, reply-all and forward for the conversation, acting on its newest
 * message - the one a reply answers (suite REQ-MAIL-30).
 */
@Composable
private fun ReplyBar(
    enabled: Boolean,
    onReply: () -> Unit,
    onReplyAll: () -> Unit,
    onForward: () -> Unit,
) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 6.dp).testTag("thread-reply-bar"),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        TextButton(onClick = onReply, enabled = enabled, modifier = Modifier.testTag("thread-reply")) {
            Icon(Icons.AutoMirrored.Filled.Reply, contentDescription = null)
            Text("Reply")
        }
        TextButton(onClick = onReplyAll, enabled = enabled, modifier = Modifier.testTag("thread-reply-all")) {
            Icon(Icons.AutoMirrored.Filled.ReplyAll, contentDescription = null)
            Text("Reply all")
        }
        TextButton(onClick = onForward, enabled = enabled, modifier = Modifier.testTag("thread-forward")) {
            Icon(Icons.AutoMirrored.Filled.Forward, contentDescription = null)
            Text("Forward")
        }
    }
}

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
) {
    Column(modifier = Modifier.fillMaxWidth().testTag("message-${message.id}")) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .clickable(onClick = onToggle)
                .padding(12.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = message.senderDisplay.ifBlank { message.fromEmail },
                    style = MaterialTheme.typography.titleSmall,
                    fontWeight = if (message.isUnread) FontWeight.Bold else FontWeight.Normal,
                )
                Text(
                    text = "to ${message.toLine}",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                if (!expanded) {
                    Text(
                        text = message.preview,
                        style = MaterialTheme.typography.bodySmall,
                        maxLines = 1,
                    )
                }
            }
        }

        if (expanded) {
            val body = message.bodyHtml?.let { HtmlSanitizer.sanitize(it, loadRemoteImages) }
                ?: message.bodyText?.let { HtmlSanitizer.sanitize(HtmlSanitizer.fromPlainText(it), loadRemoteImages) }

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
                        TextButton(onClick = onShowRemoteImages, modifier = Modifier.testTag("show-images-${message.id}")) {
                            Text("Show images")
                        }
                    }
                }
                MessageBodyWebView(
                    html = HtmlSanitizer.document(body.html, darkTheme),
                    resolveInlineImage = resolveInlineImage,
                    resolveRemoteImage = if (loadRemoteImages) resolveRemoteImage else { _ -> null },
                    modifier = Modifier.fillMaxWidth().testTag("message-body-${message.id}"),
                )
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
 * The body surface: a WebView with JavaScript, file access and content
 * access off. Inline images are served from the local blob cache by
 * intercepting the scheme the sanitiser rewrote `cid:` onto; every other
 * network load is refused, so opening a message makes no request the user
 * did not ask for.
 */
@SuppressLint("SetJavaScriptEnabled")
@Composable
private fun MessageBodyWebView(
    html: String,
    resolveInlineImage: (String) -> Pair<String, ByteArray>?,
    resolveRemoteImage: (String) -> Pair<String, ByteArray>?,
    modifier: Modifier = Modifier,
) {
    AndroidView(
        modifier = modifier,
        factory = { context ->
            WebView(context).apply {
                settings.javaScriptEnabled = false
                settings.allowFileAccess = false
                settings.allowContentAccess = false
                settings.loadsImagesAutomatically = true
                isVerticalScrollBarEnabled = false
                webViewClient = object : WebViewClient() {
                    override fun shouldInterceptRequest(
                        view: WebView?,
                        request: WebResourceRequest?,
                    ): WebResourceResponse? {
                        val url = request?.url?.toString() ?: return null
                        if (url.startsWith(HtmlSanitizer.INLINE_SCHEME)) {
                            val cid = url.removePrefix(HtmlSanitizer.INLINE_SCHEME)
                            val resolved = resolveInlineImage(cid) ?: return blocked()
                            return serve(resolved)
                        }
                        if (url.startsWith("http://") || url.startsWith("https://")) {
                            val resolved = resolveRemoteImage(url) ?: return blocked()
                            return serve(resolved)
                        }
                        return blocked()
                    }

                    private fun serve(resolved: Pair<String, ByteArray>) = WebResourceResponse(
                        resolved.first.substringBefore(';'),
                        null,
                        ByteArrayInputStream(resolved.second),
                    )

                    private fun blocked() =
                        WebResourceResponse("text/plain", "utf-8", ByteArrayInputStream(ByteArray(0)))
                }
            }
        },
        update = { webView ->
            webView.loadDataWithBaseURL(null, html, "text/html", "utf-8", null)
        },
    )
}

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
    val thumbnail by produceState<ImageBitmap?>(null, attachment.blobId) {
        if (!isImage) return@produceState
        value = withContext(Dispatchers.IO) {
            loadBlob(attachment)?.let { ImageScaling.thumbnail(it, THUMBNAIL_PX) }
        }
    }
    ListItem(
        leadingContent = {
            thumbnail?.let { bitmap ->
                Image(
                    bitmap = bitmap,
                    contentDescription = attachment.name,
                    contentScale = ContentScale.Crop,
                    modifier = Modifier
                        .size(THUMBNAIL_DP.dp)
                        .testTag("attachment-thumbnail-${attachment.name}"),
                )
            }
        },
        headlineContent = { Text(attachment.name) },
        supportingContent = { Text("${attachment.type} - ${formatBytes(attachment.size)}") },
        modifier = Modifier
            .clickable(enabled = isImage, onClick = onOpen)
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
    val image by produceState<ImageBitmap?>(null, attachment.blobId) {
        value = withContext(Dispatchers.IO) {
            loadBlob(attachment)?.let { ImageScaling.thumbnail(it, maxEdgePx) }
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
            image?.let { bitmap ->
                Image(
                    bitmap = bitmap,
                    contentDescription = attachment.name,
                    contentScale = ContentScale.Fit,
                    modifier = Modifier.fillMaxWidth(),
                )
            } ?: CircularProgressIndicator()
        }
    }
}

private fun formatBytes(size: Long): String = when {
    size >= 1_000_000 -> "${(size / 100_000) / 10.0} MB"
    size >= 1_000 -> "${size / 1_000} kB"
    else -> "$size B"
}

/** A chip's thumbnail: 56 dp on screen, decoded to a little more than that. */
private const val THUMBNAIL_DP = 56
private const val THUMBNAIL_PX = 256
