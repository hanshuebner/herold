package com.netzhansa.herold.android.ui.signin

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.DEFAULT_BASE_URL
import com.netzhansa.herold.shared.auth.SignInFailure
import com.netzhansa.herold.shared.auth.SignInResult
import kotlinx.coroutines.launch

/**
 * Sign-in through the device-token grant: email, password, and the TOTP code
 * when the principal has one enrolled (REQ-AND-AUTH-01, server #199). The
 * base URL defaults to the production host and stays editable so a dev
 * instance can be reached from the emulator.
 */
@Composable
fun SignInScreen(container: AppContainer) {
    var baseUrl by rememberSaveable { mutableStateOf(DEFAULT_BASE_URL) }
    var email by rememberSaveable { mutableStateOf("") }
    var password by rememberSaveable { mutableStateOf("") }
    var totp by rememberSaveable { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var totpRequired by rememberSaveable { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    Scaffold { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .imePadding()
                .verticalScroll(rememberScrollState())
                .padding(24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(text = "herold", style = MaterialTheme.typography.headlineMedium)

            OutlinedTextField(
                value = baseUrl,
                onValueChange = { baseUrl = it },
                label = { Text("Server") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                modifier = Modifier.fillMaxWidth().testTag("signin-base-url"),
            )
            OutlinedTextField(
                value = email,
                onValueChange = { email = it },
                label = { Text("Email") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Email),
                modifier = Modifier.fillMaxWidth().testTag("signin-email"),
            )
            OutlinedTextField(
                value = password,
                onValueChange = { password = it },
                label = { Text("Password") },
                singleLine = true,
                visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, imeAction = ImeAction.Done),
                modifier = Modifier.fillMaxWidth().testTag("signin-password"),
            )
            OutlinedTextField(
                value = totp,
                onValueChange = { totp = it.filter(Char::isDigit).take(6) },
                label = { Text(if (totpRequired) "Two-factor code (required)" else "Two-factor code (if enabled)") },
                singleLine = true,
                isError = totpRequired,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.NumberPassword, imeAction = ImeAction.Done),
                modifier = Modifier.fillMaxWidth().testTag("signin-totp"),
            )

            Button(
                enabled = !busy && email.isNotBlank() && password.isNotBlank(),
                onClick = {
                    busy = true
                    error = null
                    scope.launch {
                        when (val result = container.signIn(baseUrl, email, password, totp)) {
                            is SignInResult.Success -> Unit
                            is SignInResult.Failure -> {
                                totpRequired = result.reason is SignInFailure.TotpRequired
                                error = when (val reason = result.reason) {
                                    is SignInFailure.TotpRequired -> reason.message
                                    is SignInFailure.Rejected -> reason.message
                                    is SignInFailure.Transport -> reason.message
                                }
                            }
                        }
                        busy = false
                    }
                },
                modifier = Modifier.testTag("signin-submit"),
            ) {
                Text("Sign in")
            }

            if (busy) CircularProgressIndicator()

            error?.let {
                Text(
                    text = it,
                    color = MaterialTheme.colorScheme.error,
                    modifier = Modifier.testTag("signin-error"),
                )
            }
        }
    }
}
