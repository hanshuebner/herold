package com.netzhansa.herold.android.ui.thread

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.produceState
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.shared.jmap.WireLlmInspect
import com.netzhansa.herold.shared.llm.TransparencyText

/**
 * "Why is this here?" for one message (suite G7, REQ-FILT-66): what the
 * classifier was asked and what it answered, with the prompt as it was
 * applied to this message. A message the classifier never ran on says so
 * rather than showing an empty form.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun LlmInspectSheet(
    session: SessionScope,
    accountId: String,
    emailId: String,
    onDismiss: () -> Unit,
) {
    val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)
    val detail by produceState<Result<WireLlmInspect?>?>(null, emailId) {
        value = runCatching { session.transparency.inspect(accountId, emailId) }
    }

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        modifier = Modifier.testTag("llm-inspect"),
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp, vertical = 8.dp),
        ) {
            Text(
                text = TransparencyText.INSPECT_TITLE,
                style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.testTag("llm-inspect-title"),
            )
            val loaded = detail
            when {
                loaded == null -> CircularProgressIndicator(
                    modifier = Modifier.padding(16.dp).testTag("llm-inspect-loading"),
                )

                loaded.getOrNull() == null -> Text(
                    text = TransparencyText.NOT_CLASSIFIED,
                    modifier = Modifier.padding(vertical = 12.dp).testTag("llm-inspect-none"),
                )

                else -> {
                    val entry = loaded.getOrNull()!!
                    entry.category?.let { category ->
                        Heading("Category")
                        Field("Assigned", category.assigned, "llm-category-assigned")
                        Field("Model", category.model, "llm-category-model")
                        Field("Classified at", category.classifiedAt, "llm-category-at")
                        Prompt(category.promptApplied, "llm-category-prompt")
                    }
                    entry.spam?.let { spam ->
                        Heading("Spam classification")
                        Field("Verdict", spam.verdict, "llm-spam-verdict")
                        Field("Confidence", TransparencyText.confidence(spam.confidence), "llm-spam-confidence")
                        Field("Reason", spam.reason, "llm-spam-reason")
                        Field("Model", spam.model, "llm-spam-model")
                        Field("Classified at", spam.classifiedAt, "llm-spam-at")
                        Prompt(spam.promptApplied, "llm-spam-prompt")
                    }
                }
            }
        }
    }
}

@Composable
private fun Heading(text: String) {
    Text(
        text = text,
        style = MaterialTheme.typography.titleSmall,
        modifier = Modifier.padding(top = 12.dp, bottom = 4.dp),
    )
}

@Composable
private fun Field(label: String, value: String, tag: String) {
    if (value.isBlank()) return
    Row(modifier = Modifier.fillMaxWidth().padding(vertical = 2.dp)) {
        Text(
            text = label,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(end = 8.dp),
        )
        Text(text = value, style = MaterialTheme.typography.bodySmall, modifier = Modifier.testTag(tag))
    }
}

@Composable
private fun Prompt(text: String, tag: String) {
    if (text.isBlank()) return
    Text(
        text = "Prompt used for this message",
        style = MaterialTheme.typography.bodySmall,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(top = 8.dp),
    )
    Text(
        text = text,
        style = MaterialTheme.typography.bodySmall,
        fontFamily = FontFamily.Monospace,
        modifier = Modifier.padding(bottom = 8.dp).testTag(tag),
    )
}
