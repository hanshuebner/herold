package com.netzhansa.herold.shared.jmap

/** What a request that did not succeed actually met (issue #370). */
enum class FailureKind {
    /**
     * The server answered and refused. The decision stands, so the entry
     * stops and the user is told.
     */
    REFUSED,

    /**
     * The server answered that it does not understand the request as
     * sent: a 400 on a body it cannot parse, a route it does not serve,
     * a method or a media type it does not take. A server older than the
     * build that sent the request answers this way to a part the client
     * has grown, so the condition ends when the server is upgraded and
     * the request is worth holding on to (issue #420).
     */
    UNSUPPORTED,

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
 * The statuses a server answers with when the request itself is not
 * something it serves: an unparseable body, an unknown route, a method
 * or a media type it does not take. They look permanent on the wire and
 * are transient in fact - a server one release behind the client answers
 * exactly this to a request the client has grown (issue #420).
 */
private val NOT_UNDERSTOOD = setOf(400, 404, 405, 415, 501)

/**
 * Classifies a thrown failure. Only a [JmapException] carries something
 * the server said; everything else - the transport's own exceptions -
 * means the request never got there, so it is [FailureKind.OFFLINE].
 *
 * A method error is the server's decision about the request's content,
 * so it outranks the status: a refusal reported inside a JMAP response
 * stands even when the status says the request was not understood.
 */
fun classifyFailure(t: Throwable): FailureKind {
    val e = t as? JmapException ?: return FailureKind.OFFLINE
    val methodError = e.methodError
    if (methodError != null) {
        return if (methodError in RETRYABLE_METHOD_ERRORS) FailureKind.BUSY else FailureKind.REFUSED
    }
    val status = e.status ?: return FailureKind.BUSY
    if (status in NOT_UNDERSTOOD) return FailureKind.UNSUPPORTED
    val clientError = status in 400..499 &&
        status != HTTP_REQUEST_TIMEOUT && status != HTTP_TOO_MANY_REQUESTS &&
        status != HTTP_UNAUTHORIZED
    return if (clientError) FailureKind.REFUSED else FailureKind.BUSY
}
