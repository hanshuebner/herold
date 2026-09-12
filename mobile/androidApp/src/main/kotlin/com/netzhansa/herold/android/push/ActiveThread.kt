package com.netzhansa.herold.android.push

/**
 * The thread the reading pane is showing, if any. A push for a thread the
 * user is already looking at reconciles the store but posts no notification
 * (architecture `04-push.md`, suite REQ-PUSH-03 parity).
 *
 * Process-wide because the messaging service runs outside the activity.
 */
object ActiveThread {

    @Volatile
    private var current: Pair<String, String>? = null

    fun entered(accountId: String, threadId: String) {
        current = accountId to threadId
    }

    fun left(accountId: String, threadId: String) {
        if (current == accountId to threadId) current = null
    }

    fun isShowing(accountId: String, threadId: String): Boolean =
        current == accountId to threadId
}
