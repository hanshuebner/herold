package com.netzhansa.herold.android.home

import android.content.Context
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.shared.inbox.HomeSnapshot
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch

/**
 * Keeps the home-screen surfaces in step with the local store
 * (REQ-AND-SYS-20/22): the widget repaints and the launcher's
 * conversation shortcuts are republished whenever the store changes, so a
 * push, a sync or an action the user took is reflected without either
 * surface polling.
 *
 * The store is the only input, which is what makes both surfaces correct
 * offline.
 */
class HomeSurfaces(
    private val context: Context,
    private val container: AppContainer,
    private val scope: CoroutineScope,
) {
    fun start() {
        scope.launch {
            combine(
                container.store.inboxEmails(EMAIL_LIMIT),
                container.store.accounts(),
                container.store.mailboxes(),
            ) { emails, accounts, mailboxes ->
                HomeSnapshot.from(emails, accounts, mailboxes)
            }
                .distinctUntilChanged()
                .collect { snapshot ->
                    runCatching { ConversationShortcuts.publish(context, snapshot.threads) }
                    InboxWidget.refresh(context)
                }
        }
    }

    private companion object {
        const val EMAIL_LIMIT = 200L
    }
}
