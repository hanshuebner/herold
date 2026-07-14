package com.netzhansa.herold.shared

/**
 * The expect/actual seam androidMain (and later iosMain) fill in for
 * platform services -- Keystore-backed token storage, connectivity
 * observation, the clock (docs/design/android/architecture/
 * 01-system-overview.md, "Module layout").
 */
expect object Platform {
    val name: String
}
