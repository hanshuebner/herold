package com.netzhansa.herold.android.ui.inbox

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/**
 * The tab the inbox's lane row draws its indicator over (issue #447),
 * on the host JVM.
 *
 * The indicator is handed a selected index and the positions of the
 * tabs that were measured. The two are read from different states, so
 * while a lane goes - the last message of a category archived - the
 * index can name a tab the measurement no longer holds; that frame
 * draws no indicator rather than indexing past the end.
 */
class CategoryTabRowTest {

    @Test
    fun `the selected tab is the one the indicator draws over`() {
        assertEquals(0, indicatorTab(tabCount = 2, selectedTabIndex = 0))
        assertEquals(1, indicatorTab(tabCount = 2, selectedTabIndex = 1))
    }

    @Test
    fun `a selection past the measured tabs draws nothing`() {
        assertNull(indicatorTab(tabCount = 1, selectedTabIndex = 1))
        assertNull(indicatorTab(tabCount = 2, selectedTabIndex = 7))
    }

    @Test
    fun `a row with no tabs draws nothing`() {
        assertNull(indicatorTab(tabCount = 0, selectedTabIndex = 0))
    }

    @Test
    fun `a selection below the row draws nothing`() {
        assertNull(indicatorTab(tabCount = 2, selectedTabIndex = -1))
    }
}
