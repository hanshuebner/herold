package com.netzhansa.herold.android

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher

/**
 * Matches the per-row test tags the list screens carry
 * (`thread-row-<threadId>`), so a test can count or pick rows without
 * knowing the ids the dev instance happened to allocate.
 */
fun hasTestTagStartingWith(prefix: String): SemanticsMatcher =
    SemanticsMatcher("test tag starts with '$prefix'") { node ->
        node.config.getOrNull(SemanticsProperties.TestTag)?.startsWith(prefix) == true
    }
