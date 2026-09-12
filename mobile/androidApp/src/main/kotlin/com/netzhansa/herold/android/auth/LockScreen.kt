package com.netzhansa.herold.android.auth

import android.os.Build
import androidx.biometric.BiometricPrompt
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity

/**
 * What the shell shows while the app is locked (REQ-AND-AUTH-11). It
 * covers the mail entirely and, because the token is held back behind
 * the same gate, nothing reaches the network until the prompt
 * succeeds. The prompt is raised as soon as the screen appears; the
 * button is there for a user who dismissed it.
 */
@Composable
fun LockScreen(controller: UnlockController) {
    val activity = LocalContext.current as? FragmentActivity
    var message by remember { mutableStateOf<String?>(null) }
    var attempts by remember { mutableStateOf(0) }

    LaunchedEffect(attempts) {
        val host = activity ?: return@LaunchedEffect
        promptUnlock(
            activity = host,
            onSuccess = {
                message = null
                controller.unlocked()
            },
            onError = { message = it },
        )
    }

    Column(
        modifier = Modifier.fillMaxSize().padding(24.dp).testTag("lock-screen"),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(text = "herold is locked", style = MaterialTheme.typography.headlineSmall)
        Text(
            text = "Unlock to let the app reach your mail.",
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(top = 8.dp),
        )
        Button(
            onClick = { attempts++ },
            modifier = Modifier.padding(top = 24.dp).testTag("lock-unlock"),
        ) {
            Text("Unlock")
        }
        message?.let {
            Text(
                text = it,
                color = MaterialTheme.colorScheme.error,
                modifier = Modifier.padding(top = 16.dp).testTag("lock-error"),
            )
        }
    }
}

/**
 * Raises the platform unlock prompt: a biometric where one is enrolled,
 * the device credential (PIN, pattern, password) otherwise, per
 * platform convention (REQ-AND-AUTH-11).
 */
fun promptUnlock(
    activity: FragmentActivity,
    onSuccess: () -> Unit,
    onError: (String) -> Unit,
) {
    val prompt = BiometricPrompt(
        activity,
        ContextCompat.getMainExecutor(activity),
        object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                onSuccess()
            }

            override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                onError(errString.toString())
            }
        },
    )
    val info = BiometricPrompt.PromptInfo.Builder()
        .setTitle("Unlock herold")
        .setSubtitle("Your mail stays locked until you confirm it is you.")
        .apply {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                setAllowedAuthenticators(UnlockController.authenticators())
            } else {
                // Combining a credential fallback with the authenticator
                // flags is only supported from API 30; below it the
                // dedicated switch is the way to offer it.
                @Suppress("DEPRECATION")
                setDeviceCredentialAllowed(true)
            }
        }
        .build()
    prompt.authenticate(info)
}
