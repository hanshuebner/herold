package com.netzhansa.herold.android.ui.compose

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import android.webkit.MimeTypeMap
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.FormatListBulleted
import androidx.compose.material.icons.filled.AttachFile
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.FormatBold
import androidx.compose.material.icons.filled.FormatItalic
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Send
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.AssistChip
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.InputChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
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
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.ComposeHandoff
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.media.ImageScaling
import com.netzhansa.herold.android.media.ImageSize
import com.netzhansa.herold.android.media.ImageSizePreference
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.compose.AttachResult
import com.netzhansa.herold.shared.compose.AttachmentStatus
import com.netzhansa.herold.shared.compose.ComposeAttachment
import com.netzhansa.herold.shared.compose.ComposeBaseline
import com.netzhansa.herold.shared.compose.ComposeMode
import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.compose.ComposeState
import com.netzhansa.herold.shared.compose.DraftHandle
import com.netzhansa.herold.shared.compose.HtmlText
import com.netzhansa.herold.shared.compose.InlineImage
import com.netzhansa.herold.shared.compose.IdentityChoice
import com.netzhansa.herold.shared.compose.RecipientParser
import com.netzhansa.herold.shared.compose.changedSince
import com.netzhansa.herold.shared.compose.withPrefill
import com.netzhansa.herold.android.ui.settings.UndoSendPreference
import com.netzhansa.herold.shared.actions.UndoActions
import com.netzhansa.herold.shared.actions.UndoMessages
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.outbox.ComposePayload
import com.netzhansa.herold.shared.outbox.toComposeState
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.first
import kotlinx.datetime.Instant
import kotlinx.datetime.TimeZone
import kotlinx.datetime.toLocalDateTime
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * The composer: new message, reply, reply-all and forward (issue #329,
 * suite `02-mail-basics.md` and `17-attachments.md`).
 *
 * The compose's whole state is the shared core's [ComposeState], so the
 * recipient derivation, the quoting, the draft's wire shape and the send
 * are the same logic the host-JVM tests cover; this file is the surface
 * that edits it.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ComposeScreen(
    container: AppContainer,
    session: SessionScope,
    mode: ComposeMode,
    accountId: String?,
    parentEmailId: String?,
    accountScope: String?,
    onClose: () -> Unit,
    /** A send taken back within its undo window, reopened as it was (issue #354). */
    resume: ComposePayload? = null,
    /**
     * What a share, a mailto: link, an unsubscribe or a shortcut opens
     * the composer on (REQ-AND-SYS-01/03, REQ-UNS-22).
     */
    handoff: ComposeHandoff? = null,
) {
    val identities by container.store.identities().collectAsStateSafely(emptyList())
    val accounts by container.store.accounts().collectAsStateSafely(emptyList())
    val mailboxes by container.store.mailboxes().collectAsStateSafely(emptyList())
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val snackbar = remember { SnackbarHostState() }
    val darkTheme = isSystemInDarkTheme()
    val editor = remember { EditorHandle() }
    /**
     * What this compose's draft is known by, which is not an id until
     * a save has answered: the discard offer holds this rather than an
     * id read when the offer was parked (issue #371).
     */
    val draft = remember { DraftHandle() }

    var state by remember { mutableStateOf<ComposeState?>(null) }
    /**
     * The composer as it was seeded. A reply, a forward and a reopened
     * draft all open with something in them, so what a close reads is
     * the difference from this, not whether the fields are blank
     * (issue #371).
     */
    var baseline by remember { mutableStateOf(ComposeBaseline.EMPTY) }
    var toText by remember { mutableStateOf("") }
    var ccText by remember { mutableStateOf("") }
    var sending by remember { mutableStateOf(false) }
    /** True once the message is the outbox's; leaving must not save it again. */
    var handedOff by remember { mutableStateOf(false) }
    /**
     * True from the moment a way out is taken, so a second tap on the
     * close control - or back over a save still on the wire - does not
     * start a second draft (issue #371).
     */
    var leaving by remember { mutableStateOf(false) }
    var linkDialog by remember { mutableStateOf(false) }
    /** A picked image waiting for its size choice (issue #341). */
    var sizeChoice by remember { mutableStateOf<PendingImage?>(null) }
    var suggestions by remember { mutableStateOf<List<MailAddress>>(emptyList()) }
    // The editor's document loads asynchronously; it publishes its body
    // once it is up, which is also what tells a caller it can be typed in.
    var editorReady by remember { mutableStateOf(false) }
    /** True while the editable holds the caret, so a keystroke reaches it. */
    var editorFocused by remember { mutableStateOf(false) }

    // The compose opens once its inputs have arrived from the local store.
    LaunchedEffect(identities, accounts, parentEmailId, mode) {
        if (state != null) return@LaunchedEffect
        if (resume != null) {
            state = resume.toComposeState()
            // A send handed back inside its undo window is the reader's
            // own message: all of it counts as theirs to keep.
            baseline = ComposeBaseline.EMPTY
            // Taken: a later recomposition must not reopen it again.
            container.composeResume.value = null
            return@LaunchedEffect
        }
        if (identities.isEmpty()) return@LaunchedEffect
        // The quote needs the parent's body, which a message opened from
        // the shade's Reply action has not been read with yet; loadBody
        // returns the cached copy when there is one (issue #348).
        val parent = if (parentEmailId != null && accountId != null) {
            session.syncEngine.loadBody(accountId, parentEmailId)
                ?: container.store.email(accountId, parentEmailId)
        } else {
            null
        }
        val opened = when {
            parent != null && mode == ComposeMode.EDIT_DRAFT ->
                session.composer.openDraft(parent, identities, accounts)

            parent != null && mode != ComposeMode.NEW ->
                session.composer.openFrom(mode, parent, identities, accounts, formatQuoteDate(parent.receivedAt))

            else -> session.composer.openNew(identities, accounts, accountScope ?: accountId)
        }
        // What a share, a mailto: or an unsubscribe handed over is the
        // user's, so it counts as a change against what the composer
        // would have opened with on its own.
        baseline = ComposeBaseline.of(opened)
        state = (handoff?.let { opened.withPrefill(it.prefill) } ?: opened).withInlineBytes(session)
        if (handoff != null) {
            // Taken: a recomposition must not fold the same handoff in twice.
            container.composeHandoff.value = null
        }
    }


    // Navigation runs on the main thread; a coroutine that resumed off it
    // after a network call must hop back before popping the back stack.
    suspend fun close() = withContext(Dispatchers.Main.immediate) { onClose() }

    val current = state
    if (current == null) {
        Scaffold { padding ->
            Column(modifier = Modifier.fillMaxSize().padding(padding).testTag("compose-loading")) {
                CircularProgressIndicator(modifier = Modifier.padding(24.dp))
            }
        }
        return
    }

    fun commitRecipients(): ComposeState {
        var next: ComposeState = current
        RecipientParser.parse(toText).takeIf { it.isNotEmpty() }?.let { next = next.copy(to = next.to + it) }
        RecipientParser.parse(ccText).takeIf { it.isNotEmpty() }?.let { next = next.copy(cc = next.cc + it) }
        toText = ""
        ccText = ""
        state = next
        return next
    }

    /** The compose with the editor's last published body folded in. */
    fun withBody(target: ComposeState): ComposeState =
        editor.latestHtml?.let { target.copy(bodyHtml = it) } ?: target

    /**
     * Takes a just-written draft into the local store, which is what every
     * screen renders from: the conversation shows the draft at the end of
     * it without waiting for the next full sync (issue #371).
     */
    suspend fun takeDraftIntoStore(accountId: String, draftId: String) {
        runCatching { session.syncEngine.loadBody(accountId, draftId) }
    }

    suspend fun saveDraft(target: ComposeState) {
        when (val result = session.composer.saveDraft(withBody(target), mailboxes)) {
            is ComposeResult.Saved -> {
                state = (state ?: target).copy(draftId = result.draftId)
                takeDraftIntoStore(target.accountId, result.draftId)
                session.drafts.saved(draft, target.accountId, result.draftId)
            }
            // With no connection the draft is in the outbox; it reaches
            // the server's Drafts mailbox on the next drain.
            is ComposeResult.Queued -> {
                state = (state ?: target).copy(draftEntryId = result.entryId)
                session.drafts.queued(draft, target.accountId, result.entryId)
            }

            is ComposeResult.Failed -> snackbar.showSnackbar(result.message)
        }
    }

    // Backgrounding the app saves what has been typed (suite REQ-DFT-01/02).
    // A composer the reader has not changed has nothing to keep, and a
    // message already queued for sending is not a draft: saving either
    // here would leave a message nobody wrote.
    LifecycleEventEffect(Lifecycle.Event.ON_STOP) {
        val target = state
        if (!handedOff && target != null && target.changedSince(baseline)) {
            scope.launch { saveDraft(target) }
        }
    }

    /** Adds a file that is ready to go up, inline or as an attachment. */
    suspend fun upload(file: PickedFile, inline: Boolean) {
        when (val result = session.composer.attach(current.accountId, file.name, file.type, file.bytes, inline)) {
            is AttachResult.Added -> {
                val added = result.attachment
                state = state?.let { it.copy(attachments = it.attachments + added) }
                val bytes = added.bytes
                if (inline && added.cid != null && bytes != null) {
                    // The tag describes the box the image is drawn in, not
                    // the pixels it is encoded at (issue #367).
                    val box = ImageScaling.dimensions(bytes)?.let { (w, h) -> InlineImage.fit(w, h) }
                    editor.insertInlineImage(added.cid!!, added.type, bytes, box)
                }
            }

            is AttachResult.Rejected -> snackbar.showSnackbar(result.message)
        }
    }

    /**
     * A picked file goes up as it is, unless it is an image big enough to
     * be a camera photo - then the size choice comes first (issue #341).
     */
    suspend fun offerOrUpload(uri: Uri, inline: Boolean) {
        val file = withContext(Dispatchers.IO) { readFile(context, uri) } ?: run {
            snackbar.showSnackbar("That file could not be read")
            return
        }
        if (ImageScaling.isImage(file.type) && file.bytes.size >= ImageScaling.OFFER_THRESHOLD_BYTES) {
            sizeChoice = PendingImage(file, inline)
            return
        }
        upload(file, inline)
    }

    // The shared files go up the composer's own attachment path, one at a
    // time: a file waits for the image size choice the file before it
    // raised, so one dialog is answered before the next appears
    // (REQ-AND-SYS-01, issue #341).
    LaunchedEffect(Unit) {
        handoff?.attachments.orEmpty()?.forEach { uri ->
            snapshotFlow { sizeChoice }.first { it == null }
            offerOrUpload(Uri.parse(uri), inline = false)
        }
    }

    /**
     * Leaves the composer, which is what the close control and back
     * both do (REQ-AND-NAV-12, issue #371).
     *
     * A composer the reader has not changed since it opened closes
     * silently and leaves nothing behind - a reply opens holding the
     * address it answers, a `Re:` subject and the quoted original, none
     * of which the reader wrote. One they did change is kept in the
     * server's Drafts mailbox, through the outbox with no connection,
     * and the snackbar on the screen behind offers to throw it away. A
     * draft that could not be kept leaves the composer open rather than
     * losing what was typed.
     */
    fun leave() {
        if (leaving) return
        leaving = true
        val target = commitRecipients()
        scope.launch {
            editor.publish()
            val toSave = withBody(state ?: target)
            if (!toSave.changedSince(baseline)) {
                close()
                return@launch
            }
            when (val result = session.composer.saveDraft(toSave, mailboxes)) {
                is ComposeResult.Saved -> {
                    handedOff = true
                    takeDraftIntoStore(toSave.accountId, result.draftId)
                    session.drafts.saved(draft, toSave.accountId, result.draftId)
                    offerDiscardDraft(container, session, draft)
                    close()
                }

                is ComposeResult.Queued -> {
                    handedOff = true
                    session.drafts.queued(draft, toSave.accountId, result.entryId)
                    offerDiscardDraft(container, session, draft)
                    close()
                }

                is ComposeResult.Failed -> {
                    leaving = false
                    snackbar.showSnackbar(result.message)
                }
            }
        }
    }

    // Back leaves the composer the same way its close control does, so
    // the draft rule does not depend on which way out the reader takes.
    BackHandler { leave() }

    val attachLauncher = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult
        scope.launch { offerOrUpload(uri, inline = false) }
    }
    val inlineLauncher = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult
        scope.launch { offerOrUpload(uri, inline = true) }
    }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbar, modifier = Modifier.testTag("compose-snackbar")) },
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(
                        onClick = { leave() },
                        modifier = Modifier.testTag("compose-close"),
                    ) {
                        Icon(Icons.Filled.Close, contentDescription = "Close")
                    }
                },
                title = { Text(titleFor(mode), modifier = Modifier.testTag("compose-title")) },
                actions = {
                    IconButton(
                        onClick = { attachLauncher.launch(arrayOf("*/*")) },
                        modifier = Modifier.testTag("compose-attach"),
                    ) {
                        Icon(Icons.Filled.AttachFile, contentDescription = "Attach a file")
                    }
                    IconButton(
                        enabled = !sending,
                        onClick = {
                            val target = commitRecipients()
                            sending = true
                            scope.launch {
                                editor.publish()
                                val toSend = withBody(state ?: target)
                                val hold = UndoSendPreference.current(context).millis
                                when (val result = session.composer.send(toSend, mailboxes, hold)) {
                                    is ComposeResult.Queued -> {
                                        // The message is durable now: the
                                        // drain carries it, with or
                                        // without a connection. The hold
                                        // is the window the list offers
                                        // the undo in (issue #354).
                                        handedOff = true
                                        offerUndoSend(container, result, hold)
                                        session.requestDrain(hold)
                                        close()
                                    }

                                    is ComposeResult.Failed -> {
                                        sending = false
                                        snackbar.showSnackbar(result.message)
                                    }

                                    else -> sending = false
                                }
                            }
                        },
                        modifier = Modifier.testTag("compose-send"),
                    ) {
                        Icon(Icons.Filled.Send, contentDescription = "Send")
                    }
                },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                // The body scrolls inside what the keyboard leaves, so the
                // field being typed into stays in view (issue #373).
                .imePadding()
                .verticalScroll(rememberScrollState())
                .testTag("compose-screen"),
        ) {
            FromRow(
                state = current,
                options = IdentityChoice.options(identities, accounts, accountScope),
                onPick = { option ->
                    state = current.copy(
                        identity = option.identity,
                        accountId = option.identity.accountId,
                        // A draft belongs to the account it was written on;
                        // moving accounts starts a new one.
                        draftId = if (option.identity.accountId == current.accountId) current.draftId else null,
                    )
                },
            )
            HorizontalDivider()

            RecipientField(
                label = "To",
                tag = "compose-to",
                chips = current.to,
                text = toText,
                suggestions = suggestions,
                onTextChange = { value ->
                    toText = value
                    scope.launch {
                        suggestions = session.addressBook.suggestions(current.accountId, value).map { it.address }
                    }
                },
                onCommit = { address ->
                    state = current.copy(to = current.to + address)
                    toText = ""
                    suggestions = emptyList()
                },
                onRemove = { address -> state = current.copy(to = current.to - address) },
            )
            if (current.showCc) {
                RecipientField(
                    label = "Cc",
                    tag = "compose-cc",
                    chips = current.cc,
                    text = ccText,
                    suggestions = emptyList(),
                    onTextChange = { ccText = it },
                    onCommit = { address ->
                        state = current.copy(cc = current.cc + address)
                        ccText = ""
                    },
                    onRemove = { address -> state = current.copy(cc = current.cc - address) },
                )
            } else {
                TextButton(
                    onClick = { state = current.copy(showCc = true) },
                    modifier = Modifier.testTag("compose-show-cc"),
                ) {
                    Text("Add Cc")
                }
            }
            HorizontalDivider()

            OutlinedTextField(
                value = current.subject,
                onValueChange = { state = current.copy(subject = it) },
                label = { Text("Subject") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Next, keyboardType = KeyboardType.Text),
                modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp)
                    .testTag("compose-subject"),
            )

            FormattingToolbar(
                inlineImages = current.attachments.count { it.inline },
                onCommand = { editor.run(it) },
                onLink = { linkDialog = true },
                onInsertImage = { inlineLauncher.launch(arrayOf("image/*")) },
            )

            RichTextEditor(
                initialHtml = current.bodyHtml,
                darkTheme = darkTheme,
                attachments = current.attachments,
                handle = editor,
                onHtmlChanged = { html ->
                    // The document the editor publishes as it loads is
                    // the body the composer was seeded with, in the
                    // editor's own markup: it is what a later edit is
                    // measured against, not an edit itself.
                    if (!editorReady) baseline = baseline.withBody(html)
                    state = state?.copy(bodyHtml = html)
                    editorReady = true
                },
                onFocusChanged = { editorFocused = it },
                modifier = Modifier.fillMaxWidth().heightIn(min = 220.dp).testTag("compose-body"),
            )

            if (editorReady) {
                // The editor lives in a WebView, whose content is outside
                // the Compose tree; its length is what tells a caller the
                // document is up and has taken an edit.
                val bodyChars = HtmlText.toPlainText(current.bodyHtml).length
                Spacer(modifier = Modifier.testTag("compose-editor-$bodyChars"))
            }

            if (editorFocused) {
                // Typing lands in the document from the moment the
                // editable takes the caret, which the page reports.
                Spacer(modifier = Modifier.testTag("compose-body-focused"))
            }

            AttachmentStrip(
                attachments = current.attachments,
                onRemove = { attachment ->
                    state = current.copy(attachments = current.attachments - attachment)
                },
            )
        }
    }

    sizeChoice?.let { pending ->
        ImageSizeDialog(
            file = pending.file,
            initial = ImageSizePreference.last(context),
            onDismiss = { sizeChoice = null },
            onPick = { size ->
                sizeChoice = null
                ImageSizePreference.remember(context, size)
                scope.launch {
                    val scaled = withContext(Dispatchers.Default) {
                        ImageScaling.scale(pending.file.name, pending.file.type, pending.file.bytes, size)
                    }
                    upload(PickedFile(scaled.name, scaled.type, scaled.bytes), pending.inline)
                }
            },
        )
    }

    if (linkDialog) {
        LinkDialog(
            onDismiss = { linkDialog = false },
            onConfirm = { url ->
                linkDialog = false
                editor.link(url)
            },
        )
    }
}

@Composable
private fun FromRow(
    state: ComposeState,
    options: List<com.netzhansa.herold.shared.compose.FromOption>,
    onPick: (com.netzhansa.herold.shared.compose.FromOption) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable { open = true }
            .padding(horizontal = 16.dp, vertical = 12.dp)
            .testTag("compose-from"),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text("From", style = MaterialTheme.typography.labelMedium)
        Text(
            text = state.identity?.email ?: "(no identity)",
            style = MaterialTheme.typography.bodyMedium,
            modifier = Modifier.testTag("compose-from-value"),
        )
    }
    DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
        options.forEach { option ->
            DropdownMenuItem(
                text = {
                    Column {
                        Text(option.identity.email)
                        Text(option.accountName, style = MaterialTheme.typography.labelSmall)
                    }
                },
                onClick = {
                    open = false
                    onPick(option)
                },
                modifier = Modifier.testTag("from-option-${option.identity.accountId}-${option.identity.id}"),
            )
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun RecipientField(
    label: String,
    tag: String,
    chips: List<MailAddress>,
    text: String,
    suggestions: List<MailAddress>,
    onTextChange: (String) -> Unit,
    onCommit: (MailAddress) -> Unit,
    onRemove: (MailAddress) -> Unit,
) {
    Column(modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp)) {
        if (chips.isNotEmpty()) {
            Row(horizontalArrangement = Arrangement.spacedBy(4.dp), modifier = Modifier.fillMaxWidth()) {
                chips.forEach { address ->
                    InputChip(
                        selected = false,
                        onClick = { onRemove(address) },
                        label = { Text(address.display) },
                        modifier = Modifier.testTag("$tag-chip-${address.email}"),
                    )
                }
            }
        }
        OutlinedTextField(
            value = text,
            onValueChange = { value ->
                // A separator commits the address, the way the suite's
                // recipient field does.
                if (value.endsWith(",") || value.endsWith(";") || value.endsWith("\n")) {
                    RecipientParser.parseOne(value.dropLast(1))?.let(onCommit)
                } else {
                    onTextChange(value)
                }
            },
            label = { Text(label) },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Email, imeAction = ImeAction.Done),
            modifier = Modifier.fillMaxWidth().testTag(tag),
        )
        suggestions.take(4).forEach { suggestion ->
            Text(
                text = suggestion.format(),
                style = MaterialTheme.typography.bodySmall,
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { onCommit(suggestion) }
                    .padding(horizontal = 12.dp, vertical = 10.dp)
                    .testTag("suggestion-${suggestion.email}"),
            )
        }
    }
}

@Composable
private fun FormattingToolbar(
    inlineImages: Int,
    onCommand: (EditorCommand) -> Unit,
    onLink: () -> Unit,
    onInsertImage: () -> Unit,
) {
    Row(
        // The inline-image count rides on the toolbar's tag: an inline
        // image lives in the body, not in the attachment strip (suite G8),
        // so this is the only place the count is observable.
        modifier = Modifier.fillMaxWidth().padding(horizontal = 8.dp).testTag("compose-toolbar-$inlineImages"),
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        IconButton(onClick = { onCommand(EditorCommand.BOLD) }, modifier = Modifier.testTag("compose-bold")) {
            Icon(Icons.Filled.FormatBold, contentDescription = "Bold")
        }
        IconButton(onClick = { onCommand(EditorCommand.ITALIC) }, modifier = Modifier.testTag("compose-italic")) {
            Icon(Icons.Filled.FormatItalic, contentDescription = "Italic")
        }
        IconButton(
            onClick = { onCommand(EditorCommand.BULLET_LIST) },
            modifier = Modifier.testTag("compose-list"),
        ) {
            Icon(Icons.AutoMirrored.Filled.FormatListBulleted, contentDescription = "Bulleted list")
        }
        IconButton(onClick = onLink, modifier = Modifier.testTag("compose-link")) {
            Icon(Icons.Filled.Link, contentDescription = "Link")
        }
        IconButton(onClick = onInsertImage, modifier = Modifier.testTag("compose-insert-image")) {
            Icon(Icons.Filled.Image, contentDescription = "Insert an image")
        }
    }
}

/**
 * Parks the "Draft saved / Discard" offer for the screen the composer
 * returned to (issue #371).
 *
 * The offer carries the compose's draft handle rather than the ids read
 * when it was parked, so taking it acts on whatever the save has reached
 * by then - the queue entry, the message on the server, or a save still
 * in flight, whose draft is destroyed as soon as it has one.
 */
private fun offerDiscardDraft(
    container: AppContainer,
    session: SessionScope,
    draft: DraftHandle,
) {
    container.undo.offer(
        message = UndoMessages.DRAFT_SAVED,
        windowMs = null,
        actionLabel = UndoActions.DISCARD,
    ) {
        session.drafts.discard(draft)
    }
}

/**
 * Parks the "Sending" offer the list shows for the length of the hold.
 * Taking it drops the queued entry and hands the compose back, with its
 * recipients, body and attachments as they were (issue #354).
 */
private fun offerUndoSend(container: AppContainer, queued: ComposeResult.Queued, holdMs: Long) {
    if (holdMs <= 0) return
    container.undo.offer(UndoMessages.SENDING, windowMs = holdMs) {
        container.composeResume.value = container.outbox.cancelCompose(queued.entryId)
    }
}

@Composable
private fun AttachmentStrip(
    attachments: List<ComposeAttachment>,
    onRemove: (ComposeAttachment) -> Unit,
) {
    val files = attachments.filter { !it.inline }
    if (files.isEmpty()) return
    Column(modifier = Modifier.fillMaxWidth().padding(8.dp).testTag("compose-attachments")) {
        files.forEach { attachment ->
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                AssistChip(
                    onClick = { onRemove(attachment) },
                    label = { Text("${attachment.name} (${attachment.size} bytes)") },
                    modifier = Modifier.testTag("attachment-${attachment.name}"),
                )
                when (attachment.status) {
                    AttachmentStatus.UPLOADING -> CircularProgressIndicator(
                        modifier = Modifier.size(16.dp).testTag("attachment-progress-${attachment.name}"),
                    )

                    AttachmentStatus.FAILED -> Text(
                        text = attachment.error ?: "Upload failed",
                        color = MaterialTheme.colorScheme.error,
                        style = MaterialTheme.typography.labelSmall,
                        modifier = Modifier.testTag("attachment-failed-${attachment.name}"),
                    )

                    AttachmentStatus.PENDING -> Text(
                        text = "Waits for a connection",
                        style = MaterialTheme.typography.labelSmall,
                        modifier = Modifier.testTag("attachment-pending-${attachment.name}"),
                    )

                    AttachmentStatus.READY -> Text(
                        text = "Ready",
                        style = MaterialTheme.typography.labelSmall,
                        modifier = Modifier.testTag("attachment-ready-${attachment.name}"),
                    )
                }
            }
        }
    }
}

@Composable
private fun LinkDialog(onDismiss: () -> Unit, onConfirm: (String) -> Unit) {
    var url by remember { mutableStateOf("https://") }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Link") },
        text = {
            OutlinedTextField(
                value = url,
                onValueChange = { url = it },
                singleLine = true,
                modifier = Modifier.fillMaxWidth().testTag("link-url"),
            )
        },
        confirmButton = {
            TextButton(onClick = { onConfirm(url) }, modifier = Modifier.testTag("link-confirm")) {
                Text("Add")
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
        modifier = Modifier.testTag("link-dialog"),
    )
}

/** A file the picker returned, read into memory. */
private class PickedFile(val name: String, val type: String, val bytes: ByteArray)

/** A picked image held while the user chooses what size to send it at. */
private class PendingImage(val file: PickedFile, val inline: Boolean)

private fun readFile(context: Context, uri: Uri): PickedFile? {
    val resolver = context.contentResolver
    var name = uri.lastPathSegment?.substringAfterLast('/') ?: "attachment"
    resolver.query(uri, null, null, null, null)?.use { cursor ->
        val index = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
        if (index >= 0 && cursor.moveToFirst()) name = cursor.getString(index) ?: name
    }
    val bytes = resolver.openInputStream(uri)?.use { it.readBytes() } ?: return null
    return PickedFile(name, typeOf(resolver, uri, name), bytes)
}

/**
 * The file's media type. A `content:` provider reports one; anything else
 * (a `file:` URI from a share, for instance) is typed from its name, so a
 * photo is recognised as a photo rather than going out as octet-stream.
 */
private fun typeOf(resolver: android.content.ContentResolver, uri: Uri, name: String): String {
    resolver.getType(uri)?.takeIf { it.isNotBlank() && it != FALLBACK_TYPE }?.let { return it }
    val extension = name.substringAfterLast('.', "").lowercase()
    return MimeTypeMap.getSingleton().getMimeTypeFromExtension(extension) ?: FALLBACK_TYPE
}

private const val FALLBACK_TYPE = "application/octet-stream"

/**
 * The size choice for an outgoing photo (issue #341): Small, Medium, Large
 * or Original, defaulting to the last choice made and to Large before
 * there is one. Each option reports what the image would weigh, computed
 * in the background so the dialog is usable the moment it appears.
 */
@Composable
private fun ImageSizeDialog(
    file: PickedFile,
    initial: ImageSize,
    onDismiss: () -> Unit,
    onPick: (ImageSize) -> Unit,
) {
    var selected by remember { mutableStateOf(initial) }
    var sizes by remember { mutableStateOf<Map<ImageSize, Int>>(emptyMap()) }
    val dimensions = remember(file) { ImageScaling.dimensions(file.bytes) }

    LaunchedEffect(file) {
        // The estimates are informational; the options answer taps while
        // they are still being computed.
        ImageSize.entries.forEach { size ->
            val bytes = withContext(Dispatchers.Default) {
                ImageScaling.scale(file.name, file.type, file.bytes, size).bytes.size
            }
            sizes = sizes + (size to bytes)
        }
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Image size") },
        text = {
            Column {
                dimensions?.let { (width, height) ->
                    Text(
                        text = "${file.name} - $width x $height",
                        style = MaterialTheme.typography.bodySmall,
                        modifier = Modifier.padding(bottom = 8.dp),
                    )
                }
                ImageSize.entries.forEach { size ->
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable { selected = size }
                            .padding(vertical = 8.dp)
                            .testTag("image-size-${size.name.lowercase()}"),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        RadioButton(selected = selected == size, onClick = { selected = size })
                        Text(size.label)
                        sizes[size]?.let { bytes ->
                            Text(
                                text = formatSize(bytes),
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }
                }
            }
        },
        confirmButton = {
            TextButton(onClick = { onPick(selected) }, modifier = Modifier.testTag("image-size-confirm")) {
                Text("Attach")
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
        modifier = Modifier.testTag("image-size-dialog"),
    )
}

private fun formatSize(bytes: Int): String = when {
    bytes >= 1_000_000 -> "${(bytes / 100_000) / 10.0} MB"
    bytes >= 1_000 -> "${bytes / 1_000} kB"
    else -> "$bytes B"
}

/**
 * The bytes of the inline parts the body points at, so the editor draws
 * a quoted original's images instead of an empty box (issue #431). They
 * come from the blob cache, or from a download when the cache has not
 * got them; a part that will not load is left without bytes, which
 * costs the editor the picture and the send nothing - the message still
 * carries the part.
 *
 * Bounded, because the composer opens once this returns: a body with
 * many or large inline parts draws what fits in the budget.
 */
private suspend fun ComposeState.withInlineBytes(session: SessionScope): ComposeState {
    val missing = attachments.filter { it.inline && it.bytes == null && it.blobId != null }
    if (missing.isEmpty()) return this
    var budget = INLINE_PREVIEW_BUDGET
    val loaded = mutableMapOf<String, ByteArray>()
    for (attachment in missing) {
        if (budget <= 0) break
        val bytes = runCatching {
            session.syncEngine.blob(accountId, attachment.blobId!!, attachment.type, attachment.name)
        }.getOrNull() ?: continue
        budget -= bytes.size
        loaded[attachment.key] = bytes
    }
    if (loaded.isEmpty()) return this
    return copy(
        attachments = attachments.map { attachment ->
            loaded[attachment.key]?.let { attachment.copy(bytes = it) } ?: attachment
        },
    )
}

/** How many bytes of inline parts the composer resolves before it opens. */
private const val INLINE_PREVIEW_BUDGET = 8 * 1024 * 1024

private fun titleFor(mode: ComposeMode): String = when (mode) {
    ComposeMode.NEW -> "New message"
    ComposeMode.REPLY -> "Reply"
    ComposeMode.REPLY_ALL -> "Reply all"
    ComposeMode.FORWARD -> "Forward"
    ComposeMode.EDIT_DRAFT -> "Draft"
}

/** The date the quote's attribution line carries. */
private fun formatQuoteDate(epochMillis: Long): String? {
    if (epochMillis <= 0) return null
    val local = Instant.fromEpochMilliseconds(epochMillis)
        .toLocalDateTime(TimeZone.currentSystemDefault())
    val minute = local.minute.toString().padStart(2, '0')
    val hour = local.hour.toString().padStart(2, '0')
    return "${local.dayOfMonth} ${local.month.name.lowercase().replaceFirstChar { it.uppercase() }} " +
        "${local.year}, $hour:$minute"
}
