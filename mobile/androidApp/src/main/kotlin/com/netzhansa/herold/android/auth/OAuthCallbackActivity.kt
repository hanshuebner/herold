package com.netzhansa.herold.android.auth

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import com.netzhansa.herold.android.HeroldApplication
import com.netzhansa.herold.android.MainActivity

/**
 * Receives the redirect herold's authorization endpoint sends the
 * system browser to (`com.netzhansa.herold:/oauth2/callback`), hands
 * the authorization code to the container for the token exchange, and
 * returns to the shell (REQ-AND-AUTH-02).
 *
 * The exchange itself runs on the container's own scope, so it
 * completes even though this activity finishes immediately; the shell
 * renders the exchange's progress and its failure.
 */
class OAuthCallbackActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        deliver(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        deliver(intent)
    }

    private fun deliver(intent: Intent?) {
        val container = (application as HeroldApplication).container
        intent?.data?.toString()?.let(container::completeSignIn)
        // NEW_TASK sends this at the app's own task rather than the
        // one this receiver runs in, where CLEAR_TOP then finds the
        // shell's existing activity and resumes it instead of stacking
        // a second copy on top of the browser.
        startActivity(
            Intent(this, MainActivity::class.java).addFlags(
                Intent.FLAG_ACTIVITY_NEW_TASK or
                    Intent.FLAG_ACTIVITY_CLEAR_TOP or
                    Intent.FLAG_ACTIVITY_SINGLE_TOP,
            ),
        )
        finish()
    }
}
