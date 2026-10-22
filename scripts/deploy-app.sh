#!/usr/bin/env bash
# Deploy the cortex binary and its secrets to an app host prepared with
# `setup-server.sh --app-host`. Run from your workstation (needs go, ssh, scp, curl).
#
# Usage: deploy-app.sh [--fqdn HOST] [--show-token] <ssh-target>
#   <ssh-target>   e.g. root@cortex.example.com
#   --fqdn HOST    public host name (default: the host part of <ssh-target>)
#   --show-token   also print CORTEX_AUTH_TOKEN (otherwise only says where it is)
#
# Steps: detect the server arch, cross-compile (GOOS=linux, CGO_ENABLED=0), install
# /usr/local/bin/cortex atomically, copy secrets/settings found in your environment
# into /etc/cortex/cortex.env (values travel over ssh stdin, never argv or output),
# restart the service and check https://<fqdn>/healthz.
# Environment: SSH_OPTS (extra ssh/scp options, e.g. "-o BatchMode=yes").
set -euo pipefail

# Variables copied from the local environment into the server env file when set.
FORWARD_VARS=(OPENAI_API_KEY OPENAI_BASE_URL ANTHROPIC_API_KEY ANTHROPIC_BASE_URL
  LLM_PROVIDER LLM_MODEL EMBEDDING_API_KEY EMBEDDING_BASE_URL EMBEDDING_MODEL EMBEDDING_DIMS)

FQDN=""
SHOW_TOKEN=0
TARGET=""
while [ $# -gt 0 ]; do
  case "$1" in
    --fqdn) FQDN="${2:?--fqdn needs a value}"; shift 2 ;;
    --show-token) SHOW_TOKEN=1; shift ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
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
# shellcheck disable=SC2206
SSH_EXTRA=(${SSH_OPTS:-})
# shellcheck disable=SC2029
rssh() { ssh ${SSH_EXTRA[@]+"${SSH_EXTRA[@]}"} "$TARGET" "$@"; }

echo "==> detecting server architecture"
case "$(rssh uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "unsupported server architecture" >&2; exit 1 ;;
esac
echo "    linux/${GOARCH}"

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
echo "==> building cortex (linux/${GOARCH}, CGO_ENABLED=0)"
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
  go build -trimpath -ldflags "-s -w" -o "$BUILD_DIR/cortex" ./cmd/cortex/)

echo "==> installing /usr/local/bin/cortex"
scp ${SSH_EXTRA[@]+"${SSH_EXTRA[@]}"} -q "$BUILD_DIR/cortex" "${TARGET}:/usr/local/bin/cortex.new"
rssh 'chmod 0755 /usr/local/bin/cortex.new && mv -f /usr/local/bin/cortex.new /usr/local/bin/cortex'

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
    OPENAI_API_KEY|OPENAI_BASE_URL|ANTHROPIC_API_KEY|ANTHROPIC_BASE_URL|LLM_PROVIDER|LLM_MODEL|EMBEDDING_API_KEY|EMBEDDING_BASE_URL|EMBEDDING_MODEL|EMBEDDING_DIMS) ;;
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
  echo "    no settings found in your environment (expected e.g. OPENAI_API_KEY); leaving the server's values"
  PAYLOAD=""
else
  echo "    sending: ${SENT[*]}"
fi
printf '%s' "$PAYLOAD" | rssh "bash -c $(printf '%q' "$REMOTE")"

echo "==> restarting cortex.service"
rssh 'systemctl daemon-reload && systemctl restart cortex.service && sleep 2 && systemctl is-active cortex.service' >/dev/null \
  || { echo "cortex.service is not running; on the server: journalctl -u cortex -n 50" >&2; exit 1; }

echo "==> checking https://${FQDN}/healthz"
HEALTH=""
for _ in $(seq 1 30); do
  if HEALTH="$(curl -fsS --max-time 10 "https://${FQDN}/healthz" 2>/dev/null)"; then break; fi
  HEALTH=""; sleep 2
done
if [ -z "$HEALTH" ]; then
  echo "healthz did not answer. Check DNS for ${FQDN}, 'journalctl -u caddy' and 'journalctl -u cortex' on the server." >&2
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
