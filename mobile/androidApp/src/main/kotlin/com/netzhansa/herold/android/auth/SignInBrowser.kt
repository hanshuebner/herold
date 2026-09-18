package com.netzhansa.herold.android.auth

import android.content.Context
import com.netzhansa.herold.android.links.ExternalBrowser

/**
 * Opens herold's authorization page in a Custom Tab (REQ-AND-AUTH-01).
 * The system browser, not an in-app WebView: that is what lets the
 * platform's password manager and passkey autofill act on the login
 * form, and it keeps the credential out of this app's process
 * altogether (RFC 8252 section 8.12).
 */
object SignInBrowser {

    /** False when the device has no browser able to show the page. */
    fun open(context: Context, url: String): Boolean = ExternalBrowser.customTab(context, url)
}
