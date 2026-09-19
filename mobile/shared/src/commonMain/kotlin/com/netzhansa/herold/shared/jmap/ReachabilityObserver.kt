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

    /** A request never got there. */
    fun unreachable()
}
