package com.netzhansa.herold.shared.jmap

/**
 * JMAP capability URIs the client uses in `using` and looks for in the
 * session descriptor.
 *
 * The vendor URIs under `https://netzhansa.com/jmap/` are one wire surface across
 * three clients: they move together with the Go constants in
 * `internal/protojmap/registry.go` and the TypeScript constants in
 * `web/apps/suite/src/lib/jmap/types.ts`.
 */
object Capability {
    const val CORE = "urn:ietf:params:jmap:core"
    const val MAIL = "urn:ietf:params:jmap:mail"
    const val SUBMISSION = "urn:ietf:params:jmap:submission"
    const val SNOOZE = "urn:ietf:params:jmap:mail:snooze"
    const val CATEGORISE = "https://netzhansa.com/jmap/categorise"
    const val SUB_ACCOUNTS = "https://netzhansa.com/jmap/sub-accounts"
    const val PUSH = "https://netzhansa.com/jmap/push"
    const val EMAIL_REACTIONS = "https://netzhansa.com/jmap/email-reactions"
    const val CHAT = "https://netzhansa.com/jmap/chat"

    /** Server-side filter rules, thread mute and blocked senders (suite `04-filters.md`). */
    const val MANAGED_RULES = "https://netzhansa.com/jmap/managed-rules"

    /** The prompts, models and per-message classifier detail (suite G7). */
    const val LLM_TRANSPARENCY = "https://netzhansa.com/jmap/llm-transparency"

    /**
     * Keyboard-shortcut coaching is not used on phone
     * (docs/design/android/notes/server-contract.md, capabilities divergence).
     */
    const val SHORTCUT_COACH = "https://netzhansa.com/jmap/shortcut-coach"
}
