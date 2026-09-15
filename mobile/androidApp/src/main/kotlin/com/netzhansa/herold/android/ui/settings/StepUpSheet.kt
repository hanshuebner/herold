package com.netzhansa.herold.android.ui.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.auth.StepUpCoordinator

/**
 * The six-digit sheet an elevated operation raises (REQ-AND-AUTH-20).
 * The server refuses a self-service operation of a TOTP-enrolled
 * account with `step_up_required` until the credential is elevated
 * (server REQ-AUTH-79); the code entered here elevates the bearer token
 * and the operation that raised the sheet goes out again on its own.
 *
 * A wrong code and a code whose window has passed keep the sheet up
 * with the server's refusal under the field.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun StepUpSheet(coordinator: StepUpCoordinator) {
    val prompt by coordinator.prompt.collectAsStateSafely(null)
    val asked = prompt ?: return
    var code by remember { mutableStateOf("") }

    ModalBottomSheet(
        onDismissRequest = { coordinator.cancel() },
        modifier = Modifier.testTag("stepup-sheet"),
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .imePadding()
                .padding(horizontal = 16.dp)
                .padding(bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(
                text = "Confirm it is you",
                style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.testTag("stepup-title"),
            )
            Text(
                text = if (asked.enrollRequired) {
                    "This account has no authenticator app enrolled. Enrol one in the web app, then try again."
                } else {
                    "Enter the six-digit code from your authenticator app."
                },
                style = MaterialTheme.typography.bodyMedium,
            )
            if (!asked.enrollRequired) {
                OutlinedTextField(
                    value = code,
                    onValueChange = { code = it.filter(Char::isDigit).take(CODE_LENGTH) },
                    label = { Text("Two-factor code") },
                    singleLine = true,
                    enabled = !asked.submitting,
                    keyboardOptions = KeyboardOptions(
                        keyboardType = KeyboardType.NumberPassword,
                        imeAction = ImeAction.Done,
                    ),
                    modifier = Modifier.fillMaxWidth().testTag("stepup-code"),
                )
            }
            asked.error?.let { message ->
                Text(
                    text = message,
                    color = MaterialTheme.colorScheme.error,
                    style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier.testTag("stepup-error"),
                )
            }
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.End,
            ) {
                TextButton(
                    onClick = { coordinator.cancel() },
                    modifier = Modifier.testTag("stepup-cancel"),
                ) {
                    Text("Cancel")
                }
                Button(
                    enabled = !asked.submitting && !asked.enrollRequired && code.length == CODE_LENGTH,
                    onClick = {
                        coordinator.submit(code)
                        code = ""
                    },
                    modifier = Modifier.testTag("stepup-submit"),
                ) {
                    Text("Confirm")
                }
            }
        }
    }
}

/** herold's TOTP codes are six digits (RFC 6238 default). */
private const val CODE_LENGTH = 6
