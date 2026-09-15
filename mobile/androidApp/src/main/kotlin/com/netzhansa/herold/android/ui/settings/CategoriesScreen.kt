package com.netzhansa.herold.android.ui.settings

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.DragHandle
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.zIndex
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.actions.CategoryActions
import com.netzhansa.herold.shared.domain.CategoryDisposition
import com.netzhansa.herold.shared.domain.Mailbox
import kotlinx.coroutines.launch
import kotlin.math.roundToInt

/** The height of one reorderable row, which the drag converts into ranks. */
private val ROW_HEIGHT = 56.dp

/**
 * Category settings (suite REQ-CAT-04/05/11): a label's disposition and
 * the order of the pinned ones.
 *
 * The server owns both properties, so the screen renders the local
 * store's mailbox rows and writes `Mailbox/set` through the outbox. A
 * refusal - the sixth pinned category the server answers `tooManyPinned`
 * to - arrives from the drain and is shown here.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CategoriesScreen(
    container: AppContainer,
    session: SessionScope,
    accountId: String,
    onBack: () -> Unit,
) {
    val mailboxes by container.store.mailboxes().collectAsStateSafely(emptyList())
    val labels = remember(mailboxes, accountId) { CategoryActions.categoryLabels(mailboxes, accountId) }
    val pinned = remember(mailboxes, accountId) { CategoryActions.pinnedLabels(mailboxes, accountId) }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()

    LaunchedEffect(session) {
        session.drainer.failures.collect { failure -> snackbar.showSnackbar(failure.message) }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("categories-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = { Text("Categories", modifier = Modifier.testTag("categories-title")) },
            )
        },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .testTag("categories-screen"),
        ) {
            Text(
                text = "How a label appears in the inbox. A pinned category is a tab, " +
                    "a bundled one collapses to a single row, and a filed or deferred one " +
                    "stays out of the stream.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(16.dp),
            )

            if (pinned.isNotEmpty()) {
                Text(
                    text = "Tab order",
                    style = MaterialTheme.typography.titleSmall,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                )
                PinnedOrder(
                    pinned = pinned,
                    onMove = { from, to -> scope.launch { session.categories.reorderPinned(pinned, from, to) } },
                )
            }

            Text(
                text = "Labels",
                style = MaterialTheme.typography.titleSmall,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
            )
            if (labels.isEmpty()) {
                Text(
                    text = "This account has no labels yet.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 16.dp).testTag("categories-empty"),
                )
            }
            labels.forEach { label ->
                DispositionRow(
                    label = label,
                    onChoose = { disposition ->
                        scope.launch { session.categories.setDisposition(label, disposition, mailboxes) }
                    },
                )
                HorizontalDivider()
            }
        }
    }
}

/**
 * The pinned categories in tab order. The handle drags a row; where it
 * is let go decides its new rank, and the write renumbers the rest
 * (REQ-CAT-05).
 */
@Composable
private fun PinnedOrder(pinned: List<Mailbox>, onMove: (Int, Int) -> Unit) {
    val rowPx = with(LocalDensity.current) { ROW_HEIGHT.toPx() }
    var dragging by remember(pinned) { mutableStateOf<Int?>(null) }
    var dragOffset by remember(pinned) { mutableFloatStateOf(0f) }

    Column(modifier = Modifier.fillMaxWidth().testTag("category-order")) {
        pinned.forEachIndexed { index, label ->
            val key = label.categoryName
            Surface(
                tonalElevation = if (dragging == index) 4.dp else 0.dp,
                modifier = Modifier
                    .fillMaxWidth()
                    .height(ROW_HEIGHT)
                    .zIndex(if (dragging == index) 1f else 0f)
                    .offset { IntOffset(0, if (dragging == index) dragOffset.roundToInt() else 0) },
            ) {
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 16.dp)
                        .testTag("category-pinned-$key"),
                ) {
                    Text(text = "${index + 1}.", modifier = Modifier.padding(end = 12.dp))
                    Text(text = label.name, modifier = Modifier.weight(1f))
                    Icon(
                        imageVector = Icons.Filled.DragHandle,
                        contentDescription = "Reorder ${label.name}",
                        modifier = Modifier
                            .testTag("category-drag-$key")
                            .pointerInput(pinned, index) {
                                detectDragGestures(
                                    onDragStart = {
                                        dragging = index
                                        dragOffset = 0f
                                    },
                                    onDragEnd = {
                                        val target = (index + (dragOffset / rowPx).roundToInt())
                                            .coerceIn(0, pinned.lastIndex)
                                        dragging = null
                                        dragOffset = 0f
                                        if (target != index) onMove(index, target)
                                    },
                                    onDragCancel = {
                                        dragging = null
                                        dragOffset = 0f
                                    },
                                    onDrag = { change, amount ->
                                        change.consume()
                                        dragOffset += amount.y
                                    },
                                )
                            },
                    )
                }
            }
        }
    }
}

/** One label with the disposition menu behind it. */
@Composable
private fun DispositionRow(label: Mailbox, onChoose: (CategoryDisposition) -> Unit) {
    var open by remember { mutableStateOf(false) }
    val key = label.categoryName

    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier
            .fillMaxWidth()
            .clickable { open = true }
            .padding(horizontal = 16.dp, vertical = 14.dp)
            .testTag("category-row-$key"),
    ) {
        Text(text = label.name, modifier = Modifier.weight(1f))
        Text(
            text = label.disposition.label,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.testTag("category-disposition-$key"),
        )
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            CategoryDisposition.entries.forEach { option ->
                DropdownMenuItem(
                    text = { Text(option.label) },
                    onClick = {
                        open = false
                        onChoose(option)
                    },
                    modifier = Modifier.testTag("category-choice-${option.wire}"),
                )
            }
        }
    }
}
