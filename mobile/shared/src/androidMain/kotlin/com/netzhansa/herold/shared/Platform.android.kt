package com.netzhansa.herold.shared

import io.ktor.client.HttpClient
import io.ktor.client.engine.okhttp.OkHttp

actual object Platform {
    actual val name: String = "android"
}

actual fun createHttpClient(): HttpClient = HttpClient(OkHttp)
