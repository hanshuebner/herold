package com.netzhansa.herold.shared.auth

import io.ktor.client.statement.HttpResponse
import io.ktor.client.statement.bodyAsText
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

/** One answered REST call: its status and its body, read once. */
data class BearerResponse(val status: Int, val body: String) {
    val isSuccess: Boolean get() = status in 200..299

    /**
     * The server refused the call until the credential is elevated by a
     * TOTP code (server `REQ-AUTH-79`: a 403 carrying
     * `step_up_required`).
     */
    val stepUpRequired: Boolean
        get() = status == FORBIDDEN && problemFlag(body, "step_up_required")

    /** The human-readable line the server put on the problem document. */
    val problemTitle: String
        get() = problemString(body, "title")

    private companion object {
        const val FORBIDDEN = 403
    }
}

private val problemJson = Json { ignoreUnknownKeys = true }

internal fun problemFlag(body: String, field: String): Boolean = runCatching {
    problemJson.parseToJsonElement(body).jsonObject[field]?.jsonPrimitive?.booleanOrNull
}.getOrNull() ?: false

internal fun problemString(body: String, field: String): String = runCatching {
    problemJson.parseToJsonElement(body).jsonObject[field]?.jsonPrimitive?.content
}.getOrNull().orEmpty()

/**
 * What a caller does when the server answers `step_up_required`: collect
 * a TOTP code from the user and elevate the credential
 * (REQ-AND-AUTH-20). It returns true once the elevation is in place and
 * the refused request is worth repeating.
 */
interface StepUpGate {
    suspend fun elevate(): Boolean

    companion object {
        /** For a caller with no surface to prompt on: the request stays refused. */
        val REFUSED: StepUpGate = object : StepUpGate {
            override suspend fun elevate(): Boolean = false
        }
    }
}

/**
 * The account's REST surface as a bearer caller sees it: one token from
 * [TokenProvider], one silent refresh on a 401 (REQ-AND-AUTH-04), and one
 * TOTP elevation on a `step_up_required` 403 (REQ-AND-AUTH-20). Each
 * recovery is attempted once, so a server that keeps refusing ends the
 * call rather than looping on it.
 */
class BearerCalls(
    private val tokens: TokenProvider,
    private val stepUp: StepUpGate = StepUpGate.REFUSED,
) {

    suspend fun call(request: suspend (String) -> HttpResponse): BearerResponse {
        val token = tokens.accessToken()
        var answered = read(request(token))
        if (answered.status == UNAUTHORIZED) {
            val refreshed = tokens.refreshAfterUnauthorized(token)
            if (refreshed != null) answered = read(request(refreshed))
        }
        if (answered.stepUpRequired && stepUp.elevate()) {
            // The elevation is recorded against the credential, so the
            // repeat goes out on the same token the server just refused.
            answered = read(request(tokens.accessToken()))
        }
        return answered
    }

    private suspend fun read(response: HttpResponse) =
        BearerResponse(response.status.value, response.bodyAsText())

    private companion object {
        const val UNAUTHORIZED = 401
    }
}
