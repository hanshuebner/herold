package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
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

/**
 * True when the list tagged [listTag] is on screen and holds no row for
 * [threadId].
 *
 * The list itself has to be there. A check that reads a missing list as an
 * absent row is satisfied by whatever screen the app went to instead, so a
 * gesture that opened the conversation rather than swiping it away passes
 * the "the row is gone" wait and fails somewhere later (issue #379).
 *
 * Both reads go through the semantics tree rather than through an
 * assertion: this runs as a `waitUntil` condition, where an assertion
 * synchronises with Espresso and re-enters the layout pass it was called
 * from ("performMeasureAndLayout called during measure layout").
 */
fun ComposeTestRule.listLacksThread(threadId: String, listTag: String = "inbox-list"): Boolean {
    check(onAllNodesWithTag(listTag).fetchSemanticsNodes().isNotEmpty()) {
        "the list \"$listTag\" is not on screen, so the absence of a row means nothing"
    }
    return !listHoldsThread(threadId, listTag)
}

/** Brings the row for [threadId] into the viewport; fails when the list has none. */
fun ComposeTestRule.scrollListToThread(threadId: String, listTag: String = "inbox-list") {
    onNodeWithTag(listTag).performScrollToNode(hasTestTag("thread-row-$threadId"))
}

/**
 * Opens the conversation's overflow and waits for its entries. Mute,
 * snooze and share live there since the bar shrank to back, archive,
 * delete and mark-unread (issue #428).
 */
fun ComposeTestRule.openThreadOverflow(timeoutMs: Long = OVERFLOW_TIMEOUT_MS) {
    onNodeWithTag("thread-overflow").performClick()
    waitUntil(timeoutMs) { onAllNodesWithTag("thread-mute").fetchSemanticsNodes().isNotEmpty() }
}

/**
 * Opens one message's own overflow, which carries the answers and the
 * entries that belong to a message rather than to the conversation
 * (issue #428).
 */
fun ComposeTestRule.openMessageOverflow(messageId: String, timeoutMs: Long = OVERFLOW_TIMEOUT_MS) {
    onNodeWithTag("message-overflow-$messageId").performClick()
    waitUntil(timeoutMs) { onAllNodesWithTag("thread-why").fetchSemanticsNodes().isNotEmpty() }
}

/** How long a menu has to appear before the wait gives up. */
private const val OVERFLOW_TIMEOUT_MS = 30_000L
