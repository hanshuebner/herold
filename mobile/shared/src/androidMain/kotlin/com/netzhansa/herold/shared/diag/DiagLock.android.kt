package com.netzhansa.herold.shared.diag

import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock as jvmWithLock

internal actual class DiagLock actual constructor() {
    private val lock = ReentrantLock()

    actual fun <T> withLock(block: () -> T): T = lock.jvmWithLock(block)
}
