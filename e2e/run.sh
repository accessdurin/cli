#!/bin/sh
# Runs the parity harness against the build named by DURIN_BIN (default: a fresh `go build`)
# with a private D-Bus session and an unlocked gnome-keyring, so the CLI's real OS credential
# store is exercised without touching the caller's keyring.
#
#   e2e/run.sh                          # all scenarios
#   e2e/run.sh -run TestTools           # extra arguments go to `go test`
#   DURIN_UPDATE_GOLDEN=1 e2e/run.sh    # re-record goldens
#
# The session bus listens at $XDG_RUNTIME_DIR/bus (/run/user/<uid>/bus), where a desktop
# session's bus lives: setup verification starts the saved bridge with a minimal environment
# that carries no DBUS_SESSION_BUS_ADDRESS, and keyring clients then look there. The script
# refuses to run if a bus already answers at that path.
#
# Needs dbus-daemon, gnome-keyring-daemon and secret-tool (Debian/Ubuntu: dbus,
# gnome-keyring, libsecret-tools).
set -eu
cd "$(dirname "$0")/.."
if [ -z "${DURIN_BIN:-}" ]; then
  go build -o "${TMPDIR:-/tmp}/durin-e2e" ./cmd/durin
  DURIN_BIN="${TMPDIR:-/tmp}/durin-e2e"
fi
export DURIN_BIN

export XDG_RUNTIME_DIR="/run/user/$(id -u)"
mkdir -p "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"
bus="$XDG_RUNTIME_DIR/bus"
if [ -S "$bus" ]; then
  if dbus-send --bus="unix:path=$bus" --dest=org.freedesktop.DBus --type=method_call --print-reply \
    /org/freedesktop/DBus org.freedesktop.DBus.GetId >/dev/null 2>&1; then
    echo "A D-Bus session already runs at $bus; run the harness in a disposable environment." >&2
    exit 1
  fi
  rm -f "$bus"
fi
export DBUS_SESSION_BUS_ADDRESS="unix:path=$bus"
dbus_pid=$(dbus-daemon --session --address="$DBUS_SESSION_BUS_ADDRESS" --fork --print-pid 2>/dev/null)
keyring_home=$(mktemp -d)
cleanup() {
  kill "$dbus_pid" 2>/dev/null || true
  pkill -f "gnome-keyring-daemon.*--components=secrets" 2>/dev/null || true
  rm -rf "$keyring_home" "$bus"
}
trap cleanup EXIT INT TERM
mkdir -p "$keyring_home/.local/share/keyrings"
# Unlocking with a known password creates and unlocks the default "login" collection.
printf 'e2e' | HOME="$keyring_home" gnome-keyring-daemon --unlock --replace --components=secrets >/dev/null 2>&1
export DURIN_E2E_KEYRING=1
go test ./e2e/ -count=1 "$@"
