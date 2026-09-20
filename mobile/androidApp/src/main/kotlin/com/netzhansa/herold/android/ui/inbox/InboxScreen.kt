package com.netzhansa.herold.android.ui.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.AccountCircle
import androidx.compose.material.icons.filled.Archive
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Outbox
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Drafts
import androidx.compose.material.icons.filled.FilterList
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.BugReport
import androidx.compose.material.icons.filled.Inbox
import androidx.compose.material.icons.filled.Menu
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Report
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Send
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.outlined.StarBorder
import androidx.compose.material3.AssistChip
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalDrawerSheet
import androidx.compose.material3.ModalNavigationDrawer
import androidx.compose.material3.NavigationDrawerItem
import androidx.compose.material3.DrawerValue
import androidx.compose.material3.rememberDrawerState
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.SnackbarDuration
import androidx.compose.material3.SnackbarResult
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.material3.pulltorefresh.PullToRefreshState
import androidx.compose.material3.pulltorefresh.rememberPullToRefreshState
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.common.LabelSheet
import com.netzhansa.herold.android.ui.common.UndoOffers
import com.netzhansa.herold.android.ui.common.listState
import com.netzhansa.herold.android.ui.common.rememberListPositions
import com.netzhansa.herold.android.ui.common.bottomSystemBarsPadding
import com.netzhansa.herold.shared.actions.UndoMessages
import com.netzhansa.herold.android.ui.common.SnoozeSheet
import com.netzhansa.herold.android.ui.common.StatusIndicator
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.inbox.CategoryLanes
import com.netzhansa.herold.shared.inbox.DrawerModel
import com.netzhansa.herold.shared.inbox.MailDestination
import com.netzhansa.herold.shared.inbox.InboxAssembler
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.outbox.PendingMessage
import com.netzhansa.herold.shared.outbox.pendingMarkersByThread
import com.netzhansa.herold.shared.inbox.InboxItem
import com.netzhansa.herold.shared.inbox.ThreadRow
import com.netzhansa.herold.shared.sync.AppStatus
import com.netzhansa.herold.shared.sync.SyncStatus
import com.netzhansa.herold.shared.sync.appStatus
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.datetime.Clock
import kotlinx.datetime.TimeZone

/**
 * The combined inbox: every account's inbox in one date-ordered stream
 * (suite REQ-MAIL-SUB-03) with a scope switcher (REQ-MAIL-SUB-02/04),
 * pinned categories as tabs and bundled ones as collapsed rows, in the
 * dispositions the server holds on the category's label
 * (REQ-CAT-04/05/10/11). It renders from the local store, so it is populated
 * before the first network call of a cold start (REQ-AND-SYNC-03).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun InboxScreen(
    container: AppContainer,
    session: SessionScope,
    onOpenThread: (accountId: String, threadId: String) -> Unit,
    onCompose: () -> Unit,
    onSearch: () -> Unit,
    onOutbox: () -> Unit,
    onSettings: () -> Unit,
    onFilters: () -> Unit,
    onDiagnostics: () -> Unit,
    onReportProblem: () -> Unit,
    onSignOut: () -> Unit,
) {
    val emails by container.store.inboxEmails().collectAsStateSafely(emptyList())
    val snoozedEmails by container.store.snoozedEmails().collectAsStateSafely(emptyList())
    // A conversation with an unsent answer - a draft on the server or a
    // send still in the outbox - is marked in the list (issue #371).
    val drafts by container.store.draftEmails().collectAsStateSafely(emptyList())
    val queued by container.outbox.entries.collectAsStateSafely(emptyList())
    val pendingThreads = remember(queued) {
        queued.filter { it.isPending }.pendingMarkersByThread().keys
    }
    val mailboxes by container.store.mailboxes().collectAsStateSafely(emptyList())
    // The classifier's own categories. A derived category with no label
    // is still a tab, which is what an account that has never touched
    // category settings shows (issue #404).
    val derivedCategories by session.syncEngine.categories.collectAsStateSafely(emptyList())
    val accounts by container.store.accounts().collectAsStateSafely(emptyList())
    val syncStatus by session.syncEngine.status.collectAsStateSafely(SyncStatus.Idle)
    val offline by container.offline.collectAsStateSafely(false)
    val pending = remember(queued) { queued.count { it.isPending } }
    val failedInQueue = remember(queued) { queued.count { it.state == OutboxState.FAILED } }
    // One dot in the app bar says what the client is doing with the
    // server, and its slot is there in every state, so the list never
    // moves under the reader (REQ-AND-SYNC-30, issue #421).
    val status = appStatus(offline, syncStatus, pending, failedInQueue)

    val accountScope by container.accountScope.collectAsStateSafely(null)
    // The lane the reader picked, if they have picked one. The lane the
    // inbox stands on is derived from it below, so an account whose
    // lanes change under a sync opens on Primary again (issue #427).
    var pickedLane by rememberSaveable { mutableStateOf<String?>(null) }
    var expandedBundles by remember { mutableStateOf(setOf<String>()) }
    // The open destination is held as its key, so it survives process death
    // in saved instance state (REQ-AND-NAV-20).
    var destinationKey by rememberSaveable { mutableStateOf(DrawerModel.key(MailDestination.Inbox)) }
    val destination = remember(destinationKey) { DrawerModel.destination(destinationKey) }
    val drawer = rememberDrawerState(DrawerValue.Closed)
    var snoozeTarget by remember { mutableStateOf<ThreadRow?>(null) }
    var labelTarget by remember { mutableStateOf<ThreadRow?>(null) }

    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()

    // The pull to refresh, which is how the message list is reloaded
    // (REQ-AND-NAV-13, issue #444). The indicator runs for as long as
    // the pass the gesture asked for, so what it says is what the
    // client is doing rather than a fixed animation. It stops on every
    // path that wait can end on - the pass finishing, failing, never
    // starting, the wait reaching its ceiling, and the screen leaving
    // composition under it (issue #450).
    val pull = rememberPullToRefreshState()
    var refreshing by remember { mutableStateOf(false) }
    LaunchedEffect(refreshing) {
        if (!refreshing) return@LaunchedEffect
        try {
            session.syncScheduler.syncNow()
        } finally {
            refreshing = false
        }
    }

    val rows = remember(emails, mailboxes, accounts, accountScope, drafts, pendingThreads) {
        InboxAssembler.threadRows(
            emails = emails,
            accounts = accounts,
            mailboxes = mailboxes,
            accountScope = accountScope,
            drafts = drafts,
            pendingThreads = pendingThreads,
        )
    }
    // A category the mail carries but the classifier's set does not name
    // still earns its tab, which is what keeps the lanes on a server
    // that has not published its derived set yet (issue #404).
    val observedCategories = remember(emails) { InboxAssembler.observedCategories(emails) }
    val lanes = remember(mailboxes, derivedCategories, observedCategories, accountScope) {
        CategoryLanes.from(mailboxes, derivedCategories, observedCategories, accountScope)
    }
    // The inbox opens on the primary-role lane (REQ-CAT-03) and stays on
    // the reader's pick for as long as that lane exists.
    val selectedLane = remember(lanes, pickedLane) { lanes.select(pickedLane) }
    val stream = remember(rows, lanes, selectedLane) {
        InboxAssembler.stream(rows, lanes, selectedLane)
    }
    // The counts the tabs badge, read from the same rows the list
    // renders, so opening a message clears the badge with the row.
    val unreadByLane = remember(rows, lanes) { InboxAssembler.unreadByLane(rows, lanes) }
    val snoozedRows = remember(snoozedEmails, accounts, mailboxes, accountScope) {
        InboxAssembler.snoozedRows(snoozedEmails, accounts, mailboxes, accountScope)
    }
    val folders = remember(mailboxes, accountScope) { DrawerModel.folders(mailboxes, accountScope) }
    val labelTree = remember(mailboxes, accountScope) { DrawerModel.labels(mailboxes, accountScope) }
    val inboxUnread = remember(mailboxes, accountScope) { DrawerModel.inboxUnread(mailboxes, accountScope) }

    // The mailboxes the open destination stands for: one per account in
    // scope, so a label opens across the combined view the inbox shows.
    val openMailboxes = remember(destination, mailboxes, accountScope) {
        (destination as? MailDestination.Folder)
            ?.let { DrawerModel.mailboxesFor(it, mailboxes, accountScope) }
            .orEmpty()
    }
    // The destination's messages, re-collected whenever the destination
    // changes: the state starts empty for the new mailbox rather than
    // showing the previous one's rows until the first emission.
    val folderEmails by produceState(initialValue = emptyList<Email>(), openMailboxes) {
        value = emptyList()
        container.store.mailboxEmails(openMailboxes.map { it.id }).collect { value = it }
    }
    val folderRows = remember(folderEmails, accounts, mailboxes, accountScope) {
        InboxAssembler.threadRows(folderEmails, accounts, mailboxes, accountScope)
    }

    // Where each list of this screen was left, in the screen's own saved
    // state: a lane, a mailbox and a label each keep their own offset
    // across a trip into a conversation and across the process
    // (issue #439, REQ-AND-NAV-25).
    val positions = rememberListPositions()

    val folderKey = "folder:$destinationKey"
    val folderListState = positions.listState(folderKey, folderRows.isNotEmpty())
    // The fill below puts conversations ahead of the ones already
    // listed, and a list holds its position against that, so a
    // destination the user has not scrolled is kept at its newest.
    LaunchedEffect(destinationKey, folderRows.firstOrNull()?.threadId) {
        if (!positions.scrolled(folderKey)) folderListState.scrollToItem(0)
    }

    val laneKey = "lane:${selectedLane ?: "all"}"
    val laneListState = positions.listState(laneKey, stream.isNotEmpty())
    LaunchedEffect(laneKey, stream.firstOrNull()?.let { inboxItemKey(it) }) {
        if (!positions.scrolled(laneKey)) laneListState.scrollToItem(0)
    }

    val snoozedListState = positions.listState("snoozed")

    // The first pass fills the inbox; a destination the user opens is
    // filled when they open it (REQ-AND-SYNC-02, issue #374).
    LaunchedEffect(openMailboxes) {
        openMailboxes.forEach { session.syncEngine.ensureMailbox(it.accountId, it.id) }
    }

    suspend fun emailsOf(row: ThreadRow): List<Email> =
        row.emailIds.mapNotNull { container.store.email(row.accountId, it) }

    /**
     * Archive with undo. The rows leave the list on the optimistic write and
     * the offer is parked with them, while the `Email/set` runs underneath:
     * an undo offered only after the round trip is one the user does not see
     * on a slow link (issue #338). [UndoOffers] below raises the snackbar,
     * for this archive and for one the thread view left behind (issue #345).
     */
    suspend fun archive(row: ThreadRow) {
        container.undo.offer(
            message = UndoMessages.ARCHIVED,
            action = session.actions.archiveLocally(emailsOf(row), mailboxes),
            actions = session.actions,
        )
    }

    UndoOffers(container = container, snackbar = snackbar)

    ModalNavigationDrawer(
        drawerState = drawer,
        drawerContent = {
            ModalDrawerSheet(modifier = Modifier.testTag("inbox-drawer")) {
              // The mailboxes scroll; Filters and Settings are pinned
              // below them, so an account with many labels does not push
              // them off the sheet.
              Column(modifier = Modifier.fillMaxHeight()) {
              Column(
                  modifier = Modifier
                      .weight(1f)
                      .verticalScroll(rememberScrollState()),
              ) {
                Text(
                    text = "Mail",
                    style = MaterialTheme.typography.titleMedium,
                    modifier = Modifier.padding(16.dp),
                )
                NavigationDrawerItem(
                    label = { Text("Inbox") },
                    selected = destination == MailDestination.Inbox,
                    icon = { Icon(Icons.Filled.Inbox, contentDescription = null) },
                    badge = { if (inboxUnread > 0) Text("$inboxUnread") },
                    onClick = {
                        destinationKey = DrawerModel.key(MailDestination.Inbox)
                        scope.launch { drawer.close() }
                    },
                    modifier = Modifier.padding(horizontal = 12.dp).testTag("drawer-inbox"),
                )
                NavigationDrawerItem(
                    label = { Text("Snoozed") },
                    selected = destination == MailDestination.Snoozed,
                    icon = { Icon(Icons.Filled.Schedule, contentDescription = null) },
                    badge = { if (snoozedRows.isNotEmpty()) Text("${snoozedRows.size}") },
                    onClick = {
                        destinationKey = DrawerModel.key(MailDestination.Snoozed)
                        scope.launch { drawer.close() }
                    },
                    modifier = Modifier.padding(horizontal = 12.dp).testTag("drawer-snoozed"),
                )
                // The system folders in the suite's sidebar order
                // (REQ-UI-13b), each with the unread count of the accounts
                // in scope (REQ-UI-13c).
                folders.forEach { folder ->
                    NavigationDrawerItem(
                        label = { Text(folder.title) },
                        selected = destination == folder.destination,
                        icon = { Icon(folderIcon(folder.role), contentDescription = null) },
                        badge = { if (folder.unread > 0) Text("${folder.unread}") },
                        onClick = {
                            destinationKey = DrawerModel.key(folder.destination)
                            scope.launch { drawer.close() }
                        },
                        modifier = Modifier
                            .padding(horizontal = 12.dp)
                            .testTag("drawer-folder-${folder.role}"),
                    )
                }
                NavigationDrawerItem(
                    label = { Text("Outbox") },
                    selected = false,
                    icon = { Icon(Icons.Filled.Outbox, contentDescription = null) },
                    badge = { if (pending > 0) Text("$pending") },
                    onClick = {
                        scope.launch { drawer.close() }
                        onOutbox()
                    },
                    modifier = Modifier.padding(horizontal = 12.dp).testTag("drawer-outbox"),
                )
                // The label tree (REQ-UI-13d): top-level labels at the
                // root, children indented, each with its colour swatch and
                // unread count.
                if (labelTree.isNotEmpty()) {
                    Text(
                        text = "Labels",
                        style = MaterialTheme.typography.titleSmall,
                        modifier = Modifier.padding(start = 28.dp, top = 16.dp, bottom = 4.dp),
                    )
                    labelTree.forEach { label ->
                        NavigationDrawerItem(
                            label = { Text(label.name) },
                            selected = destination == label.destination,
                            icon = { LabelDot(label.title) },
                            badge = { if (label.unread > 0) Text("${label.unread}") },
                            onClick = {
                                destinationKey = DrawerModel.key(label.destination)
                                scope.launch { drawer.close() }
                            },
                            modifier = Modifier
                                .padding(start = 12.dp + (label.depth * 16).dp, end = 12.dp)
                                .testTag("drawer-label-${label.title}"),
                        )
                    }
                }
              }
              // The entries pinned to the foot of the sheet stand above
              // what the system holds along the bottom edge, so the last
              // of them is not under the gesture handle (issue #428).
              Column(modifier = Modifier.bottomSystemBarsPadding()) {
                HorizontalDivider(modifier = Modifier.padding(vertical = 8.dp))
                NavigationDrawerItem(
                    label = { Text("Filters") },
                    selected = false,
                    icon = { Icon(Icons.Filled.FilterList, contentDescription = null) },
                    onClick = {
                        scope.launch { drawer.close() }
                        onFilters()
                    },
                    modifier = Modifier.padding(horizontal = 12.dp).testTag("drawer-filters"),
                )
                NavigationDrawerItem(
                    label = { Text("Settings") },
                    selected = false,
                    icon = { Icon(Icons.Filled.Settings, contentDescription = null) },
                    onClick = {
                        scope.launch { drawer.close() }
                        onSettings()
                    },
                    modifier = Modifier.padding(horizontal = 12.dp).testTag("drawer-settings"),
                )
                // The report captures the screen behind the drawer, so
                // the drawer is closed before the capture is asked for
                // (REQ-AND-SYS-50).
                NavigationDrawerItem(
                    label = { Text("Report a problem") },
                    selected = false,
                    icon = { Icon(Icons.Filled.BugReport, contentDescription = null) },
                    onClick = {
                        scope.launch {
                            drawer.close()
                            onReportProblem()
                        }
                    },
                    modifier = Modifier.padding(horizontal = 12.dp).testTag("drawer-report-problem"),
                )
              }
              }
            }
        },
    ) {
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar, modifier = Modifier.testTag("inbox-snackbar")) },
        floatingActionButton = {
            FloatingActionButton(onClick = onCompose, modifier = Modifier.testTag("inbox-compose")) {
                Icon(Icons.Filled.Edit, contentDescription = "Compose")
            }
        },
        // The top row carries the drawer, the search field and the
        // avatar, and nothing besides (REQ-AND-NAV-02, issue #444).
        topBar = {
            TopAppBar(
                modifier = Modifier.testTag("inbox-top-bar"),
                navigationIcon = {
                    IconButton(
                        onClick = { scope.launch { drawer.open() } },
                        modifier = Modifier.testTag("inbox-drawer-open"),
                    ) {
                        Icon(Icons.Filled.Menu, contentDescription = "Destinations")
                    }
                },
                title = { SearchField(onClick = onSearch) },
                actions = {
                    AccountMenu(
                        accounts = accounts.map { it.id to it.name },
                        onSelect = { container.accountScope.value = it },
                        onSignOut = onSignOut,
                    )
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding)) {
            // The open destination's name, its lanes and the status dot
            // stand in a row of their own under the top row
            // (REQ-AND-NAV-02, issue #444).
            MailboxRow(
                title = when (destination) {
                    MailDestination.Snoozed -> "Snoozed"
                    is MailDestination.Folder -> (destination as MailDestination.Folder).title
                    MailDestination.Inbox ->
                        accounts.firstOrNull { it.id == accountScope }?.name ?: "Inbox"
                },
                tabs = if (destination == MailDestination.Inbox) lanes.tabs else emptyList(),
                selectedLane = selectedLane,
                unreadByLane = unreadByLane,
                onSelectLane = { pickedLane = it },
                status = status,
                onOpenDiagnostics = onDiagnostics,
            )
            PullToRefreshBox(
                isRefreshing = refreshing,
                // The forced pass goes through the loop, so a refusal
                // the loop is backing off from does not hold the user's
                // own refresh up (issue #436).
                onRefresh = { refreshing = true },
                state = pull,
                indicator = {
                    RefreshIndicator(
                        state = pull,
                        refreshing = refreshing,
                        modifier = Modifier.align(Alignment.TopCenter),
                    )
                },
                modifier = Modifier.fillMaxSize(),
            ) {
        Column(modifier = Modifier.fillMaxSize()) {
            if (destination == MailDestination.Snoozed) {
                SnoozedList(
                    rows = snoozedRows,
                    listState = snoozedListState,
                    showAccount = accountScope == null && accounts.size > 1,
                    onOpen = { row -> onOpenThread(row.accountId, row.threadId) },
                )
            }

            // A folder or a label lists its conversations with the inbox's
            // row model and actions (issue #374).
            if (destination is MailDestination.Folder) {
                if (folderRows.isEmpty()) {
                    Text(
                        text = "Nothing here yet.",
                        modifier = Modifier.fillMaxWidth().padding(24.dp).testTag("mailbox-empty"),
                    )
                }
                LazyColumn(
                    state = folderListState,
                    modifier = Modifier.fillMaxSize().testTag("mailbox-list"),
                ) {
                    items(folderRows, key = { "mailbox:${it.accountId}:${it.threadId}" }) { row ->
                        SwipeableThreadRow(
                            row = row,
                            showAccount = accountScope == null && accounts.size > 1,
                            onOpen = { onOpenThread(row.accountId, row.threadId) },
                            onArchive = { scope.launch { archive(row) } },
                            onToggleStar = {
                                scope.launch { session.actions.setFlagged(emailsOf(row), !row.isFlagged) }
                            },
                            onToggleRead = {
                                scope.launch { session.actions.setSeen(emailsOf(row), row.isUnread) }
                            },
                            onSnooze = { snoozeTarget = row },
                            onLabel = { labelTarget = row },
                        )
                        HorizontalDivider()
                    }
                }
            }

            if (destination == MailDestination.Inbox) {
            if (stream.isEmpty()) {
                Text(
                    text = if (selectedLane == null) {
                        "Your inbox is empty."
                    } else {
                        "Nothing in this category."
                    },
                    modifier = Modifier.fillMaxWidth().padding(24.dp).testTag("inbox-empty"),
                )
            }
            LazyColumn(
                state = laneListState,
                modifier = Modifier.fillMaxSize().testTag("inbox-list"),
            ) {
                items(stream, key = { item -> inboxItemKey(item) }) { item ->
                    when (item) {
                        is InboxItem.Conversation -> SwipeableThreadRow(
                            row = item.row,
                            showAccount = accountScope == null && accounts.size > 1,
                            onOpen = { onOpenThread(item.row.accountId, item.row.threadId) },
                            onArchive = { scope.launch { archive(item.row) } },
                            onToggleStar = {
                                scope.launch {
                                    session.actions.setFlagged(emailsOf(item.row), !item.row.isFlagged)
                                }
                            },
                            onToggleRead = {
                                scope.launch {
                                    session.actions.setSeen(emailsOf(item.row), item.row.isUnread)
                                }
                            },
                            onSnooze = { snoozeTarget = item.row },
                            onLabel = { labelTarget = item.row },
                        )

                        is InboxItem.Bundle -> {
                            val expanded = expandedBundles.contains(item.row.category)
                            BundleRowItem(
                                row = item.row,
                                expanded = expanded,
                                onToggle = {
                                    expandedBundles = if (expanded) {
                                        expandedBundles - item.row.category
                                    } else {
                                        expandedBundles + item.row.category
                                    }
                                },
                            )
                            if (expanded) {
                                item.row.threads.forEach { thread ->
                                    SwipeableThreadRow(
                                        row = thread,
                                        showAccount = accountScope == null && accounts.size > 1,
                                        onOpen = { onOpenThread(thread.accountId, thread.threadId) },
                                        onArchive = { scope.launch { archive(thread) } },
                                        onToggleStar = {
                                            scope.launch {
                                                session.actions.setFlagged(emailsOf(thread), !thread.isFlagged)
                                            }
                                        },
                                        onToggleRead = {
                                            scope.launch {
                                                session.actions.setSeen(emailsOf(thread), thread.isUnread)
                                            }
                                        },
                                        onSnooze = { snoozeTarget = thread },
                                        onLabel = { labelTarget = thread },
                                        indented = true,
                                    )
                                }
                            }
                        }
                    }
                    HorizontalDivider()
                }
            }
            }
        }
            }
        }
    }
    }

    snoozeTarget?.let { row ->
        SnoozeSheet(
            onDismiss = { snoozeTarget = null },
            onPick = { wakeAt ->
                snoozeTarget = null
                scope.launch { session.actions.snooze(emailsOf(row), wakeAt) }
            },
        )
    }

    labelTarget?.let { row ->
        LabelSheet(
            labels = mailboxes.filter { it.accountId == row.accountId && it.role == null },
            applied = row.labels.toSet(),
            onDismiss = { labelTarget = null },
            onToggle = { label, applied ->
                scope.launch { session.actions.setLabel(emailsOf(row), label, applied) }
            },
        )
    }

    LaunchedEffect(session) {
        session.syncScheduler.requestSync()
    }
}

/**
 * What identifies an item of the inbox stream: the conversation's
 * thread or the bundle's category. The list keys its rows on it, and
 * the pin that holds an untouched lane at its newest message watches
 * the leading one.
 */
private fun inboxItemKey(item: InboxItem): String = when (item) {
    is InboxItem.Conversation -> "thread:${item.row.accountId}:${item.row.threadId}"
    is InboxItem.Bundle -> "bundle:${item.row.category}"
}

/**
 * The Snoozed destination (suite REQ-SNZ-10/14, issue #353): the
 * conversations the server holds a wake time for, next to wake first,
 * each stating when it comes back. Opening one lands on the thread view,
 * where the wake time can be edited or cancelled.
 */
@Composable
private fun SnoozedList(
    rows: List<ThreadRow>,
    listState: LazyListState,
    showAccount: Boolean,
    onOpen: (ThreadRow) -> Unit,
) {
    if (rows.isEmpty()) {
        Text(
            text = "Nothing is snoozed.",
            modifier = Modifier.fillMaxWidth().padding(24.dp).testTag("snoozed-empty"),
        )
        return
    }
    LazyColumn(state = listState, modifier = Modifier.fillMaxSize().testTag("snoozed-list")) {
        items(rows, key = { "snoozed:${it.accountId}:${it.threadId}" }) { row ->
            SnoozedRowItem(row = row, showAccount = showAccount, onOpen = { onOpen(row) })
            HorizontalDivider()
        }
    }
}

@Composable
private fun SnoozedRowItem(
    row: ThreadRow,
    showAccount: Boolean,
    onOpen: () -> Unit,
) {
    val zone = TimeZone.currentSystemDefault()
    val wake = row.wakeAt
        ?.let { SnoozeClock.parseWake(it) }
        ?.let { SnoozeClock.describe(it, Clock.System.now(), zone) }
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(MaterialTheme.colorScheme.surface)
            .clickable(onClick = onOpen)
            .padding(start = 12.dp, end = 12.dp, top = 10.dp, bottom = 10.dp)
            .testTag("thread-row-${row.threadId}"),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = row.senders.ifBlank { "(unknown sender)" },
                style = MaterialTheme.typography.titleSmall,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            Text(
                text = row.subject,
                style = MaterialTheme.typography.bodyMedium,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.testTag("thread-subject-${row.threadId}"),
            )
            if (wake != null) {
                Text(
                    text = "Wakes $wake",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.testTag("thread-wake-${row.threadId}"),
                )
            }
            if (showAccount) {
                AssistChip(
                    onClick = {},
                    label = { Text(row.accountName, style = MaterialTheme.typography.labelSmall) },
                )
            }
        }
        Icon(Icons.Filled.Schedule, contentDescription = null)
    }
}

/**
 * The way into search from the inbox's top row (issue #444).
 *
 * The field is a target that opens the search screen, whose own field
 * takes focus as it comes up: the query, its results and the place the
 * results list was left belong to that destination's back-stack entry
 * (issues #340, #439), so the text is typed into the field that lives
 * there.
 */
@Composable
private fun SearchField(onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        shape = CircleShape,
        color = MaterialTheme.colorScheme.surfaceContainerHighest,
        modifier = Modifier
            .fillMaxWidth()
            .height(SEARCH_FIELD_HEIGHT)
            .testTag("inbox-search"),
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
            modifier = Modifier.padding(horizontal = 16.dp),
        ) {
            Icon(
                imageVector = Icons.Filled.Search,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Text(
                text = "Search mail",
                style = MaterialTheme.typography.bodyLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

/** How tall the top row's search field stands. */
private val SEARCH_FIELD_HEIGHT = 44.dp

/**
 * The avatar at the end of the top row and what it opens: the account
 * the inbox is scoped to, and signing out (issue #444).
 */
@Composable
private fun AccountMenu(
    accounts: List<Pair<String, String>>,
    onSelect: (String?) -> Unit,
    onSignOut: () -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    IconButton(onClick = { open = true }, modifier = Modifier.testTag("inbox-avatar")) {
        Icon(Icons.Filled.AccountCircle, contentDescription = "Account")
    }
    DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
        DropdownMenuItem(
            text = { Text("All accounts") },
            onClick = { onSelect(null); open = false },
            modifier = Modifier.testTag("scope-all"),
        )
        accounts.forEach { (id, name) ->
            DropdownMenuItem(
                text = { Text(name) },
                onClick = { onSelect(id); open = false },
                modifier = Modifier.testTag("scope-$id"),
            )
        }
        HorizontalDivider()
        DropdownMenuItem(
            text = { Text("Sign out") },
            onClick = { open = false; onSignOut() },
            modifier = Modifier.testTag("account-sign-out"),
        )
    }
}

/**
 * The row under the top row: the open destination's name, the inbox's
 * lanes and the status dot (issue #444).
 *
 * The dot keeps the fixed slot it had in the app bar, at the end of
 * this row, so it still says what the client is doing with the server
 * without anything in the list moving when it changes (REQ-AND-SYNC-30,
 * issue #421).
 */
@Composable
private fun MailboxRow(
    title: String,
    tabs: List<String>,
    selectedLane: String?,
    unreadByLane: Map<String, Int>,
    onSelectLane: (String) -> Unit,
    status: AppStatus,
    onOpenDiagnostics: () -> Unit,
) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier.fillMaxWidth().testTag("inbox-mailbox-row"),
    ) {
        Text(
            text = title,
            style = MaterialTheme.typography.titleMedium,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier
                .widthIn(max = MAILBOX_NAME_WIDTH)
                .padding(start = 16.dp, end = 8.dp)
                .testTag("inbox-title"),
        )
        if (tabs.isNotEmpty()) {
            CategoryTabRow(
                tabs = tabs,
                selected = selectedLane,
                unreadByLane = unreadByLane,
                onSelect = onSelectLane,
                modifier = Modifier.weight(1f),
            )
        } else {
            Spacer(modifier = Modifier.weight(1f))
        }
        StatusIndicator(status = status, onOpenDiagnostics = onOpenDiagnostics)
    }
}

/** The most the destination's name takes of its row, so the lanes keep theirs. */
private val MAILBOX_NAME_WIDTH = 140.dp

/**
 * The pull-to-refresh indicator, in the app's own colours (issue #444).
 *
 * It turns from a coroutine rather than the frame clock, for the reason
 * the status dot's pulse does (issue #421): an animation that holds the
 * frame clock for as long as a sync runs makes every "wait until the UI
 * settles" wait for the network. What it says - pulling, or running -
 * is on the node, so TalkBack reads it and a check can assert it.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun RefreshIndicator(
    state: PullToRefreshState,
    refreshing: Boolean,
    modifier: Modifier = Modifier,
) {
    val reach = if (refreshing) 1f else state.distanceFraction.coerceIn(0f, 1f)
    val angle = if (refreshing) spinAngle() else reach * PULL_SWEEP
    val description = if (refreshing) "Refreshing" else "Pull to refresh"
    Surface(
        shape = CircleShape,
        color = MaterialTheme.colorScheme.surfaceContainerHigh,
        shadowElevation = 2.dp,
        modifier = modifier
            .offset(y = (INDICATOR_SIZE + INDICATOR_MARGIN) * reach - INDICATOR_SIZE)
            .size(INDICATOR_SIZE)
            .alpha(reach)
            .semantics { contentDescription = description }
            .testTag("inbox-refresh-indicator"),
    ) {
        Box(contentAlignment = Alignment.Center) {
            Icon(
                imageVector = Icons.Filled.Refresh,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.primary,
                modifier = Modifier.size(INDICATOR_ICON).rotate(angle),
            )
        }
    }
}

/** The angle the running indicator stands at, stepped off the frame clock. */
@Composable
private fun spinAngle(): Float {
    var angle by remember { mutableFloatStateOf(0f) }
    LaunchedEffect(Unit) {
        while (true) {
            angle = (angle + SPIN_STEP) % 360f
            delay(SPIN_STEP_MS)
        }
    }
    return angle
}

/** The indicator's disc, its icon, and how far below the edge it lands. */
private val INDICATOR_SIZE = 40.dp
private val INDICATOR_ICON = 24.dp
private val INDICATOR_MARGIN = 12.dp

/** How far the icon turns as the pull reaches the threshold. */
private const val PULL_SWEEP = 270f

/** How far, and how often, the running indicator turns. */
private const val SPIN_STEP = 30f
private const val SPIN_STEP_MS = 80L

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun SwipeableThreadRow(
    row: ThreadRow,
    showAccount: Boolean,
    onOpen: () -> Unit,
    onArchive: () -> Unit,
    onToggleStar: () -> Unit,
    onToggleRead: () -> Unit,
    onSnooze: () -> Unit,
    onLabel: () -> Unit,
    indented: Boolean = false,
) {
    val dismissState = rememberSwipeToDismissBoxState(
        confirmValueChange = { value ->
            if (value != SwipeToDismissBoxValue.Settled) {
                onArchive()
                false
            } else {
                false
            }
        },
    )
    SwipeToDismissBox(
        state = dismissState,
        backgroundContent = {
            Box(
                modifier = Modifier
                    .fillMaxSize()
                    .background(MaterialTheme.colorScheme.secondaryContainer)
                    .padding(horizontal = 20.dp),
                contentAlignment = Alignment.CenterStart,
            ) {
                Icon(Icons.Filled.Archive, contentDescription = "Archive")
            }
        },
        modifier = Modifier.testTag("thread-swipe-${row.threadId}"),
    ) {
        ThreadRowItem(
            row = row,
            showAccount = showAccount,
            indented = indented,
            onOpen = onOpen,
            onToggleStar = onToggleStar,
            onArchive = onArchive,
            onToggleRead = onToggleRead,
            onSnooze = onSnooze,
            onLabel = onLabel,
        )
    }
}

@Composable
private fun ThreadRowItem(
    row: ThreadRow,
    showAccount: Boolean,
    indented: Boolean,
    onOpen: () -> Unit,
    onToggleStar: () -> Unit,
    onArchive: () -> Unit,
    onToggleRead: () -> Unit,
    onSnooze: () -> Unit,
    onLabel: () -> Unit,
) {
    var menuOpen by remember { mutableStateOf(false) }
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(MaterialTheme.colorScheme.surface)
            .clickable(onClick = onOpen)
            .padding(start = if (indented) 32.dp else 12.dp, end = 4.dp, top = 10.dp, bottom = 10.dp)
            .testTag("thread-row-${row.threadId}"),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(
                    text = row.senders.ifBlank { "(unknown sender)" },
                    style = MaterialTheme.typography.titleSmall,
                    fontWeight = if (row.isUnread) FontWeight.Bold else FontWeight.Normal,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false),
                )
                if (row.messageCount > 1) Text("${row.messageCount}", style = MaterialTheme.typography.labelSmall)
            }
            Text(
                text = row.subject,
                style = MaterialTheme.typography.bodyMedium,
                fontWeight = if (row.isUnread) FontWeight.SemiBold else FontWeight.Normal,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.testTag("thread-subject-${row.threadId}"),
            )
            Text(
                text = row.preview,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                if (row.hasDraft) {
                    AssistChip(
                        onClick = {},
                        label = {
                            Text(
                                text = PendingMessage.MARKER_DRAFT,
                                style = MaterialTheme.typography.labelSmall,
                                color = MaterialTheme.colorScheme.primary,
                            )
                        },
                        modifier = Modifier.testTag("thread-draft-${row.threadId}"),
                    )
                }
                if (showAccount) {
                    AssistChip(onClick = {}, label = { Text(row.accountName, style = MaterialTheme.typography.labelSmall) })
                }
                row.labels.take(3).forEach { label ->
                    AssistChip(
                        onClick = {},
                        label = { Text(label, style = MaterialTheme.typography.labelSmall) },
                        modifier = Modifier.testTag("thread-label-$label"),
                    )
                }
            }
        }
        IconButton(onClick = onToggleStar, modifier = Modifier.size(40.dp).testTag("thread-star-${row.threadId}")) {
            Icon(
                imageVector = if (row.isFlagged) Icons.Filled.Star else Icons.Outlined.StarBorder,
                contentDescription = if (row.isFlagged) "Unstar" else "Star",
            )
        }
        IconButton(onClick = { menuOpen = true }, modifier = Modifier.size(40.dp).testTag("thread-menu-${row.threadId}")) {
            Icon(Icons.Filled.MoreVert, contentDescription = "Actions")
        }
        DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
            DropdownMenuItem(
                text = { Text("Archive") },
                onClick = { menuOpen = false; onArchive() },
                modifier = Modifier.testTag("action-archive-${row.threadId}"),
            )
            DropdownMenuItem(
                text = { Text(if (row.isUnread) "Mark read" else "Mark unread") },
                onClick = { menuOpen = false; onToggleRead() },
                modifier = Modifier.testTag("action-read-${row.threadId}"),
            )
            DropdownMenuItem(
                text = { Text("Snooze") },
                onClick = { menuOpen = false; onSnooze() },
                modifier = Modifier.testTag("action-snooze-${row.threadId}"),
            )
            DropdownMenuItem(
                text = { Text("Labels") },
                onClick = { menuOpen = false; onLabel() },
                modifier = Modifier.testTag("action-label-${row.threadId}"),
            )
        }
    }
}

@Composable
private fun BundleRowItem(
    row: com.netzhansa.herold.shared.inbox.BundleRow,
    expanded: Boolean,
    onToggle: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onToggle)
            .padding(horizontal = 12.dp, vertical = 14.dp)
            .testTag("bundle-row-${row.category}"),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(if (expanded) "v" else ">", style = MaterialTheme.typography.titleMedium)
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = "${Keywords.categoryLabel(row.category)} (${row.threadCount})",
                style = MaterialTheme.typography.titleSmall,
                fontWeight = if (row.unreadCount > 0) FontWeight.Bold else FontWeight.Normal,
            )
            Text(
                text = row.senders,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

/** The icon a system folder carries in the drawer. */
private fun folderIcon(role: String?) = when (role) {
    MailboxRoles.SENT -> Icons.Filled.Send
    MailboxRoles.DRAFTS -> Icons.Filled.Drafts
    MailboxRoles.ARCHIVE -> Icons.Filled.Archive
    MailboxRoles.JUNK -> Icons.Filled.Report
    MailboxRoles.TRASH -> Icons.Filled.Delete
    else -> Icons.Filled.Folder
}

/**
 * A label's colour swatch (suite REQ-UI-13d). herold carries no colour on
 * `Mailbox`, so the swatch is derived from the label's path, which is what
 * the suite falls back to (`docs/design/web/requirements/03-labels.md`,
 * "Colour storage").
 */
@Composable
private fun LabelDot(path: String) {
    Box(
        modifier = Modifier
            .size(12.dp)
            .clip(CircleShape)
            .background(labelColour(path)),
    )
}

private fun labelColour(path: String): Color {
    val hue = ((path.hashCode().toLong() and 0xFFFFFFFFL) % 360L).toFloat()
    return Color.hsv(hue, SWATCH_SATURATION, SWATCH_VALUE)
}

private const val SWATCH_SATURATION = 0.55f
private const val SWATCH_VALUE = 0.75f
