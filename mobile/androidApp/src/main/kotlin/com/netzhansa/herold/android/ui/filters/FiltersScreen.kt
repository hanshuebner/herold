package com.netzhansa.herold.android.ui.filters

import androidx.compose.foundation.clickable
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
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.produceState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.actions.FilterActions
import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.filters.RuleText
import kotlinx.coroutines.launch

/**
 * The filter list (suite REQ-FLT-20/31): every rule the account holds, in
 * the order the server runs them, each stating its conditions and actions
 * in words. It renders the local store's rows - the sync engine folds
 * `ManagedRule/changes` into them - so a filter written in the suite shows
 * up here without this screen fetching anything.
 *
 * The per-conversation mute rules the thread overflow writes are left out:
 * they are a mute, not a filter the user authored.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FiltersScreen(
    container: AppContainer,
    session: SessionScope,
    accountId: String,
    onEdit: (ruleId: String) -> Unit,
    onCreate: () -> Unit,
    onBack: () -> Unit,
) {
    val all by container.store.managedRules().collectAsStateSafely(emptyList())
    val offline by container.offline.collectAsStateSafely(false)
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    var confirmDelete by remember { mutableStateOf<ManagedRule?>(null) }
    // Read once per account: the section lives in the lazy list, which
    // composes and disposes it as the list scrolls.
    val sieveScript by produceState<String?>(null, accountId) {
        value = runCatching { session.client.sieveScript(accountId) }.getOrNull()
    }

    val rules = remember(all, accountId) {
        FilterActions.userRules(all.filter { it.accountId == accountId })
            .sortedWith(compareBy({ it.order }, { it.id }))
    }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbar, modifier = Modifier.testTag("filters-snackbar")) },
        floatingActionButton = {
            FloatingActionButton(onClick = onCreate, modifier = Modifier.testTag("filters-new")) {
                Icon(Icons.Filled.Add, contentDescription = "New filter")
            }
        },
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("filters-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = { Text("Filters", modifier = Modifier.testTag("filters-title")) },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding)) {
            Text(
                text = "Rules run in this order on every message that arrives.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
            )
            if (offline) {
                Text(
                    text = "Offline: a change you make here goes out when the phone reconnects.",
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier
                        .padding(horizontal = 16.dp, vertical = 4.dp)
                        .testTag("filters-offline"),
                )
            }
            if (rules.isEmpty()) {
                Text(
                    text = "No filters yet. Create one to label, archive or delete mail as it arrives.",
                    modifier = Modifier.padding(16.dp).testTag("filters-empty"),
                )
            }
            LazyColumn(modifier = Modifier.weight(1f).testTag("filters-list")) {
                items(rules, key = { it.id }) { rule ->
                    FilterRow(
                        rule = rule,
                        isFirst = rule.id == rules.first().id,
                        isLast = rule.id == rules.last().id,
                        onOpen = { onEdit(rule.id) },
                        onToggle = { enabled -> scope.launch { session.filters.setEnabled(rule, enabled) } },
                        onMove = { up -> scope.launch { session.filters.move(rule, rules, up) } },
                        onDelete = { confirmDelete = rule },
                    )
                    HorizontalDivider()
                }
                item { SieveSection(script = sieveScript) }
            }
        }
    }

    confirmDelete?.let { rule ->
        androidx.compose.material3.AlertDialog(
            onDismissRequest = { confirmDelete = null },
            title = { Text("Delete this filter?") },
            text = { Text(RuleText.title(rule)) },
            confirmButton = {
                TextButton(
                    onClick = {
                        confirmDelete = null
                        scope.launch { session.filters.delete(rule) }
                    },
                    modifier = Modifier.testTag("filter-delete-confirm"),
                ) { Text("Delete") }
            },
            dismissButton = {
                TextButton(onClick = { confirmDelete = null }) { Text("Keep") }
            },
        )
    }
}

@Composable
private fun FilterRow(
    rule: ManagedRule,
    isFirst: Boolean,
    isLast: Boolean,
    onOpen: () -> Unit,
    onToggle: (Boolean) -> Unit,
    onMove: (Boolean) -> Unit,
    onDelete: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onOpen)
            .padding(horizontal = 16.dp, vertical = 10.dp)
            .testTag("filter-${rule.id}"),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = RuleText.title(rule),
                    style = MaterialTheme.typography.titleSmall,
                    modifier = Modifier.testTag("filter-title-${rule.id}"),
                )
                Text(
                    text = RuleText.conditionLine(rule),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.testTag("filter-conditions-${rule.id}"),
                )
                Text(
                    text = RuleText.actionLine(rule),
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.testTag("filter-actions-${rule.id}"),
                )
            }
            Switch(
                checked = rule.enabled,
                onCheckedChange = onToggle,
                modifier = Modifier.testTag("filter-enabled-${rule.id}"),
            )
        }
        Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            IconButton(
                onClick = { onMove(true) },
                enabled = !isFirst,
                modifier = Modifier.testTag("filter-up-${rule.id}"),
            ) {
                Icon(Icons.Filled.ArrowUpward, contentDescription = "Move up")
            }
            IconButton(
                onClick = { onMove(false) },
                enabled = !isLast,
                modifier = Modifier.testTag("filter-down-${rule.id}"),
            ) {
                Icon(Icons.Filled.ArrowDownward, contentDescription = "Move down")
            }
            IconButton(onClick = onDelete, modifier = Modifier.testTag("filter-delete-${rule.id}")) {
                Icon(Icons.Filled.Delete, contentDescription = "Delete")
            }
        }
    }
}

/**
 * The hand-written Sieve script, read-only (suite REQ-FLT-22). The phone
 * edits the structured rules above; the script itself is the suite's
 * surface, and showing it here keeps a user who has one from wondering
 * where their filtering went.
 */
@Composable
private fun SieveSection(script: String?) {
    if (script.isNullOrBlank()) return
    Column(modifier = Modifier.padding(16.dp).testTag("filters-sieve")) {
        Text(text = "Your Sieve script", style = MaterialTheme.typography.titleSmall)
        Text(
            text = "Read-only here. Edit it in the herold suite on a desktop browser.",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Text(
            text = script.orEmpty(),
            style = MaterialTheme.typography.bodySmall,
            fontFamily = FontFamily.Monospace,
            modifier = Modifier.padding(top = 8.dp).testTag("filters-sieve-text"),
        )
    }
}
