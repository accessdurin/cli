#!/bin/sh
# Runs the parity harness against the build named by DURIN_BIN (default: a fresh `go build`)
# with a private D-Bus session and an unlocked gnome-keyring, so the CLI's real OS credential
# store is exercised without touching the caller's keyring.
#
#   e2e/run.sh                          # all scenarios
#   e2e/run.sh -run TestTools           # extra arguments go to `go test`
#   DURIN_UPDATE_GOLDEN=1 e2e/run.sh    # re-record goldens
#
# Each run gets its own runtime directory and session bus, so runs never share a keyring.
# Setup verification passes DBUS_SESSION_BUS_ADDRESS and XDG_RUNTIME_DIR to the bridge it
# starts, so the private bus is found there too.
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

export XDG_RUNTIME_DIR="$(mktemp -d)"
chmod 700 "$XDG_RUNTIME_DIR"
bus="$XDG_RUNTIME_DIR/bus"
export DBUS_SESSION_BUS_ADDRESS="unix:path=$bus"
dbus_pid=$(dbus-daemon --session --address="$DBUS_SESSION_BUS_ADDRESS" --fork --print-pid 2>/dev/null)
keyring_home=$(mktemp -d)
cleanup() {
  kill "$dbus_pid" 2>/dev/null || true
  [ -n "${keyring_pid:-}" ] && kill "$keyring_pid" 2>/dev/null || true
  rm -rf "$keyring_home" "$XDG_RUNTIME_DIR"
}
trap cleanup EXIT INT TERM
mkdir -p "$keyring_home/.local/share/keyrings"
# Unlocking with a known password creates and unlocks the default "login" collection.
keyring_env=$(printf 'e2e' | HOME="$keyring_home" gnome-keyring-daemon --unlock --components=secrets 2>/dev/null || true)
keyring_pid=$(printf '%s\n' "$keyring_env" | sed -n 's/^GNOME_KEYRING_PID=//p')
# Wait until the daemon owns the Secret Service name on this bus.
for _ in $(seq 50); do
  dbus-send --session --dest=org.freedesktop.DBus --type=method_call --print-reply \
    /org/freedesktop/DBus org.freedesktop.DBus.NameHasOwner string:org.freedesktop.secrets 2>/dev/null \
    | grep -q 'boolean true' && break
  sleep 0.1
done
export DURIN_E2E_KEYRING=1
go test ./e2e/ -count=1 "$@"
