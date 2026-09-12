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
