package com.netzhansa.herold.android.links

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.browser.customtabs.CustomTabsIntent

/**
 * Where a URL the app does not render itself goes: a Custom Tab, or the
 * activity the system registered for the scheme.
 *
 * One path for every such URL - a link in a message body (issue #425),
 * an unsubscribe page (REQ-UNS-10), the authorization page sign-in runs
 * (REQ-AND-AUTH-01) - so the page always opens in the browser, with the
 * app's own surface left standing behind it.
 */
object ExternalBrowser {

    /**
     * Shows [url] in a Custom Tab, or hands it to whatever the system
     * registered when no browser supports one. False when nothing on the
     * device can show it, in which case the caller stays as it is.
     */
    fun open(context: Context, url: String): Boolean =
        customTab(context, url) || view(context, url)

    /**
     * A Custom Tab over the app: the page keeps the browser's cookie jar,
     * its password manager and its address bar, and the app process never
     * sees the page.
     */
    fun customTab(context: Context, url: String): Boolean {
        val intent = CustomTabsIntent.Builder()
            .setShowTitle(true)
            .setUrlBarHidingEnabled(false)
            .build()
        intent.intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        return try {
            intent.launchUrl(context, Uri.parse(url))
            true
        } catch (missing: ActivityNotFoundException) {
            false
        }
    }

    /** `ACTION_VIEW` for a URI another app owns: `tel:`, `sms:`, `geo:`. */
    fun view(context: Context, uri: String): Boolean = try {
        context.startActivity(
            Intent(Intent.ACTION_VIEW, Uri.parse(uri)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
        )
        true
    } catch (missing: ActivityNotFoundException) {
        false
    }
}
