package com.netzhansa.herold.android

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import com.netzhansa.herold.shared.createHttpClient
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.launch

/**
 * Phase 0 entry point (docs/design/android/implementation-plan.md
 * acceptance: "the app authenticates against dev-instance and renders the
 * account's mailbox list"). The base-URL/email/password fields are a plain
 * config surface for this phase, prefilled for the dev-instance emulator
 * path (10.0.2.2 reaches the host's loopback from the emulator); the system-
 * browser OAuth2 sign-in (REQ-AND-AUTH-01/02) and a polished login UX are
 * later increments.
 */
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            HeroldApp()
        }
    }
}

private sealed interface SignInState {
    data object Idle : SignInState
    data object Loading : SignInState
    data class Success(val mailboxes: List<Mailbox>) : SignInState
    data class Failure(val message: String) : SignInState
}

@Composable
fun HeroldApp() {
    MaterialTheme {
        Surface(modifier = Modifier.fillMaxSize()) {
            SignInScreen()
        }
    }
}

@Composable
private fun SignInScreen() {
    var baseUrl by remember { mutableStateOf("http://10.0.2.2:8080") }
    var email by remember { mutableStateOf("alice@example.local") }
    var password by remember { mutableStateOf("testpass123...") }
    var state by remember { mutableStateOf<SignInState>(SignInState.Idle) }
    val scope = rememberCoroutineScope()

    Scaffold { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(text = "herold", style = MaterialTheme.typography.headlineMedium)

            OutlinedTextField(
                value = baseUrl,
                onValueChange = { baseUrl = it },
                label = { Text("Base URL") },
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = email,
                onValueChange = { email = it },
                label = { Text("Email") },
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = password,
                onValueChange = { password = it },
                label = { Text("Password") },
                modifier = Modifier.fillMaxWidth(),
            )

            Button(
                onClick = {
                    state = SignInState.Loading
                    scope.launch {
                        state = try {
                            val httpClient = createHttpClient()
                            val tokenStore = InMemoryTokenStore()
                            val client = JmapClient(httpClient, baseUrl, tokenStore)
                            client.signIn(email, password)
                            SignInState.Success(client.fetchMailboxes())
                        } catch (t: Throwable) {
                            SignInState.Failure(t.message ?: "sign-in failed")
                        }
                    }
                },
            ) {
                Text("Sign in")
            }

            when (val current = state) {
                is SignInState.Idle -> Unit
                is SignInState.Loading -> CircularProgressIndicator()
                is SignInState.Failure -> Text(
                    text = "Error: ${current.message}",
                    color = Color.Red,
                )
                is SignInState.Success -> MailboxList(current.mailboxes)
            }
        }
    }
}

@Composable
private fun MailboxList(mailboxes: List<Mailbox>) {
    LazyColumn {
        items(mailboxes) { mailbox ->
            ListItem(
                headlineContent = { Text(mailbox.name) },
                supportingContent = { Text("${mailbox.unreadEmails}/${mailbox.totalEmails} unread") },
            )
        }
    }
}

@Preview(showBackground = true)
@Composable
private fun HeroldAppPreview() {
    HeroldApp()
}
