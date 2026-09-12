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
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.BuildConfig
import com.netzhansa.herold.android.DEFAULT_BASE_URL
import com.netzhansa.herold.android.SignInState
import com.netzhansa.herold.android.auth.SignInBrowser
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.auth.SignInFailure
import com.netzhansa.herold.shared.auth.SignInResult
import kotlinx.coroutines.launch

/**
 * Sign-in through herold's OAuth2 authorization-code grant with PKCE
 * (REQ-AND-AUTH-01/02): the server field and one button, which opens
 * herold's own login page in a Custom Tab. Password, TOTP and any
 * federated provider are the server's page, in the system browser,
 * where the platform's password manager and passkey autofill reach
 * them. This app never sees the password.
 *
 * A debug build additionally offers the device-token grant, which is
 * how the instrumented harness signs in without driving a browser.
 */
@Composable
fun SignInScreen(container: AppContainer) {
    val context = LocalContext.current
    var baseUrl by rememberSaveable { mutableStateOf(DEFAULT_BASE_URL) }
    val state by container.signInState.collectAsStateSafely(SignInState.Idle)
    var launching by remember { mutableStateOf(false) }
    var noBrowser by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    val busy = launching || state is SignInState.AwaitingBrowser || state is SignInState.Exchanging

    // The user came back from the browser without authorising: the
    // request is dropped so the next attempt mints a fresh one.
    val lifecycleOwner = LocalLifecycleOwner.current
    LaunchedEffect(lifecycleOwner) {
        lifecycleOwner.lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            if (container.signInState.value is SignInState.AwaitingBrowser) {
                container.abandonSignIn()
            }
        }
    }

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
            Text(
                text = "Sign in on your herold server. The page opens in your browser, " +
                    "so your saved password and passkeys are available.",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            OutlinedTextField(
                value = baseUrl,
                onValueChange = { baseUrl = it },
                label = { Text("Server") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                modifier = Modifier.fillMaxWidth().testTag("signin-base-url"),
            )

            Button(
                enabled = !busy && baseUrl.isNotBlank(),
                onClick = {
                    launching = true
                    noBrowser = false
                    container.clearSignInError()
                    scope.launch {
                        val url = container.beginSignIn(baseUrl)
                        if (!SignInBrowser.open(context, url)) {
                            noBrowser = true
                            container.abandonSignIn()
                        }
                        launching = false
                    }
                },
                modifier = Modifier.testTag("signin-submit"),
            ) {
                Text("Sign in")
            }

            if (busy) CircularProgressIndicator(modifier = Modifier.testTag("signin-busy"))

            (state as? SignInState.Failed)?.let { failure ->
                Text(
                    text = failure.message,
                    color = MaterialTheme.colorScheme.error,
                    modifier = Modifier.testTag("signin-error"),
                )
            }

            if (noBrowser) {
                Text(
                    text = "No browser on this device can show the sign-in page.",
                    color = MaterialTheme.colorScheme.error,
                    modifier = Modifier.testTag("signin-no-browser"),
                )
            }

            if (BuildConfig.DEBUG) {
                PasswordFallback(container = container, baseUrl = baseUrl)
            }
        }
    }
}

/**
 * The device-token grant, kept in debug builds only: it is what the
 * instrumented acceptance suite signs in with when the flow under test
 * is not sign-in itself, and a way in on a device with no browser.
 */
@Composable
private fun PasswordFallback(container: AppContainer, baseUrl: String) {
    var expanded by rememberSaveable { mutableStateOf(false) }
    var email by rememberSaveable { mutableStateOf("") }
    var password by rememberSaveable { mutableStateOf("") }
    var totp by rememberSaveable { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var totpRequired by rememberSaveable { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    TextButton(
        onClick = { expanded = !expanded },
        modifier = Modifier.testTag("signin-password-toggle"),
    ) {
        Text(if (expanded) "Hide password sign-in (debug)" else "Password sign-in (debug)")
    }

    if (!expanded) return

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
    if (totpRequired) {
        // The server asked for the second factor: a single six-digit
        // field, no password re-entry (REQ-AND-AUTH-20).
        OutlinedTextField(
            value = totp,
            onValueChange = { totp = it.filter(Char::isDigit).take(6) },
            label = { Text("Two-factor code") },
            singleLine = true,
            keyboardOptions = KeyboardOptions(
                keyboardType = KeyboardType.NumberPassword,
                imeAction = ImeAction.Done,
            ),
            modifier = Modifier.fillMaxWidth().testTag("signin-totp"),
        )
    }

    Button(
        enabled = !busy && email.isNotBlank() && password.isNotBlank(),
        onClick = {
            busy = true
            error = null
            scope.launch {
                val result = container.signInWithPassword(baseUrl, email, password, totp)
                if (result is SignInResult.Failure) {
                    totpRequired = totpRequired || result.reason is SignInFailure.TotpRequired
                    error = when (val reason = result.reason) {
                        is SignInFailure.TotpRequired -> reason.message
                        is SignInFailure.Rejected -> reason.message
                        is SignInFailure.Transport -> reason.message
                    }
                }
                busy = false
            }
        },
        modifier = Modifier.testTag("signin-password-submit"),
    ) {
        Text("Sign in with password")
    }

    error?.let {
        Text(
            text = it,
            color = MaterialTheme.colorScheme.error,
            modifier = Modifier.testTag("signin-password-error"),
        )
    }
}
