package com.netzhansa.herold.android.ui.search

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.search.SearchHit
import com.netzhansa.herold.shared.search.SearchResults
import com.netzhansa.herold.shared.search.SearchScope
import com.netzhansa.herold.shared.search.splitSnippet
import kotlinx.coroutines.launch

/**
 * Search results (suite `07-search.md`). Online this is `Email/query` over
 * every account in scope with the server's highlighted snippets; without
 * connectivity the same field answers from the local store and says so
 * (REQ-AND-SYNC-13).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SearchScreen(
    container: AppContainer,
    session: SessionScope,
    accountScope: String?,
    onOpenThread: (accountId: String, threadId: String) -> Unit,
    onBack: () -> Unit,
) {
    val accounts by container.store.accounts().collectAsStateSafely(emptyList())
    val mailboxes by container.store.mailboxes().collectAsStateSafely(emptyList())
    val scope = rememberCoroutineScope()
    val focus = remember { FocusRequester() }

    // The query, the results and the scroll position live in a holder
    // scoped to this destination's back-stack entry, so opening a result
    // and coming back shows the search as it was left (issue #340).
    val model: SearchViewModel = viewModel()

    LaunchedEffect(Unit) {
        if (!model.everFocused) {
            model.everFocused = true
            runCatching { focus.requestFocus() }
        }
    }

    fun run() {
        if (model.query.isBlank()) return
        model.running = true
        scope.launch {
            model.results = session.search.search(model.query, accounts, mailboxes, accountScope)
            model.running = false
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("search-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = {
                    TextField(
                        value = model.query,
                        onValueChange = { model.query = it },
                        placeholder = { Text("Search mail") },
                        singleLine = true,
                        keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                        keyboardActions = KeyboardActions(onSearch = { run() }),
                        trailingIcon = {
                            if (model.query.isNotEmpty()) {
                                IconButton(
                                    onClick = { model.query = ""; model.results = null },
                                    modifier = Modifier.testTag("search-clear"),
                                ) {
                                    Icon(Icons.Filled.Close, contentDescription = "Clear")
                                }
                            }
                        },
                        modifier = Modifier
                            .fillMaxWidth()
                            .focusRequester(focus)
                            .testTag("search-field"),
                    )
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding)) {
            if (model.running) {
                LinearProgressIndicator(modifier = Modifier.fillMaxWidth().testTag("search-running"))
            }
            val current = model.results
            if (current != null) {
                if (current.scope == SearchScope.CACHED) {
                    Text(
                        text = current.message ?: "Cached results only",
                        style = MaterialTheme.typography.labelMedium,
                        color = MaterialTheme.colorScheme.error,
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(horizontal = 16.dp, vertical = 8.dp)
                            .testTag("search-cached-banner"),
                    )
                }
                Text(
                    text = "${current.hits.size} result${if (current.hits.size == 1) "" else "s"}",
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp).testTag("search-count"),
                )
                LazyColumn(
                    state = model.listState,
                    modifier = Modifier.fillMaxSize().testTag("search-results"),
                ) {
                    items(current.hits, key = { it.row.accountId + ":" + it.row.threadId }) { hit ->
                        SearchRow(hit = hit, onOpen = { onOpenThread(hit.row.accountId, hit.row.threadId) })
                        HorizontalDivider()
                    }
                }
            }
        }
    }
}

@Composable
private fun SearchRow(hit: SearchHit, onOpen: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onOpen)
            .padding(horizontal = 16.dp, vertical = 10.dp)
            .testTag("search-row-${hit.row.threadId}"),
        verticalArrangement = Arrangement.spacedBy(2.dp),
    ) {
        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(
                text = hit.row.senders.ifBlank { "(unknown sender)" },
                style = MaterialTheme.typography.titleSmall,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
        Text(
            text = highlighted(hit.subjectSnippet, hit.row.subject),
            style = MaterialTheme.typography.bodyMedium,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.testTag("search-subject-${hit.row.threadId}"),
        )
        Text(
            text = highlighted(hit.previewSnippet, hit.row.preview),
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.testTag("search-snippet-${hit.row.threadId}"),
        )
    }
}

/**
 * The server's snippet with its `<mark>` runs emphasised; without a
 * snippet the row falls back to the message's own text (REQ-SRC-32).
 */
@Composable
private fun highlighted(snippet: String?, fallback: String) = buildAnnotatedString {
    if (snippet == null) {
        append(fallback)
        return@buildAnnotatedString
    }
    splitSnippet(snippet).forEach { (text, marked) ->
        if (marked) {
            withStyle(
                SpanStyle(
                    fontWeight = FontWeight.Bold,
                    background = MaterialTheme.colorScheme.tertiaryContainer,
                ),
            ) {
                append(text)
            }
        } else {
            append(text)
        }
    }
}

/**
 * What the search screen holds across a trip into a thread. Navigation
 * Compose disposes a destination's composition while it stays on the back
 * stack, so plain `remember` state is gone by the time back returns; a
 * ViewModel resolved inside the destination is scoped to its back-stack
 * entry and is cleared when the entry is popped (REQ-AND-NAV-01).
 */
class SearchViewModel : ViewModel() {
    var query by mutableStateOf("")
    var running by mutableStateOf(false)
    var results by mutableStateOf<SearchResults?>(null)

    /** The result list's scroll offset, restored with the results. */
    val listState = LazyListState()

    /** True once the field has taken focus, so a return does not re-open the keyboard. */
    var everFocused = false
}
