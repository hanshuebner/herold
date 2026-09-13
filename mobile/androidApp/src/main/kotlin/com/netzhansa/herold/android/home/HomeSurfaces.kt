package com.netzhansa.herold.android.home

import android.content.Context
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.shared.inbox.homeSnapshots
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch

/**
 * Keeps the home-screen surfaces in step with the local store
 * (REQ-AND-SYS-20/22): the launcher's conversation shortcuts are
 * republished and the widget is asked to repaint whenever the store
 * changes, so a push, a sync or an action the user took is reflected
 * without either surface polling.
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
            container.store.homeSnapshots().collect { snapshot ->
                runCatching { ConversationShortcuts.publish(context, snapshot.threads) }
                InboxWidget.refresh(context)
            }
        }
    }
}
