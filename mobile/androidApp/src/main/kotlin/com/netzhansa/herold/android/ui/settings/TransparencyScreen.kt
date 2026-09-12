package com.netzhansa.herold.android.ui.settings

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.produceState
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.shared.jmap.WireLlmTransparency
import com.netzhansa.herold.shared.llm.TransparencyText

/**
 * "How herold sorts your mail" (suite G7, REQ-FILT-65/67/68): the prompts
 * in effect, the categories they produced, the models they run on, and
 * the server's own disclosure note, rendered verbatim. Operator
 * guardrails are not part of what the server returns and are not shown.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun TransparencyScreen(
    session: SessionScope,
    accountId: String,
    onBack: () -> Unit,
) {
    val data by produceState<WireLlmTransparency?>(null, accountId) {
        value = session.transparency.overview(accountId)
    }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("transparency-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = {
                    Text(TransparencyText.TITLE, modifier = Modifier.testTag("transparency-title"))
                },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .testTag("transparency-screen"),
        ) {
            val row = data
            if (row == null) {
                Text(
                    text = TransparencyText.UNAVAILABLE,
                    modifier = Modifier.padding(16.dp).testTag("transparency-unavailable"),
                )
                return@Column
            }

            Surface(
                color = MaterialTheme.colorScheme.secondaryContainer,
                modifier = Modifier.fillMaxWidth().padding(16.dp),
            ) {
                Text(
                    text = row.disclosureNote,
                    style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier.padding(12.dp).testTag("transparency-disclosure"),
                )
            }

            Section(
                heading = "Categorisation prompt",
                body = row.categoriserPrompt.ifBlank { "This server uses its built-in prompt." },
                tag = "transparency-category-prompt",
                monospace = row.categoriserPrompt.isNotBlank(),
            )
            Section(
                heading = "Categories in use",
                body = row.derivedCategories.joinToString(", ").ifBlank {
                    "No categories yet: the classifier has not answered since the prompt last changed."
                },
                tag = "transparency-categories",
            )
            Section(
                heading = "Categorisation model",
                body = row.categoriserModel.display.ifBlank { "Not reported." },
                tag = "transparency-category-model",
            )
            Section(
                heading = "Spam prompt",
                body = row.spamPrompt.ifBlank { "This server uses its built-in spam prompt." },
                tag = "transparency-spam-prompt",
                monospace = row.spamPrompt.isNotBlank(),
            )
            Section(
                heading = "Spam model",
                body = row.spamModel.display.ifBlank { "Not reported." },
                tag = "transparency-spam-model",
            )
        }
    }
}

@Composable
private fun Section(heading: String, body: String, tag: String, monospace: Boolean = false) {
    Text(
        text = heading,
        style = MaterialTheme.typography.titleSmall,
        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 12.dp, bottom = 4.dp),
    )
    Text(
        text = body,
        style = MaterialTheme.typography.bodySmall,
        fontFamily = if (monospace) FontFamily.Monospace else FontFamily.Default,
        modifier = Modifier.padding(horizontal = 16.dp).testTag(tag),
    )
}
