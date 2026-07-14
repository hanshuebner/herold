package com.netzhansa.herold.android

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import org.junit.Rule
import org.junit.Test

/**
 * Instrumented smoke test proving the Compose toolchain renders on a real
 * device/emulator. The authenticate-and-render-mailbox-list flow this
 * harness grows into is a later Phase 0 increment
 * (docs/design/android/implementation-plan.md).
 */
class HeroldAppTest {
    @get:Rule
    val composeTestRule = createComposeRule()

    @Test
    fun rendersTheStubScreen() {
        composeTestRule.setContent { HeroldApp() }

        composeTestRule.onNodeWithText("herold").assertExists()
    }
}
