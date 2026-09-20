package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode

/**
 * The inbox's chrome, as the checks reach it (issue #444).
 *
 * The top row carries the drawer button, the search field and the
 * avatar; reporting a problem is a drawer entry and signing out sits
 * under the avatar, so the classes that raise either walk the same way
 * a reader does.
 */

/** Raises the bug reporter from the inbox, through the drawer. */
internal fun ComposeTestRule.reportProblemFromTheDrawer() {
    onNodeWithTag("inbox-drawer-open").performClick()
    awaitTag("drawer-report-problem")
    onNodeWithTag("inbox-drawer").performScrollToNode(hasTestTag("drawer-report-problem"))
    onNodeWithTag("drawer-report-problem").performClick()
}

/** True once the inbox's top row is on screen. */
internal fun ComposeTestRule.onTheInbox(): Boolean =
    onAllNodesWithTag("inbox-drawer-open").fetchSemanticsNodes().isNotEmpty()
