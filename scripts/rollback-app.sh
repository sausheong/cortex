#!/usr/bin/env bash
# Roll the app host back to the version before scripts/deploy-app.sh: restore the
# previous binary (cortex.prev) and service unit (cortex.service.prev), restore the
# database from the pre-upgrade dump, restart and check /healthz.
#
# Usage: rollback-app.sh [--fqdn HOST] [--dump PATH] <ssh-target>
#   <ssh-target>   e.g. root@cortex.example.com
#   --fqdn HOST    public host name (default: the host part of <ssh-target>)
#   --dump PATH    remote pg_dump -Fc file (default /root/cortex/pre-multiuser.dump)
#
# Everything written after the dump was taken is lost. Nothing is changed unless the
# previous binary, the previous unit and the dump all exist on the server.
# Environment: SSH_OPTS (extra ssh options, e.g. "-o BatchMode=yes").
set -euo pipefail

FQDN=""
DUMP="/root/cortex/pre-multiuser.dump"
TARGET=""
while [ $# -gt 0 ]; do
  case "$1" in
    --fqdn) FQDN="${2:?--fqdn needs a value}"; shift 2 ;;
    --dump) DUMP="${2:?--dump needs a value}"; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    -*) echo "unknown argument: $1" >&2; exit 2 ;;
    *) [ -z "$TARGET" ] || { echo "only one ssh target allowed" >&2; exit 2; }; TARGET="$1"; shift ;;
  esac
done
[ -n "$TARGET" ] || { echo "usage: $0 [--fqdn HOST] [--dump PATH] <ssh-target>" >&2; exit 2; }
if [ -z "$FQDN" ]; then
  FQDN="${TARGET#*@}"
fi
if ! printf '%s' "$FQDN" | grep -Eq '^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$' \
   || printf '%s' "$FQDN" | grep -Eq '^[0-9.]+$'; then
  echo "cannot derive a host name from '$TARGET'; pass --fqdn HOST" >&2; exit 2
fi
case "$DUMP" in
  /*) ;;
  *) echo "--dump must be an absolute path on the server" >&2; exit 2 ;;
esac

# shellcheck disable=SC2206
SSH_EXTRA=(${SSH_OPTS:-})
# shellcheck disable=SC2029
rssh() { ssh ${SSH_EXTRA[@]+"${SSH_EXTRA[@]}"} "$TARGET" "$@"; }

read -r -d '' REMOTE <<'ROLLBACK_SCRIPT' || true
set -eu
DUMP="$1"
for f in /usr/local/bin/cortex.prev /etc/systemd/system/cortex.service.prev "$DUMP"; do
  [ -f "$f" ] || { echo "missing $f: cannot roll back" >&2; exit 4; }
done
set -a; . /etc/cortex/cortex.env; set +a
systemctl stop cortex.service 2>/dev/null || true
cp -p /usr/local/bin/cortex.prev /usr/local/bin/cortex.restore
mv -f /usr/local/bin/cortex.restore /usr/local/bin/cortex
cp -p /etc/systemd/system/cortex.service.prev /etc/systemd/system/cortex.service.restore
mv -f /etc/systemd/system/cortex.service.restore /etc/systemd/system/cortex.service
echo "    restored the previous binary and service unit"
pg_restore --clean --if-exists --no-owner -d "$CORTEX_DATABASE_URL" "$DUMP"
echo "    restored the database from $DUMP"
systemctl daemon-reload
systemctl start cortex.service
sleep 2
systemctl is-active cortex.service >/dev/null
ROLLBACK_SCRIPT

echo "==> rolling back ${TARGET#*@} (database from ${DUMP})"
rc=0
rssh "bash -c $(printf '%q' "$REMOTE") rollback $(printf '%q' "$DUMP")" || rc=$?
if [ "$rc" -eq 4 ]; then
  echo "nothing was changed." >&2
  exit 1
elif [ "$rc" -ne 0 ]; then
  echo "rollback failed part-way (exit ${rc}); check the server state by hand ('journalctl -u cortex -n 50')." >&2
  exit 1
fi

echo "==> checking https://${FQDN}/healthz"
HEALTH=""
for _ in $(seq 1 30); do
  if HEALTH="$(curl -fsS --max-time 10 "https://${FQDN}/healthz" 2>/dev/null)"; then break; fi
  HEALTH=""; sleep 2
done
if [ -z "$HEALTH" ]; then
  echo "healthz did not answer after the rollback. Check 'journalctl -u cortex' on the server." >&2
  exit 1
fi
echo "    $HEALTH"
echo "rolled back."
