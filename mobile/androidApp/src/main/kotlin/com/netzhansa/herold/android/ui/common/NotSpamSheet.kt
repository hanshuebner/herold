package com.netzhansa.herold.android.ui.common

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.shared.actions.NeverSpam
import com.netzhansa.herold.shared.actions.NeverSpamScope

/**
 * What "Not spam" asks before it acts (issue #506, suite
 * `NotSpamDialog.svelte`): the message goes back to the inbox either
 * way, and the sheet offers to keep the sender out of Junk for good
 * with a never-spam rule on the exact address or on its whole domain
 * (REQ-FLT-16).
 *
 * [onConfirm] is handed the scope the reader chose, or null when they
 * asked for the move alone.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NotSpamSheet(
    senderEmail: String,
    onDismiss: () -> Unit,
    onConfirm: (NeverSpamScope?) -> Unit,
) {
    val domain = remember(senderEmail) { NeverSpam.senderDomain(senderEmail) }
    var scope by remember { mutableStateOf<NeverSpamScope?>(null) }
    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
        modifier = Modifier.testTag("not-spam-sheet"),
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .bottomSystemBarsPadding()
                .padding(bottom = 24.dp),
        ) {
            Text(
                text = "Not spam",
                style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
            )
            Text(
                text = "This message goes back to your inbox and herold is told its verdict was wrong.",
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
            )
            ScopeRow(
                label = "Just this message",
                selected = scope == null,
                tag = "not-spam-scope-none",
                onSelect = { scope = null },
            )
            if (senderEmail.isNotBlank()) {
                ScopeRow(
                    label = "Never mark mail from $senderEmail as spam",
                    selected = scope == NeverSpamScope.ADDRESS,
                    tag = "not-spam-scope-address",
                    onSelect = { scope = NeverSpamScope.ADDRESS },
                )
            }
            if (domain.isNotBlank()) {
                ScopeRow(
                    label = "Never mark mail from anyone at $domain as spam",
                    selected = scope == NeverSpamScope.DOMAIN,
                    tag = "not-spam-scope-domain",
                    onSelect = { scope = NeverSpamScope.DOMAIN },
                )
            }
            Row(
                modifier = Modifier.fillMaxWidth().padding(horizontal = 8.dp),
                horizontalArrangement = Arrangement.End,
            ) {
                TextButton(onClick = onDismiss, modifier = Modifier.testTag("not-spam-cancel")) {
                    Text("Cancel")
                }
                TextButton(
                    onClick = { onConfirm(scope) },
                    modifier = Modifier.testTag("not-spam-confirm"),
                ) {
                    Text("Move to Inbox")
                }
            }
        }
    }
}

@Composable
private fun ScopeRow(label: String, selected: Boolean, tag: String, onSelect: () -> Unit) {
    ListItem(
        headlineContent = { Text(label) },
        leadingContent = { RadioButton(selected = selected, onClick = null) },
        modifier = Modifier.testTag(tag).clickable(onClick = onSelect),
    )
}
