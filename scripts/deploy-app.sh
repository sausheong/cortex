#!/usr/bin/env bash
# Deploy the cortex binary and its secrets to an app host prepared with
# `setup-server.sh --app-host`. Run from your workstation (needs go, ssh, scp, curl).
#
# Usage: deploy-app.sh [--fqdn HOST] [--show-token] <ssh-target>
#   <ssh-target>   e.g. root@cortex.example.com
#   --fqdn HOST    public host name (default: the host part of <ssh-target>)
#   --show-token   also print CORTEX_AUTH_TOKEN (otherwise only says where it is)
#
# Preflight (before the cutover): the host must have cortex.service.prev, CORTEX_SECRET_KEY,
# CORTEX_PUBLIC_URL; else re-run setup-server.sh. No provider key is needed or forwarded:
# per-user settings hold it (cortex users set-provider).
# Steps: detect the server arch, cross-compile (GOOS=linux, CGO_ENABLED=0), upload the
# binary, copy GOOGLE_OAUTH_CLIENT_ID/SECRET and CORTEX_BOOTSTRAP_EMAIL found in your environment (or the repo .env, if any)
# into /etc/cortex/cortex.env (values travel over ssh stdin, never argv or output).
# Then the single restart: stop the service -> final pg_dump -Fc to
# /root/cortex/pre-multiuser-<UTC timestamp>.dump (abort if it fails; an existing dump is
# never overwritten) -> keep the installed binary as
# cortex.prev -> install the new binary -> `cortex migrate --timeout 30m` -> daemon-reload
# and start -> poll https://<fqdn>/healthz. A failed migrate or health check exits
# non-zero (a failed migrate never starts the new binary) and prints the rollback
# command (scripts/rollback-app.sh).
# Only one deploy/rollback runs at a time: a lock directory (/run/cortex-deploy.lock) is held on
# the server for the whole run. ssh connections are multiplexed (ControlMaster) so the host's
# ssh rate limit is not hit.
# Environment: SSH_OPTS (extra ssh/scp options, e.g. "-o BatchMode=yes"),
# DUMP_PATH (remote dump file, default /root/cortex/pre-multiuser-<timestamp>.dump; must not exist).
set -euo pipefail

# Variables copied from the local environment into the server env file when set.
# Provider settings (OpenAI/Anthropic/LLM/EMBEDDING) are never forwarded: each user's own
# settings hold them (see `cortex users set-provider`).
FORWARD_VARS=(GOOGLE_OAUTH_CLIENT_ID GOOGLE_OAUTH_CLIENT_SECRET CORTEX_BOOTSTRAP_EMAIL)
DUMP_PATH="${DUMP_PATH:-/root/cortex/pre-multiuser-$(date -u +%Y%m%dT%H%M%SZ).dump}"

FQDN=""
SHOW_TOKEN=0
TARGET=""
while [ $# -gt 0 ]; do
  case "$1" in
    --fqdn) FQDN="${2:?--fqdn needs a value}"; shift 2 ;;
    --show-token) SHOW_TOKEN=1; shift ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    -*) echo "unknown argument: $1" >&2; exit 2 ;;
    *) [ -z "$TARGET" ] || { echo "only one ssh target allowed" >&2; exit 2; }; TARGET="$1"; shift ;;
  esac
done
[ -n "$TARGET" ] || { echo "usage: $0 [--fqdn HOST] [--show-token] <ssh-target>" >&2; exit 2; }
if [ -z "$FQDN" ]; then
  FQDN="${TARGET#*@}"
fi
if ! printf '%s' "$FQDN" | grep -Eq '^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$' \
   || printf '%s' "$FQDN" | grep -Eq '^[0-9.]+$'; then
  echo "cannot derive a host name from '$TARGET'; pass --fqdn HOST" >&2; exit 2
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# The repo .env (git-ignored) may hold the Google and bootstrap settings. Only the
# FORWARD_VARS keys are parsed out of it (it is never sourced, never printed); a value
# already in the environment wins.
if [ -f "$REPO_ROOT/.env" ]; then
  for v in "${FORWARD_VARS[@]}"; do
    [ -z "${!v:-}" ] || continue
    line="$(grep -E "^[[:space:]]*(export[[:space:]]+)?${v}=" "$REPO_ROOT/.env" | tail -n1 || true)"
    [ -n "$line" ] || continue
    val="${line#*=}"
    case "$val" in
      \"*\") val="${val#\"}"; val="${val%\"}" ;;
      \'*\') val="${val#\'}"; val="${val%\'}" ;;
    esac
    [ -z "$val" ] || printf -v "$v" '%s' "$val"
  done
fi
SSH_MUX=(-o ControlMaster=auto -o "ControlPath=/tmp/cx-%C" -o ControlPersist=600)
# shellcheck disable=SC2206
SSH_EXTRA=("${SSH_MUX[@]}" ${SSH_OPTS:-})
# shellcheck disable=SC2029
rssh() { ssh "${SSH_EXTRA[@]}" "$TARGET" "$@"; }

BUILD_DIR=""
LOCKED=0
cleanup() {
  [ -z "$BUILD_DIR" ] || rm -rf "$BUILD_DIR"
  if [ "$LOCKED" -eq 1 ]; then rssh 'rmdir /run/cortex-deploy.lock' >/dev/null 2>&1 || true; fi
  ssh "${SSH_MUX[@]}" -O exit "$TARGET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> taking the deploy lock"
lrc=0
rssh 'mkdir /run/cortex-deploy.lock 2>/dev/null || exit 7' || lrc=$?
if [ "$lrc" -eq 7 ]; then
  echo "another deploy/rollback holds /run/cortex-deploy.lock on the server (if none is running, remove it: ssh ${TARGET} rmdir /run/cortex-deploy.lock)." >&2
  exit 1
elif [ "$lrc" -ne 0 ]; then
  echo "could not reach ${TARGET} over ssh (exit ${lrc})." >&2
  exit 1
fi
LOCKED=1

echo "==> detecting server architecture"
case "$(rssh uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "unsupported server architecture" >&2; exit 1 ;;
esac
echo "    linux/${GOARCH}"

BUILD_DIR="$(mktemp -d)"
echo "==> building cortex (linux/${GOARCH}, CGO_ENABLED=0)"
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
  go build -trimpath -ldflags "-s -w" -o "$BUILD_DIR/cortex" ./cmd/cortex/)

echo "==> uploading the binary (not installed yet)"
scp "${SSH_EXTRA[@]}" -q "$BUILD_DIR/cortex" "${TARGET}:/usr/local/bin/cortex.new"
rssh 'chmod 0755 /usr/local/bin/cortex.new'

echo "==> updating /etc/cortex/cortex.env"
# Remote side reads KEY=VALUE lines on stdin; only allow-listed keys are written.
read -r -d '' REMOTE <<'REMOTE_SCRIPT' || true
set -eu
ENV_FILE=/etc/cortex/cortex.env
[ -f "$ENV_FILE" ] || { echo "missing $ENV_FILE: run setup-server.sh --app-host first" >&2; exit 1; }
tmp="$(mktemp)"
cat "$ENV_FILE" > "$tmp"
n=0
while IFS= read -r line; do
  key="${line%%=*}"; val="${line#*=}"
  case "$key" in
    GOOGLE_OAUTH_CLIENT_ID|GOOGLE_OAUTH_CLIENT_SECRET|CORTEX_BOOTSTRAP_EMAIL) ;;
    *) continue ;;
  esac
  grep -v "^${key}=" "$tmp" > "$tmp.n" || true
  printf '%s=%s\n' "$key" "$val" >> "$tmp.n"
  mv -f "$tmp.n" "$tmp"
  n=$((n+1))
done
install -o root -g root -m 0600 "$tmp" "$ENV_FILE"
rm -f "$tmp" "$tmp.n"
echo "    updated $n setting(s)"
REMOTE_SCRIPT

SENT=()
PAYLOAD=""
for v in "${FORWARD_VARS[@]}"; do
  val="${!v:-}"
  [ -n "$val" ] || continue
  case "$val" in *$'\n'*) echo "warning: $v contains a newline; skipped" >&2; continue ;; esac
  PAYLOAD+="${v}=${val}"$'\n'
  SENT+=("$v")
done
if [ ${#SENT[@]} -eq 0 ]; then
  echo "    no settings found in your environment (expected GOOGLE_OAUTH_CLIENT_ID etc.); leaving the server's values"
  PAYLOAD=""
else
  echo "    sending: ${SENT[*]}"
fi
printf '%s' "$PAYLOAD" | rssh "bash -c $(printf '%q' "$REMOTE")"

echo "==> preflight: rollback unit and required settings"
read -r -d '' REMOTE_PREFLIGHT <<'PREFLIGHT_SCRIPT' || true
set -eu
ENV_FILE=/etc/cortex/cortex.env
miss=""
[ -f /etc/systemd/system/cortex.service.prev ] || miss="$miss cortex.service.prev"
for k in CORTEX_SECRET_KEY CORTEX_PUBLIC_URL; do
  grep -q "^${k}=." "$ENV_FILE" || miss="$miss $k"
done
if [ -n "$miss" ]; then
  echo "missing on server:$miss" >&2
  exit 5
fi
PREFLIGHT_SCRIPT
if ! rssh "bash -c $(printf '%q' "$REMOTE_PREFLIGHT")"; then
  echo "preflight failed: the service was not touched. Re-run 'setup-server.sh --app-host <fqdn>' on the server (writes the service unit, CORTEX_SECRET_KEY and CORTEX_PUBLIC_URL), then retry." >&2
  exit 1
fi

ROLLBACK_HINT="scripts/rollback-app.sh --fqdn ${FQDN} --dump ${DUMP_PATH} ${TARGET}"

echo "==> stopping cortex.service, taking the final pre-upgrade dump"
read -r -d '' REMOTE_CUTOVER <<'CUTOVER_SCRIPT' || true
set -eu
DUMP_PATH="$1"
set -a; . /etc/cortex/cortex.env; set +a
systemctl stop cortex.service 2>/dev/null || true
install -d -m 0700 "$(dirname "$DUMP_PATH")"
if [ -e "$DUMP_PATH" ]; then
  echo "DUMP_EXISTS" >&2
  exit 6
fi
if ! pg_dump -Fc -f "$DUMP_PATH.tmp" "$CORTEX_DATABASE_URL"; then
  rm -f "$DUMP_PATH.tmp"
  echo "DUMP_FAILED" >&2
  exit 3
fi
mv -f "$DUMP_PATH.tmp" "$DUMP_PATH"
chmod 0600 "$DUMP_PATH"
echo "    dump: $DUMP_PATH"
if [ -f /usr/local/bin/cortex ]; then
  cp -p /usr/local/bin/cortex /usr/local/bin/cortex.prev
fi
mv -f /usr/local/bin/cortex.new /usr/local/bin/cortex
echo "    installed /usr/local/bin/cortex (previous kept as cortex.prev)"
CUTOVER_SCRIPT
rc=0
rssh "bash -c $(printf '%q' "$REMOTE_CUTOVER") cutover $(printf '%q' "$DUMP_PATH")" || rc=$?
if [ "$rc" -eq 3 ]; then
  echo "pg_dump failed: nothing was changed except stopping the service." >&2
  echo "Start it again with: ssh ${TARGET} systemctl start cortex.service" >&2
  exit 1
elif [ "$rc" -eq 6 ]; then
  echo "${DUMP_PATH} already exists and is never overwritten: nothing was changed except stopping the service." >&2
  echo "Start it again with: ssh ${TARGET} systemctl start cortex.service" >&2
  exit 1
elif [ "$rc" -ne 0 ]; then
  echo "cutover failed (exit ${rc}). Roll back with: ${ROLLBACK_HINT}" >&2
  exit 1
fi

echo "==> cortex migrate --timeout 30m"
if ! rssh 'set -a; . /etc/cortex/cortex.env; set +a; /usr/local/bin/cortex migrate --timeout 30m'; then
  echo "migrate failed; the new binary was NOT started (cortex.service stays stopped)." >&2
  echo "Roll back with: ${ROLLBACK_HINT}" >&2
  exit 1
fi

echo "==> starting cortex.service"
if ! rssh 'systemctl daemon-reload && systemctl start cortex.service && sleep 2 && systemctl is-active cortex.service' >/dev/null; then
  echo "cortex.service is not running; on the server: journalctl -u cortex -n 50" >&2
  echo "Roll back with: ${ROLLBACK_HINT}" >&2
  exit 1
fi

echo "==> checking https://${FQDN}/healthz"
HEALTH=""
for _ in $(seq 1 30); do
  if HEALTH="$(curl -fsS --max-time 10 "https://${FQDN}/healthz" 2>/dev/null)"; then break; fi
  HEALTH=""; sleep 2
done
if [ -z "$HEALTH" ]; then
  echo "healthz did not answer. Check DNS for ${FQDN}, 'journalctl -u caddy' and 'journalctl -u cortex' on the server." >&2
  echo "Roll back with: ${ROLLBACK_HINT}" >&2
  exit 1
fi
echo "    $HEALTH"
CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST -H 'Content-Type: application/json' -d '{}' "https://${FQDN}/mcp" || true)"
if [ "$CODE" != "401" ]; then
  echo "WARNING: unauthenticated POST /mcp returned ${CODE}, expected 401: auth may not be enforced" >&2
  exit 1
fi
echo "    auth enforced (unauthenticated /mcp -> 401)"

echo
echo "MCP URL: https://${FQDN}/mcp"
if [ "$SHOW_TOKEN" -eq 1 ]; then
  TOKEN="$(rssh "sed -n 's/^CORTEX_AUTH_TOKEN=//p' /etc/cortex/cortex.env")"
  echo "Token:   ${TOKEN}"
else
  echo "Token:   CORTEX_AUTH_TOKEN in /etc/cortex/cortex.env on ${TARGET#*@} (re-run with --show-token to print it)"
fi
echo "Clients send:  Authorization: Bearer <token>"
