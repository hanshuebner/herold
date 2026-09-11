package com.netzhansa.herold.android.ui.thread

import android.annotation.SuppressLint
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Archive
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.outlined.StarBorder
import androidx.compose.material3.Card
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.common.SnoozeSheet
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.actions.ActionResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
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
    onBack: () -> Unit,
) {
    val messages by container.store.threadEmails(accountId, threadId).collectAsStateSafely(emptyList())
    val mailboxes by container.store.mailboxes().collectAsStateSafely(emptyList())
    val scope = rememberCoroutineScope()
    val snackbar = remember { SnackbarHostState() }
    var expandedId by remember { mutableStateOf<String?>(null) }
    var loadRemoteImages by remember { mutableStateOf(false) }
    var snoozing by remember { mutableStateOf(false) }
    val darkTheme = isSystemInDarkTheme()

    val newest = messages.lastOrNull()
    LaunchedEffect(newest?.id) {
        val target = messages.lastOrNull { it.isUnread } ?: newest
        if (target != null) {
            expandedId = target.id
            session.syncEngine.loadBody(accountId, target.id)
            if (target.isUnread) session.actions.setSeen(listOf(target), true)
        }
    }

    suspend fun report(result: ActionResult) {
        if (result is ActionResult.Reverted) snackbar.showSnackbar(result.message)
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
                        text = messages.firstOrNull { it.subject.isNotBlank() }?.subject ?: "(no subject)",
                        maxLines = 1,
                        modifier = Modifier.testTag("thread-title"),
                    )
                },
                actions = {
                    val flagged = messages.any { it.isFlagged }
                    IconButton(
                        onClick = { scope.launch { report(session.actions.setFlagged(messages, !flagged)) } },
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
                        onClick = {
                            scope.launch {
                                val (result, _) = session.actions.archive(messages, mailboxes)
                                if (result is ActionResult.Reverted) {
                                    snackbar.showSnackbar(result.message)
                                } else {
                                    onBack()
                                }
                            }
                        },
                        modifier = Modifier.testTag("thread-archive"),
                    ) {
                        Icon(Icons.Filled.Archive, contentDescription = "Archive")
                    }
                },
            )
        },
    ) { padding ->
        LazyColumn(
            modifier = Modifier.fillMaxSize().padding(padding).testTag("thread-messages"),
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
                                if (message.isUnread) report(session.actions.setSeen(listOf(message), true))
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
                                session.syncEngine.blob(accountId, it.blobId, it.type, it.name)
                                    ?.let { bytes -> it.type to bytes }
                            }
                        }
                    },
                )
                HorizontalDivider()
            }
        }
    }

    if (snoozing) {
        SnoozeSheet(
            onDismiss = { snoozing = false },
            onPick = { wakeAt ->
                snoozing = false
                scope.launch {
                    report(session.actions.snooze(messages, wakeAt))
                    onBack()
                }
            },
        )
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
                    ListItem(
                        headlineContent = { Text(attachment.name) },
                        supportingContent = { Text("${attachment.type} - ${attachment.size} bytes") },
                        modifier = Modifier.testTag("attachment-${attachment.name}"),
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
