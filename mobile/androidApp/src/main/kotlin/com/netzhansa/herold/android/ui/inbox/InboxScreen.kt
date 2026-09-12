package com.netzhansa.herold.android.ui.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
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
import androidx.compose.material.icons.filled.AccountCircle
import androidx.compose.material.icons.filled.Archive
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Menu
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Search
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
import androidx.compose.material3.LinearProgressIndicator
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
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.material3.ScrollableTabRow
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.common.LabelSheet
import com.netzhansa.herold.android.ui.common.UndoOffers
import com.netzhansa.herold.shared.actions.UndoMessages
import com.netzhansa.herold.android.ui.common.SnoozeSheet
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.actions.ActionResult
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.inbox.CategoryLanes
import com.netzhansa.herold.shared.inbox.InboxAssembler
import com.netzhansa.herold.shared.inbox.InboxItem
import com.netzhansa.herold.shared.inbox.ThreadRow
import com.netzhansa.herold.shared.sync.SyncStatus
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import kotlinx.datetime.Clock
import kotlinx.datetime.TimeZone

/**
 * The combined inbox: every account's inbox in one date-ordered stream
 * (suite REQ-MAIL-SUB-03) with a scope switcher (REQ-MAIL-SUB-02/04),
 * pinned categories as tabs and bundled ones as collapsed rows
 * (REQ-CAT-10/11). It renders from the local store, so it is populated
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
    onSignOut: () -> Unit,
) {
    val emails by container.store.inboxEmails().collectAsStateSafely(emptyList())
    val snoozedEmails by container.store.snoozedEmails().collectAsStateSafely(emptyList())
    val mailboxes by container.store.mailboxes().collectAsStateSafely(emptyList())
    val accounts by container.store.accounts().collectAsStateSafely(emptyList())
    val categories by session.syncEngine.categories.collectAsStateSafely(emptyList())
    val syncStatus by session.syncEngine.status.collectAsStateSafely(SyncStatus.Idle)

    val accountScope by container.accountScope.collectAsStateSafely(null)
    var selectedCategory by rememberSaveable { mutableStateOf<String?>(null) }
    var expandedBundles by remember { mutableStateOf(setOf<String>()) }
    var snoozedView by rememberSaveable { mutableStateOf(false) }
    val drawer = rememberDrawerState(DrawerValue.Closed)
    var snoozeTarget by remember { mutableStateOf<ThreadRow?>(null) }
    var labelTarget by remember { mutableStateOf<ThreadRow?>(null) }

    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()

    val rows = remember(emails, mailboxes, accounts, accountScope) {
        InboxAssembler.threadRows(emails, accounts, mailboxes, accountScope)
    }
    val lanes = remember(categories, emails) {
        CategoryLanes.from(categories, InboxAssembler.observedCategories(emails))
    }
    val stream = remember(rows, lanes, selectedCategory) {
        InboxAssembler.stream(rows, lanes, selectedCategory)
    }
    val snoozedRows = remember(snoozedEmails, accounts, mailboxes, accountScope) {
        InboxAssembler.snoozedRows(snoozedEmails, accounts, mailboxes, accountScope)
    }

    suspend fun emailsOf(row: ThreadRow): List<Email> =
        row.emailIds.mapNotNull { container.store.email(row.accountId, it) }

    suspend fun report(result: ActionResult) {
        if (result is ActionResult.Reverted) snackbar.showSnackbar(result.message)
    }

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

    UndoOffers(container = container, session = session, snackbar = snackbar)

    ModalNavigationDrawer(
        drawerState = drawer,
        drawerContent = {
            ModalDrawerSheet(modifier = Modifier.testTag("inbox-drawer")) {
                Text(
                    text = "Mail",
                    style = MaterialTheme.typography.titleMedium,
                    modifier = Modifier.padding(16.dp),
                )
                NavigationDrawerItem(
                    label = { Text("Inbox") },
                    selected = !snoozedView,
                    icon = { Icon(Icons.Filled.Archive, contentDescription = null) },
                    onClick = {
                        snoozedView = false
                        scope.launch { drawer.close() }
                    },
                    modifier = Modifier.padding(horizontal = 12.dp).testTag("drawer-inbox"),
                )
                NavigationDrawerItem(
                    label = { Text("Snoozed") },
                    selected = snoozedView,
                    icon = { Icon(Icons.Filled.Schedule, contentDescription = null) },
                    badge = { if (snoozedRows.isNotEmpty()) Text("${snoozedRows.size}") },
                    onClick = {
                        snoozedView = true
                        scope.launch { drawer.close() }
                    },
                    modifier = Modifier.padding(horizontal = 12.dp).testTag("drawer-snoozed"),
                )
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
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(
                        onClick = { scope.launch { drawer.open() } },
                        modifier = Modifier.testTag("inbox-drawer-open"),
                    ) {
                        Icon(Icons.Filled.Menu, contentDescription = "Destinations")
                    }
                },
                title = {
                    Text(
                        text = when {
                            snoozedView -> "Snoozed"
                            else -> accounts.firstOrNull { it.id == accountScope }?.name ?: "Inbox"
                        },
                        modifier = Modifier.testTag("inbox-title"),
                    )
                },
                actions = {
                    AccountScopeSwitcher(
                        accounts = accounts.map { it.id to it.name },
                        selected = accountScope,
                        onSelect = { container.accountScope.value = it },
                    )
                    IconButton(onClick = onSearch, modifier = Modifier.testTag("inbox-search")) {
                        Icon(Icons.Filled.Search, contentDescription = "Search")
                    }
                    IconButton(
                        onClick = { scope.launch { session.syncEngine.syncAll() } },
                        modifier = Modifier.testTag("inbox-refresh"),
                    ) {
                        Icon(Icons.Filled.Refresh, contentDescription = "Refresh")
                    }
                    OverflowMenu(onSignOut = onSignOut)
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding)) {
            if (syncStatus is SyncStatus.Syncing) {
                LinearProgressIndicator(modifier = Modifier.fillMaxWidth().testTag("inbox-syncing"))
            }
            (syncStatus as? SyncStatus.Failed)?.let { failed ->
                Text(
                    text = failed.message,
                    color = MaterialTheme.colorScheme.error,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp).testTag("inbox-sync-error"),
                )
            }

            if (snoozedView) {
                SnoozedList(
                    rows = snoozedRows,
                    showAccount = accountScope == null && accounts.size > 1,
                    onOpen = { row -> onOpenThread(row.accountId, row.threadId) },
                )
            }

            if (!snoozedView) {
            if (lanes.pinned.isNotEmpty()) {
                val tabs = listOf<String?>(null) + lanes.pinned
                ScrollableTabRow(
                    selectedTabIndex = tabs.indexOf(selectedCategory).coerceAtLeast(0),
                    edgePadding = 8.dp,
                    modifier = Modifier.testTag("inbox-tabs"),
                ) {
                    tabs.forEach { category ->
                        Tab(
                            selected = selectedCategory == category,
                            onClick = { selectedCategory = category },
                            text = { Text(category?.let(Keywords::categoryLabel) ?: "All") },
                            modifier = Modifier.testTag("inbox-tab-${category ?: "all"}"),
                        )
                    }
                }
            }

            LazyColumn(modifier = Modifier.fillMaxSize().testTag("inbox-list")) {
                items(stream, key = { item ->
                    when (item) {
                        is InboxItem.Conversation -> "thread:${item.row.accountId}:${item.row.threadId}"
                        is InboxItem.Bundle -> "bundle:${item.row.category}"
                    }
                }) { item ->
                    when (item) {
                        is InboxItem.Conversation -> SwipeableThreadRow(
                            row = item.row,
                            showAccount = accountScope == null && accounts.size > 1,
                            onOpen = { onOpenThread(item.row.accountId, item.row.threadId) },
                            onArchive = { scope.launch { archive(item.row) } },
                            onToggleStar = {
                                scope.launch {
                                    report(session.actions.setFlagged(emailsOf(item.row), !item.row.isFlagged))
                                }
                            },
                            onToggleRead = {
                                scope.launch {
                                    report(session.actions.setSeen(emailsOf(item.row), item.row.isUnread))
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
                                                report(session.actions.setFlagged(emailsOf(thread), !thread.isFlagged))
                                            }
                                        },
                                        onToggleRead = {
                                            scope.launch {
                                                report(session.actions.setSeen(emailsOf(thread), thread.isUnread))
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

    snoozeTarget?.let { row ->
        SnoozeSheet(
            onDismiss = { snoozeTarget = null },
            onPick = { wakeAt ->
                snoozeTarget = null
                scope.launch { report(session.actions.snooze(emailsOf(row), wakeAt)) }
            },
        )
    }

    labelTarget?.let { row ->
        LabelSheet(
            labels = mailboxes.filter { it.accountId == row.accountId && it.role == null },
            applied = row.labels.toSet(),
            onDismiss = { labelTarget = null },
            onToggle = { label, applied ->
                scope.launch { report(session.actions.setLabel(emailsOf(row), label, applied)) }
            },
        )
    }

    LaunchedEffect(session) {
        session.syncEngine.syncAll()
    }
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
    LazyColumn(modifier = Modifier.fillMaxSize().testTag("snoozed-list")) {
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

@Composable
private fun AccountScopeSwitcher(
    accounts: List<Pair<String, String>>,
    selected: String?,
    onSelect: (String?) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    IconButton(onClick = { open = true }, modifier = Modifier.testTag("inbox-scope-switcher")) {
        Icon(Icons.Filled.AccountCircle, contentDescription = "Account scope")
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
    }
    if (selected != null) Unit
}

@Composable
private fun OverflowMenu(onSignOut: () -> Unit) {
    var open by remember { mutableStateOf(false) }
    IconButton(onClick = { open = true }, modifier = Modifier.testTag("inbox-overflow")) {
        Icon(Icons.Filled.MoreVert, contentDescription = "More")
    }
    DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
        DropdownMenuItem(
            text = { Text("Sign out") },
            onClick = { open = false; onSignOut() },
            modifier = Modifier.testTag("menu-sign-out"),
        )
    }
}

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
