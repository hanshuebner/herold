package com.netzhansa.herold.shared.jmap

/** What a request that did not succeed actually met (issue #370). */
enum class FailureKind {
    /**
     * The server answered and refused. The decision stands, so the entry
     * stops and the user is told.
     */
    REFUSED,

    /**
     * The server answered that it could not do this now - a 5xx, a
     * rate limit, an expired token, a `serverFail`. Another attempt is
     * worth making.
     */
    BUSY,

    /**
     * Nothing reached the server: a name that would not resolve, a
     * refused or unreachable connection, a timeout on the wire. This is
     * the phone being offline, which is not an error the user is shown.
     */
    OFFLINE,
}

/** A status the server answers with that means "come back later". */
private const val HTTP_REQUEST_TIMEOUT = 408
private const val HTTP_TOO_MANY_REQUESTS = 429
private const val HTTP_UNAUTHORIZED = 401

/** Method errors that describe a busy server rather than a refusal. */
private val RETRYABLE_METHOD_ERRORS = setOf("serverFail", "serverUnavailable", "serverPartialFail")

/**
 * Classifies a thrown failure. Only a [JmapException] carries something
 * the server said; everything else - the transport's own exceptions -
 * means the request never got there, so it is [FailureKind.OFFLINE].
 */
fun classifyFailure(t: Throwable): FailureKind {
    val e = t as? JmapException ?: return FailureKind.OFFLINE
    val status = e.status
    val clientError = status != null && status in 400..499 &&
        status != HTTP_REQUEST_TIMEOUT && status != HTTP_TOO_MANY_REQUESTS &&
        status != HTTP_UNAUTHORIZED
    val refused = e.methodError != null && e.methodError !in RETRYABLE_METHOD_ERRORS
    return if (clientError || refused) FailureKind.REFUSED else FailureKind.BUSY
}
