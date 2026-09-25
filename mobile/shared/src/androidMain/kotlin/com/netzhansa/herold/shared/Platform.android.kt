package com.netzhansa.herold.shared

import io.ktor.client.HttpClient
import io.ktor.client.engine.okhttp.OkHttp
import io.ktor.client.plugins.HttpTimeout

actual object Platform {
    actual val name: String = "android"
}

/**
 * `HttpTimeout` is installed with no defaults, so an ordinary JMAP call
 * keeps the OkHttp engine's own timeouts; only a request that asks for a
 * longer socket-read timeout (the event stream, issue #493) gets one, via
 * the per-request `timeout { }` override.
 */
actual fun createHttpClient(): HttpClient = HttpClient(OkHttp) {
    install(HttpTimeout)
}
