package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performScrollToNode

/**
 * True when the list tagged [listTag] holds a row for [threadId], scrolling
 * the list to it when the row sits outside the composed window.
 *
 * A LazyColumn composes only the rows around the viewport, so on a list
 * longer than one screen a row that is present in the store is invisible to
 * a plain tag lookup (issue #335). A conversation coming back from a snooze
 * keeps the time it was received, so it returns below whatever arrived while
 * it slept: on an inbox of thirty threads the woken row landed nineteenth
 * and a wait for its tag alone timed out (issue #355).
 */
fun ComposeTestRule.listHoldsThread(threadId: String, listTag: String = "inbox-list"): Boolean {
    if (onAllNodesWithTag("thread-row-$threadId").fetchSemanticsNodes().isNotEmpty()) return true
    return runCatching { scrollListToThread(threadId, listTag) }.isSuccess
}

/** Brings the row for [threadId] into the viewport; fails when the list has none. */
fun ComposeTestRule.scrollListToThread(threadId: String, listTag: String = "inbox-list") {
    onNodeWithTag(listTag).performScrollToNode(hasTestTag("thread-row-$threadId"))
}
