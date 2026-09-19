# herold mobile client

Kotlin Multiplatform client for herold: `shared/` holds the JMAP-over-bearer
client, the sync engine and the SQLDelight local store; `androidApp/` holds the
Compose UI and every Android platform integration. Requirements and
architecture live in `docs/design/android/`.

## Build

    ./gradlew :shared:build :androidApp:assembleDebug

The instrumented acceptance suite runs against an ephemeral herold from
`scripts/dev-instance.sh`; from an emulator the host is `10.0.2.2`:

    ./gradlew :androidApp:connectedDebugAndroidTest \
      -Pandroid.testInstrumentationRunnerArguments.heroldBaseUrl=http://10.0.2.2:<port>

## The instrumented acceptance harness

The suite runs against one ephemeral instance and a headless emulator, in
phases, because two of its checks need the radios off and three need seed
data the instance does not create on its own.

    # the instance: the sub-account seed is what gives alice a second account
    HEROLD_DEV_SUB_ACCOUNTS=1 scripts/dev-instance.sh start

    # a fresh install, so no earlier run's state is in the store
    adb shell pm clear com.netzhansa.herold.android
    ./gradlew :androidApp:installDebug :androidApp:installDebugAndroidTest

Seed what the suite expects beyond its own deliveries:

- `ComposeAcceptanceTest#t22` needs two accounts. The seed leaves the
  identity separable, not separated, so the suite's setup separates it
  itself over JMAP; all it needs from the instance is the
  `HEROLD_DEV_SUB_ACCOUNTS=1` seed.

`OutboxAcceptanceTest` runs in phases around the radios and a kill of the
app process, one `am instrument` invocation each:

    #60 online: seeds the two conversations the offline phase acts on
    adb shell svc data disable && adb shell svc wifi disable
    #61 offline: archive, star, send with an attachment - three entries
    adb shell am kill com.netzhansa.herold.android
    #62 the queue is still there in a fresh process
    adb shell svc data enable && adb shell svc wifi enable
    #63 the drain, asserted through an independent JMAP client
    #64 a send herold refuses, retained with its reason and retried
    #65 queues a send to leave with the app closed: kill the app (`am
        kill`, not `am force-stop` - a force-stopped package gets no
        background work until the user opens it again), turn the radios
        back on, and read the recipient's account to see it arrive

The client treats a network the system has not validated as no network,
so the checks that turn the radios off and on need the emulator to
validate its own. An emulator whose captive-portal probe cannot reach
the internet never marks the link validated, the queue stays parked and
the drain phases time out; switch the probe off once per emulator:

    adb shell settings put global captive_portal_detection_enabled 0
    adb shell settings put global captive_portal_mode 0

`StatusIndicatorAcceptanceTest` asserts that the status indicator
changes state without moving anything (issue #421). It runs in two
phases around the radios, and the second reads what the first measured
out of the app's own storage, so the app must not be cleared between
them:

    #t70 online: measures the list and a row while idle and with a
         send waiting out its undo window, and reads the diagnostics
         screen the dot opens
    adb shell svc data disable && adb shell svc wifi disable
    #t71 offline: the same measurements with no connection
    adb shell svc data enable && adb shell svc wifi enable

`UndoSendAcceptanceTest` runs online and needs no phases.
`t64` needs the foreign-identity seed, so start the instance with
`HEROLD_DEV_EXTERNAL_SUBMISSION=1`.
`InlineReplyAcceptanceTest` runs online too: it injects a push, sends a
`RemoteInput` reply through the notification action's `PendingIntent`,
and reads the reply back from `bob@example.local`. It sets the undo
window itself, and the shade shot needs the emulator unlocked.

### Sign-in, unlock and sessions (issue #352)

`CustomTabSignInTest` drives the emulator's real browser. Complete
Chrome's first-run screens once before the run - launch it and tap "Use
without an account" - or the first Custom Tab of the run shows them
instead of herold's login page:

    adb shell am start -a android.intent.action.VIEW -d http://example.com
    # tap through "Use without an account"

`UnlockAcceptanceTest` needs a screen lock, and runs in two phases with
a process kill between them:

    adb shell locksettings set-pin 1234
    # UnlockAcceptanceTest#t80  - locks on returning to the foreground
    adb shell am kill com.netzhansa.herold.android
    # UnlockAcceptanceTest#t81  - the fresh process starts locked
    adb shell locksettings clear --old 1234

The emulator's device credential stands in for a fingerprint: enrolling
one needs a pass through the Settings UI, and the credential is the
fallback the same prompt offers.

`OAuthAcceptanceTest` needs no device setup. Pass the instance's
`OAUTH2_CLIENT_ID` when it is not the default `herold-android`:

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.OAuthAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldTotpSecret <ADMIN_TOTP_SECRET> \
      -e heroldOauthClientId <OAUTH2_CLIENT_ID> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

`StepUpAcceptanceTest` drives the six-digit step-up sheet against
`POST /api/v1/auth/step-up` (issue #401). It signs in as the instance's
TOTP-enrolled principal, so it needs the printed `ADMIN_TOTP_SECRET`:

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.StepUpAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldTotpSecret <ADMIN_TOTP_SECRET> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

`t77` spends one wrong code before the right one. The server locks a
principal out after five consecutive wrong TOTP attempts, so re-running
it repeatedly against the same instance eventually meets the lockout
rather than the sheet.

### Registering the OAuth2 client on a real instance

`scripts/dev-instance.sh` registers the client itself. A production
instance needs it once, from an admin session:

    curl -X POST https://<host>/api/v1/oauth2/clients \
      -H "Authorization: Bearer <admin api key>" \
      -H "Content-Type: application/json" \
      -d '{
            "client_id": "herold-android",
            "name": "herold Android",
            "redirect_uris": ["com.netzhansa.herold:/oauth2/callback"]
          }'

The client is public: no secret is issued and PKCE S256 is mandatory.
Omitting `scopes` grants the default end-user set. The redirect URI is
matched byte-for-byte, so it must read exactly as above
(`docs/design/android/notes/server-contract.md`).

Then run the online phase, the offline phase, and collect the screenshots:

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.AcceptanceTest,... \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
      -e heroldFakeFcmAddr 10.0.2.2:<fakefcm-port> \
      -e heroldTotpSecret <ADMIN_TOTP_SECRET> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

    adb shell svc data disable && adb shell svc wifi disable
    # OfflineAcceptanceTest#t2..., ComposeOfflineAcceptanceTest,
    # SearchAcceptanceTest#t31... - the phase-one runs above warm their caches
    adb shell svc data enable && adb shell svc wifi enable

    adb pull /sdcard/herold-shots

Run the classes in a few invocations rather than one: a single invocation of
the whole suite loads the emulator enough that a delivery wait or the
editor's readiness check can exceed its 30 s budget.

### Any order, twice over (issue #414)

The classes share one account on the instance, so a class that writes
account-wide state gives it back. `AcceptanceTest`, `CategoryAcceptanceTest`,
`FiltersAcceptanceTest` and `MailboxNavigationAcceptanceTest` snapshot the
labels or rules they find and restore them in an `@After`
(`LabelState.take` / `restore`), and the checks that need a pinned lane
clear the five-pin budget first rather than hoping it is free. A new class
that writes labels, dispositions, managed rules or snoozes does the same: the
suite has to pass in any order and twice in a row on one instance.

Mail is the exception - a check delivers the messages it reads and leaves
them - so a class asserts on the conversations it created, never on what the
seed or an earlier class happens to hold.

### Oversized parts (issue #420)

`BlobCacheInstrumentedTest` needs no instance: it states Android's 2 MiB
cursor-window limit against the row layout the blob cache used to have,
then round-trips a 3 MiB blob through the cache as it is now.

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.BlobCacheInstrumentedTest \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

`LargeInlineImageAcceptanceTest` delivers a message whose inline image
is larger than that window and opens the thread on it, which is the
flow that closed the app on the reporting device:

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.LargeInlineImageAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

`ForwardHtmlAcceptanceTest` forwards a formatted message - a heading, a
table, an inline image, a script and a style block - to the signed-in
principal, then reads the copy that arrived over JMAP: the markup and
the inline part are there, the script and the style block are not
(issue #431).

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.ForwardHtmlAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

`CrashRestoreAcceptanceTest` stands on a conversation, leaves the crash
record the uncaught-exception handler writes, and recreates the
activity: the shell comes back on the inbox, and the trace stays for the
next report.

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.CrashRestoreAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

### The bug reporter (issues #407, #417)

`BugReportAcceptanceTest` drives the in-app reporter against the dev
instance: a one-tap report from a thread overflow, a described one, the
shake, and a report raised from the inbox, each read back from the
server's `GET /api/v1/bug-reports`. That listing needs a bug-reports
key, which the instance mints from its bootstrap admin key:

    KEY=$(HEROLD_API_KEY=$(cat <state-dir>/api-key.txt) \
      bin/herold api-key create admin@example.local \
        --server-url <admin-url> --scope bug-reports --json | jq -r .key)

Run the class in its own invocation:

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.BugReportAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
      -e heroldBugReportsKey "$KEY" \
      -e heroldEmulatorToken "$(cat ~/.emulator_console_auth_token)" \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

Without `heroldBugReportsKey` the three server-side checks skip. The
same key drives `bin/herold bug-fetch --server-url <backend-url>
--dry-run` with `$HEROLD_BUG_REPORTS_KEY`, which is how the posted
reports are listed from the maintainer's side.

`BugReportMultiCaptureAcceptanceTest` drives a report that carries more
than one screen (issue #424): a capture on a conversation, "Add another
capture", a second capture from the inbox added to the open report, and
one send whose drop carries `screenshot-1.png`, `screenshot-2.png` and
two `captures[]` entries. Its last check leaves a report open on
purpose, for the class that follows:

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.BugReportMultiCaptureAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
      -e heroldBugReportsKey "$KEY" \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

    adb shell am kill com.netzhansa.herold.android

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.BugReportPendingReportSurvivesTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

The second invocation is a cold process, which is what a pending report
has to survive: the marker it finds comes from app storage alone.

The shake is injected through the emulator console rather than by hand:
`adb emu` talks to a telnet listener on the host's loopback, which the
device reaches at `10.0.2.2:5554`, and the listener wants the token in
`~/.emulator_console_auth_token`. `heroldEmulatorConsole` overrides the
address. Without `heroldEmulatorToken` the shake check skips, so the
class still runs on a physical device.

### The bottom edge, under both navigation modes (issue #428)

`BottomInsetAcceptanceTest` measures what the shell pins to the bottom
of the window - the conversation's reply pills, the inbox's compose
button, the drawer's pinned entries, the diagnostics and outbox
screens, the bug reporter's action row, the snooze sheet's last row -
against the inset the window reports, and fails when one of them
reaches into the navigation bar or the gesture handle. Run it once per
navigation mode; the overlay switch restarts SystemUI, so give it a
moment before the run:

    adb shell cmd overlay enable com.android.internal.systemui.navbar.gestural
    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.BottomInsetAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

    adb shell cmd overlay enable com.android.internal.systemui.navbar.threebutton
    # the same invocation again

The shots it takes carry the mode in their name
(`m4-bottom-inset-gesture-thread.png`,
`m4-bottom-inset-threebutton-thread.png`, and one per surface), and
each run logs the window height and the insets it measured under the
`BottomInset` tag, which `adb logcat -d -s BottomInset` prints.

### UnifiedPush (issue #229)

`UnifiedPushAcceptanceTest` and `PushTransportSettingsTest` need a
distributor on the device and a way for the host-side instance to reach
it. `mobile/fakeDistributor` is that distributor: it serves its endpoint
on device port 19280, and `adb forward` publishes the same port on the
host, which is what `scripts/dev-instance.sh` allowlists in
`[server.push.network]` (override with `HEROLD_DEV_UNIFIEDPUSH_PORT`).

    ./gradlew :fakeDistributor:installDebug
    adb shell am start -n com.netzhansa.herold.fakedistributor/.MainActivity
    adb forward tcp:19280 tcp:19280
    curl -X POST http://127.0.0.1:19280/control/state   # {"delivered":0,"gone":false}

    adb shell am instrument -w -r \
      -e class com.netzhansa.herold.android.UnifiedPushAcceptanceTest \
      -e heroldBaseUrl http://10.0.2.2:<backend-port> \
      -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
      com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner

Run the four methods in two or three invocations: `t92` waits for the
dispatcher to retry against a 410 and `t93` fetches an FCM token.

A real device uses a real distributor (ntfy, NextPush): install it, pick
it under Settings -> Push transport, and the endpoint it hands out is
registered the same way. The fake exists because the emulator has Play
Services and no distributor, and a distributor pointed at a self-hosted
ntfy would hand out an endpoint naming the host as the emulator sees it
(`10.0.2.2`), which the herold instance running on that host cannot POST
to.

## Firebase (push)

Push needs a Firebase project. The build reads its values and the app
initialises Firebase from them in code, so the google-services Gradle plugin is
not applied and a checkout without credentials still builds — push is simply
off. Two sources, first match wins, neither committed:

1. `mobile/androidApp/firebase.properties`:

       projectId=herold-xxxxx
       applicationId=1:123456789012:android:abcdef0123456789
       apiKey=AIza...
       projectNumber=123456789012

2. `~/.config/herold-android/google-services.json` — the file as downloaded
   from the Firebase console. `$HEROLD_GOOGLE_SERVICES_JSON` overrides the
   location; `mobile/androidApp/google-services.json` is also read.

`./gradlew :androidApp:printBuildFacts` reports whether the build picked a
project up, the version it will stamp, and whether release signing is
configured.

The FCM service-account key is the server's half (`fcm_service_account_json`
in the herold Ansible role); the client never holds it.

## Release

An `android-vX.Y.Z` tag drives the `release` job in
`.forgejo/workflows/mobile.yml`: it builds `assembleRelease`, verifies the
signature with `apksigner`, and attaches `herold-android-<version>.apk` plus
its checksum to a Forgejo release. `versionName` is the tag without the
`android-v` prefix and `versionCode` is derived from it (1.4.2 -> 10402); an
untagged build is 0.1.0 / 1.

Signing material reaches the build through the environment, never the tree:

- `ANDROID_KEYSTORE_B64` — base64 of the keystore (or `ANDROID_KEYSTORE_FILE`
  for a local path).
- `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`.

All four are Forgejo repository secrets. A PKCS12 keystore protects its key
with the store password, so the build probes the configured key password and
falls back to the store password when it does not open the key.

Locally, with the maintainer's keystore:

    set -a; . ~/.config/herold-android/release-secrets.env; set +a
    export ANDROID_KEYSTORE_FILE=~/.config/herold-android/release.keystore \
           ANDROID_KEYSTORE_PASSWORD="$keystore_password" \
           ANDROID_KEY_ALIAS="$key_alias" \
           ANDROID_KEY_PASSWORD="$key_password"
    ./gradlew :androidApp:assembleRelease
    apksigner verify --print-certs \
      androidApp/build/outputs/apk/release/androidApp-release.apk
