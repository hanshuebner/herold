package com.netzhansa.herold.android.ui.filters

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.toMutableStateList
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.actions.FilterActions
import com.netzhansa.herold.shared.domain.RuleAction
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleCondition
import com.netzhansa.herold.shared.domain.RuleFields
import com.netzhansa.herold.shared.domain.RuleOps
import com.netzhansa.herold.shared.filters.RuleText
import kotlinx.coroutines.launch

/**
 * The structured filter editor (suite REQ-FLT-30): conditions and actions
 * are chosen from the server's closed vocabularies and never typed as
 * Sieve. Opened empty from the filter list, on an existing rule from a
 * row, or seeded from a message by the thread view's "Create filter from
 * this message" (REQ-FLT-32).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FilterEditorScreen(
    container: AppContainer,
    session: SessionScope,
    accountId: String,
    ruleId: String?,
    seedFrom: String?,
    seedSubject: String?,
    onClose: () -> Unit,
) {
    val all by container.store.managedRules().collectAsStateSafely(emptyList())
    val existing = remember(all, ruleId) { all.firstOrNull { it.id == ruleId && it.accountId == accountId } }
    val labels by container.store.mailboxes().collectAsStateSafely(emptyList())
    val scope = rememberCoroutineScope()

    var name by remember(existing?.id) { mutableStateOf(existing?.name.orEmpty()) }
    val conditions = remember(existing?.id, seedFrom, seedSubject) {
        val seeded = when {
            existing != null -> existing.conditions
            seedFrom != null || seedSubject != null ->
                FilterActions.seedConditions(seedFrom.orEmpty(), seedSubject.orEmpty())

            else -> listOf(RuleCondition(RuleFields.FROM, RuleOps.CONTAINS, ""))
        }
        seeded.toMutableStateList()
    }
    val actions = remember(existing?.id) {
        (existing?.actions ?: listOf(RuleAction(RuleActions.SKIP_INBOX))).toMutableStateList()
    }
    var error by remember { mutableStateOf<String?>(null) }

    fun save() {
        val problem = RuleText.validate(conditions.toList(), actions.toList())
        if (problem != null) {
            error = problem
            return
        }
        scope.launch {
            if (existing != null) {
                session.filters.update(existing, name, conditions.toList(), actions.toList())
            } else {
                val order = all.filter { it.accountId == accountId }.maxOfOrNull { it.order + 1 } ?: 0
                session.filters.create(accountId, name, conditions.toList(), actions.toList(), order)
            }
            onClose()
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onClose, modifier = Modifier.testTag("filter-editor-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = {
                    Text(
                        text = if (existing != null) "Edit filter" else "New filter",
                        modifier = Modifier.testTag("filter-editor-title"),
                    )
                },
                actions = {
                    TextButton(onClick = { save() }, modifier = Modifier.testTag("filter-save")) {
                        Text("Save")
                    }
                },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .testTag("filter-editor"),
        ) {
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("Name (optional)") },
                singleLine = true,
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 8.dp)
                    .testTag("filter-name"),
            )

            SectionHeading("If all of these match")
            conditions.forEachIndexed { index, condition ->
                ConditionRow(
                    index = index,
                    condition = condition,
                    removable = conditions.size > 1,
                    onChange = { conditions[index] = it },
                    onRemove = { conditions.removeAt(index) },
                )
            }
            TextButton(
                onClick = { conditions.add(RuleCondition(RuleFields.FROM, RuleOps.CONTAINS, "")) },
                modifier = Modifier.padding(horizontal = 8.dp).testTag("filter-add-condition"),
            ) { Text("Add condition") }

            SectionHeading("Then")
            actions.forEachIndexed { index, action ->
                ActionRow(
                    index = index,
                    action = action,
                    labelNames = labels.filter { it.accountId == accountId && it.role == null }.map { it.name },
                    removable = actions.size > 1,
                    onChange = { actions[index] = it },
                    onRemove = { actions.removeAt(index) },
                )
            }
            TextButton(
                onClick = { actions.add(RuleAction(RuleActions.MARK_READ)) },
                modifier = Modifier.padding(horizontal = 8.dp).testTag("filter-add-action"),
            ) { Text("Add action") }

            error?.let { message ->
                Text(
                    text = message,
                    color = MaterialTheme.colorScheme.error,
                    modifier = Modifier.padding(16.dp).testTag("filter-error"),
                )
            }
        }
    }
}

@Composable
private fun SectionHeading(text: String) {
    Text(
        text = text,
        style = MaterialTheme.typography.titleSmall,
        modifier = Modifier.padding(start = 16.dp, top = 16.dp, bottom = 4.dp),
    )
}

@Composable
private fun ConditionRow(
    index: Int,
    condition: RuleCondition,
    removable: Boolean,
    onChange: (RuleCondition) -> Unit,
    onRemove: () -> Unit,
) {
    Column(modifier = Modifier.padding(horizontal = 16.dp).testTag("condition-$index")) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(4.dp),
        ) {
            Picker(
                testTag = "condition-field-$index",
                current = RuleText.fieldLabel(condition.field),
                options = RuleFields.EDITABLE.map { it to RuleText.fieldLabel(it) },
                onPick = { onChange(condition.copy(field = it)) },
            )
            if (condition.field != RuleFields.HAS_ATTACHMENT) {
                Picker(
                    testTag = "condition-op-$index",
                    current = RuleText.opLabel(condition.op),
                    options = RuleOps.EDITABLE.map { it to RuleText.opLabel(it) },
                    onPick = { onChange(condition.copy(op = it)) },
                )
            }
            if (removable) {
                IconButton(onClick = onRemove, modifier = Modifier.testTag("condition-remove-$index")) {
                    Icon(Icons.Filled.Close, contentDescription = "Remove condition")
                }
            }
        }
        if (condition.field != RuleFields.HAS_ATTACHMENT) {
            OutlinedTextField(
                value = condition.value,
                onValueChange = { onChange(condition.copy(value = it)) },
                singleLine = true,
                label = { Text("Value") },
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(bottom = 8.dp)
                    .testTag("condition-value-$index"),
            )
        }
    }
}

@Composable
private fun ActionRow(
    index: Int,
    action: RuleAction,
    labelNames: List<String>,
    removable: Boolean,
    onChange: (RuleAction) -> Unit,
    onRemove: () -> Unit,
) {
    val paramName = RuleText.actionParamName(action.kind)
    Column(modifier = Modifier.padding(horizontal = 16.dp).testTag("action-$index")) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(4.dp),
        ) {
            Picker(
                testTag = "action-kind-$index",
                current = RuleText.actionLabel(action.kind),
                options = RuleActions.EDITABLE.map { it to RuleText.actionLabel(it) },
                onPick = { kind -> onChange(RuleAction(kind)) },
            )
            if (removable) {
                IconButton(onClick = onRemove, modifier = Modifier.testTag("action-remove-$index")) {
                    Icon(Icons.Filled.Close, contentDescription = "Remove action")
                }
            }
        }
        if (RuleText.actionTakesValue(action.kind)) {
            OutlinedTextField(
                value = action.params[paramName].orEmpty(),
                onValueChange = { onChange(action.copy(params = mapOf(paramName to it))) },
                singleLine = true,
                label = {
                    Text(if (action.kind == RuleActions.APPLY_LABEL) "Label" else "Address")
                },
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(bottom = 8.dp)
                    .testTag("action-value-$index"),
            )
            if (action.kind == RuleActions.APPLY_LABEL && labelNames.isNotEmpty()) {
                Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    labelNames.take(MAX_LABEL_SUGGESTIONS).forEach { label ->
                        TextButton(
                            onClick = { onChange(action.copy(params = mapOf(paramName to label))) },
                            modifier = Modifier.testTag("action-label-$label"),
                        ) { Text(label) }
                    }
                }
            }
        }
    }
}

/** A one-of chooser over a closed vocabulary, stating the current choice. */
@Composable
private fun Picker(
    testTag: String,
    current: String,
    options: List<Pair<String, String>>,
    onPick: (String) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    Box {
        TextButton(onClick = { open = true }, modifier = Modifier.testTag(testTag)) {
            Text(current)
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            options.forEach { (value, label) ->
                DropdownMenuItem(
                    text = { Text(label) },
                    onClick = {
                        open = false
                        onPick(value)
                    },
                    modifier = Modifier.testTag("$testTag-$value"),
                )
            }
        }
    }
}

/** How many of the account's labels the editor offers as one-tap chips. */
private const val MAX_LABEL_SUGGESTIONS = 4
