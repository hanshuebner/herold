package com.netzhansa.herold.shared

import io.ktor.client.HttpClient

/**
 * The expect/actual seam androidMain (and later iosMain) fill in for
 * platform services -- Keystore-backed token storage, connectivity
 * observation, the clock (docs/design/android/architecture/
 * 01-system-overview.md, "Module layout").
 */
expect object Platform {
    val name: String
}

/**
 * Builds the platform's Ktor engine (OkHttp on Android). Keeping the engine
 * choice behind this seam means [com.netzhansa.herold.shared.jmap.JmapClient]
 * and its callers depend only on `HttpClient`, not on a specific engine
 * artifact.
 */
expect fun createHttpClient(): HttpClient
