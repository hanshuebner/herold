package com.netzhansa.herold.android.ui.common

import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.mandatorySystemGestures
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier

/**
 * What the system owns along the window's bottom edge: the navigation
 * bar, and the strip a gesture-navigated device reserves for the swipe
 * that leaves the app. The shell draws edge to edge, so the platform's
 * report is the only thing that says how tall that area is on the device
 * in hand - a bar's height with three buttons, a handle's worth with
 * gestures, and nothing at all on a device showing neither.
 *
 * The horizontal sides come along for the landscape case, where the
 * navigation bar stands at one edge of the screen (issue #428).
 */
val bottomSystemInsets: WindowInsets
    @Composable get() = WindowInsets.navigationBars
        .union(WindowInsets.mandatorySystemGestures)
        .only(WindowInsetsSides.Horizontal + WindowInsetsSides.Bottom)

/**
 * Keeps a row pinned to the bottom edge out of [bottomSystemInsets]: its
 * background still paints to the edge of the screen, its buttons sit
 * above the system's own area, and it takes exactly the room the device
 * reports, so a three-button device gets no empty band under the bar.
 * Applying it consumes the inset, so nothing nested inside pays it twice.
 */
@Composable
fun Modifier.bottomSystemBarsPadding(): Modifier = windowInsetsPadding(bottomSystemInsets)
