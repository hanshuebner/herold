#!/bin/bash
# Exercises the checkAttachedDevices Gradle guard (issue #484) without a
# real device or a real adb server: HEROLD_ADB_EXECUTABLE points the guard
# at a stub script this test writes, so it runs the same on a CI runner
# with no emulator as it does on a workstation with one attached.
#
# Run from anywhere; invoked by .forgejo/workflows/mobile.yml on every
# mobile push and by a developer as
#   mobile/scripts/check-attached-devices-guard-test.sh
set -euo pipefail

MOBILE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

write_fake_adb() {
    # $1: path to write; $2+: one "devices -l" output line per argument.
    local path="$1"
    shift
    {
        echo '#!/bin/bash'
        echo 'if [ "$1" = "devices" ]; then'
        echo '  echo "List of devices attached"'
        for line in "$@"; do
            printf '  echo %q\n' "$line"
        done
        echo '  exit 0'
        echo 'fi'
        echo 'echo "fake adb: unsupported command $*" >&2; exit 1'
    } > "$path"
    chmod +x "$path"
}

run_guard() {
    # $1: fake adb path; $2..: extra ./gradlew arguments.
    local adb_path="$1"
    shift
    HEROLD_ADB_EXECUTABLE="$adb_path" \
        "$MOBILE_DIR/gradlew" --no-daemon -p "$MOBILE_DIR" :androidApp:checkAttachedDevices "$@" \
        > "$WORK_DIR/out.log" 2>&1
}

TWO_DEVICE_ADB="$WORK_DIR/adb-two-devices"
write_fake_adb "$TWO_DEVICE_ADB" \
    "emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:18" \
    "57180DLAQ000KF          device usb:1-1 product:panther model:Pixel_9 device:panther transport_id:2"

ONE_EMULATOR_ADB="$WORK_DIR/adb-one-emulator"
write_fake_adb "$ONE_EMULATOR_ADB" \
    "emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:18"

echo "case 1: two devices, one a real phone -- expect failure listing both serials"
if run_guard "$TWO_DEVICE_ADB"; then
    cat "$WORK_DIR/out.log" >&2
    fail "checkAttachedDevices succeeded with two devices attached"
fi
grep -q "emulator-5554" "$WORK_DIR/out.log" || { cat "$WORK_DIR/out.log" >&2; fail "failure message omits the emulator serial"; }
grep -q "57180DLAQ000KF" "$WORK_DIR/out.log" || { cat "$WORK_DIR/out.log" >&2; fail "failure message omits the phone serial"; }
echo "  ok"

echo "case 2: one emulator -- expect success"
if ! run_guard "$ONE_EMULATOR_ADB"; then
    cat "$WORK_DIR/out.log" >&2
    fail "checkAttachedDevices failed with one emulator attached"
fi
echo "  ok"

echo "case 3: two devices with -Pherold.allowDevices=true -- expect success"
if ! run_guard "$TWO_DEVICE_ADB" -Pherold.allowDevices=true; then
    cat "$WORK_DIR/out.log" >&2
    fail "checkAttachedDevices failed under -Pherold.allowDevices=true"
fi
echo "  ok"

echo "case 4: install*/uninstall*/connected* tasks depend on the guard"
for task in :androidApp:uninstallAll :androidApp:installDebug :androidApp:connectedDebugAndroidTest; do
    "$MOBILE_DIR/gradlew" --no-daemon -p "$MOBILE_DIR" "$task" --dry-run > "$WORK_DIR/dryrun.log" 2>&1 \
        || { cat "$WORK_DIR/dryrun.log" >&2; fail "$task --dry-run failed"; }
    grep -q "checkAttachedDevices" "$WORK_DIR/dryrun.log" \
        || { cat "$WORK_DIR/dryrun.log" >&2; fail "$task's task graph does not include checkAttachedDevices"; }
done
echo "  ok"

echo "PASS: checkAttachedDevices guard"
