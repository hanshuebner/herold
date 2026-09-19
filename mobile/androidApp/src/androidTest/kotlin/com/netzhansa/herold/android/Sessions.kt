package com.netzhansa.herold.android

import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import com.netzhansa.herold.shared.auth.SignInResult

/**
 * Puts the app on the dev instance's principal, keeping the session it
 * already holds when that session is the right one.
 *
 * A session outlives the class that opened it: the two-factor checks
 * sign in as the TOTP-enrolled admin, so a class that takes whatever
 * session it finds reads another principal's mailbox and waits out its
 * budget for mail delivered to somebody else (issue #414).
 *
 * The principal comes from the token store rather than from the wire,
 * so the offline phases - which run with the radios off and must keep
 * the session they have - answer it without a round trip.
 */
suspend fun HeroldApplication.signInAsDevInstancePrincipal() {
    val held = container.session.value
    if (held != null) {
        val principal = KeystoreTokenStore(
            InstrumentationRegistry.getInstrumentation().targetContext,
        ).principal()
        if (principal == null || principal.equals(DevInstance.email, ignoreCase = true)) return
        container.signOut()
    }
    val result = container.signInWithPassword(
        DevInstance.baseUrl,
        DevInstance.email,
        DevInstance.password,
        null,
    )
    check(result is SignInResult.Success) { "sign-in as ${DevInstance.email} failed: $result" }
}
