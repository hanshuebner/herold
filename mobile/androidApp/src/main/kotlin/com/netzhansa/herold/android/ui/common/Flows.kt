package com.netzhansa.herold.android.ui.common

import androidx.compose.runtime.Composable
import androidx.compose.runtime.State
import androidx.compose.runtime.collectAsState
import kotlinx.coroutines.flow.Flow

/**
 * Collects a local-store flow into Compose state. The initial value is the
 * store's empty shape, so a screen renders its frame before the first row
 * arrives rather than blocking on it.
 */
@Composable
fun <T> Flow<T>.collectAsStateSafely(initial: T): State<T> = collectAsState(initial)
