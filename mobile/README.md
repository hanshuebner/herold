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

- `SearchAcceptanceTest` searches for messages whose subject starts with
  `seed message`; deliver two or three over the instance's SMTP listener.
- `ComposeAcceptanceTest#t22` needs two accounts, which means separating
  the seeded identity: `Identity/set {"<id>": {"separated": true}}` on the
  primary account, with the sub-accounts capability in `using`. The seed
  leaves it separable, not separated.

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

`UndoSendAcceptanceTest` runs online and needs no phases.
`t64` needs the foreign-identity seed, so start the instance with
`HEROLD_DEV_EXTERNAL_SUBMISSION=1`.

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
