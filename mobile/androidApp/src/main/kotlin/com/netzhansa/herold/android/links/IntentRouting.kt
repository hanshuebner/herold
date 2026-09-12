package com.netzhansa.herold.android.links

import android.content.Intent
import android.net.Uri
import android.os.Build
import com.netzhansa.herold.android.push.MailNotifier
import com.netzhansa.herold.shared.links.AppDestination
import com.netzhansa.herold.shared.links.AppLinks
import com.netzhansa.herold.shared.links.ComposePrefill
import com.netzhansa.herold.shared.links.Mailto

/**
 * What an incoming intent asks the shell to open: a destination and, for
 * a share, the files that came with it (REQ-AND-SYS-01/03/10/11).
 */
data class LaunchRequest(
    val destination: AppDestination,
    /** Files a share handed over, attached once the composer is up. */
    val attachments: List<Uri> = emptyList(),
)

/**
 * Turns an `Intent` into a [LaunchRequest].
 *
 * Four kinds of intent reach the shell: a notification's tap or Reply
 * target, a share from another app, a `mailto:` link, and a deep link -
 * the internal `herold://` scheme or a verified App Link to the
 * deployment origin. All of them resolve to the same destinations, so the
 * shell has one routing path rather than one per entry point.
 */
object IntentRouting {

    fun resolve(intent: Intent?): LaunchRequest? {
        if (intent == null) return null
        return when (intent.action) {
            Intent.ACTION_SEND, Intent.ACTION_SEND_MULTIPLE -> share(intent)
            Intent.ACTION_SENDTO -> intent.data?.let { mailto(it) }
            Intent.ACTION_VIEW -> view(intent)
            else -> notificationTarget(intent)
        }
    }

    /**
     * A tap or an action on a notification. The extras are read before
     * the data URI because a notification posted by an earlier build
     * carries only the extras.
     */
    private fun notificationTarget(intent: Intent): LaunchRequest? {
        val accountId = intent.getStringExtra(MailNotifier.EXTRA_ACCOUNT_ID) ?: return null
        val replyTo = intent.getStringExtra(MailNotifier.EXTRA_REPLY_EMAIL_ID)
        if (replyTo != null) return LaunchRequest(AppDestination.Reply(accountId, replyTo))
        val threadId = intent.getStringExtra(MailNotifier.EXTRA_THREAD_ID) ?: return null
        return LaunchRequest(AppDestination.Thread(accountId, threadId))
    }

    private fun view(intent: Intent): LaunchRequest? {
        notificationTarget(intent)?.let { return it }
        val data = intent.data ?: return null
        if (data.scheme.equals("mailto", ignoreCase = true)) return mailto(data)
        return AppLinks.parse(data.toString())?.let { LaunchRequest(it) }
    }

    private fun mailto(uri: Uri): LaunchRequest =
        LaunchRequest(AppDestination.Compose(Mailto.parse(uri.toString()) ?: ComposePrefill()))

    /**
     * A share: the text becomes the body, `EXTRA_SUBJECT` the subject,
     * the address extras the recipients, and the streams the attachments
     * (REQ-AND-SYS-01). The files go up the composer's own attachment
     * path, so the image size choice applies to a shared photo as it does
     * to a picked one.
     */
    private fun share(intent: Intent): LaunchRequest {
        val text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString().orEmpty()
        val subject = intent.getStringExtra(Intent.EXTRA_SUBJECT).orEmpty()
        val prefill = ComposePrefill(
            to = intent.getStringArrayExtra(Intent.EXTRA_EMAIL)?.toList().orEmpty(),
            cc = intent.getStringArrayExtra(Intent.EXTRA_CC)?.toList().orEmpty(),
            bcc = intent.getStringArrayExtra(Intent.EXTRA_BCC)?.toList().orEmpty(),
            subject = subject,
            body = text,
        )
        return LaunchRequest(AppDestination.Compose(prefill), attachments = streams(intent))
    }

    private fun streams(intent: Intent): List<Uri> = when (intent.action) {
        Intent.ACTION_SEND_MULTIPLE -> parcelableList(intent)
        else -> listOfNotNull(parcelable(intent))
    }

    @Suppress("DEPRECATION")
    private fun parcelable(intent: Intent): Uri? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java)
        } else {
            intent.getParcelableExtra(Intent.EXTRA_STREAM)
        }

    @Suppress("DEPRECATION")
    private fun parcelableList(intent: Intent): List<Uri> =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java).orEmpty()
        } else {
            intent.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM).orEmpty()
        }
}
