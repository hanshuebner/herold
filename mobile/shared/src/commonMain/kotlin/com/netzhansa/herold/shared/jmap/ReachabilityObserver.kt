package com.netzhansa.herold.shared.jmap

/**
 * What the transport tells the shell about reaching the server
 * (issue #433). The transport is the only place that knows whether a
 * request got an answer, so it is what reports it: every caller - a
 * sync pass, an outbox drain, the EventSource connection, a blob
 * fetch - clears the offline indication by the traffic it makes,
 * whatever branch the caller itself ends on.
 */
interface ReachabilityObserver {

    /** A request reached the server, whatever the server then said. */
    fun reached()

    /**
     * A request confirmed the server: a status the transport answered
     * with a body that decoded as the descriptor or a method response,
     * not merely a socket round trip that completed (issue #479). A
     * captive portal's login page completes a round trip too, so what
     * tells the platform the network is validated needs this rather
     * than [reached]. Implies [reached].
     */
    fun confirmedReached()

    /** A request never got there. */
    fun unreachable()
}
