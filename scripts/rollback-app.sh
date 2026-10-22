#!/usr/bin/env bash
# Roll the app host back to the version before scripts/deploy-app.sh: restore the
# previous binary (cortex.prev) and service unit (cortex.service.prev), restore the
# database from the pre-upgrade dump, restart and check /healthz.
#
# Usage: rollback-app.sh [--fqdn HOST] [--dump PATH] <ssh-target>
#   <ssh-target>   e.g. root@cortex.example.com
#   --fqdn HOST    public host name (default: the host part of <ssh-target>)
#   --dump PATH    remote pg_dump -Fc file (default: the newest
#                  /root/cortex/pre-multiuser-*.dump, else the legacy pre-multiuser.dump)
#
# Everything written after the dump was taken is lost. Nothing is changed unless the
# previous binary, the previous unit and the dump all exist on the server.
# The database reset and restore run as the postgres superuser (sudo -u postgres): the
# public schema is dropped and recreated, the vector extension is recreated before
# pg_restore, and objects are restored owned by the application role (the one in
# CORTEX_DATABASE_URL). Takes the same server-side lock as deploy-app.sh.
# ssh connections are multiplexed (ControlMaster) so the host's ssh rate limit is not hit.
# Environment: SSH_OPTS (extra ssh options, e.g. "-o BatchMode=yes").
set -euo pipefail

FQDN=""
DUMP=""
TARGET=""
while [ $# -gt 0 ]; do
  case "$1" in
    --fqdn) FQDN="${2:?--fqdn needs a value}"; shift 2 ;;
    --dump) DUMP="${2:?--dump needs a value}"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
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
  ""|/*) ;;
  *) echo "--dump must be an absolute path on the server" >&2; exit 2 ;;
esac

SSH_MUX=(-o ControlMaster=auto -o "ControlPath=/tmp/cx-%C" -o ControlPersist=600)
# shellcheck disable=SC2206
SSH_EXTRA=("${SSH_MUX[@]}" ${SSH_OPTS:-})
# shellcheck disable=SC2029
rssh() { ssh "${SSH_EXTRA[@]}" "$TARGET" "$@"; }

LOCKED=0
cleanup() {
  if [ "$LOCKED" -eq 1 ]; then rssh 'rmdir /run/cortex-deploy.lock' >/dev/null 2>&1 || true; fi
  ssh "${SSH_MUX[@]}" -O exit "$TARGET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

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

read -r -d '' REMOTE <<'ROLLBACK_SCRIPT' || true
set -eu
DUMP="$1"
if [ -z "$DUMP" ]; then
  # Newest timestamped pre-migration dump, else the legacy fixed name.
  DUMP="$(ls -1 /root/cortex/pre-multiuser-*.dump 2>/dev/null | sort | tail -n1 || true)"
  [ -n "$DUMP" ] || DUMP=/root/cortex/pre-multiuser.dump
fi
for f in /usr/local/bin/cortex.prev /etc/systemd/system/cortex.service.prev "$DUMP"; do
  [ -f "$f" ] || { echo "missing $f: cannot roll back" >&2; exit 4; }
done
echo "    using dump $DUMP"
set -a; . /etc/cortex/cortex.env; set +a
DBNAME="$(psql -Atq "$CORTEX_DATABASE_URL" -c 'select current_database()')"
DBROLE="$(psql -Atq "$CORTEX_DATABASE_URL" -c 'select current_user')"
[ -n "$DBNAME" ] && [ -n "$DBROLE" ] || { echo "cannot read database/role from CORTEX_DATABASE_URL" >&2; exit 4; }
systemctl stop cortex.service 2>/dev/null || true
cp -p /usr/local/bin/cortex.prev /usr/local/bin/cortex.restore
mv -f /usr/local/bin/cortex.restore /usr/local/bin/cortex
cp -p /etc/systemd/system/cortex.service.prev /etc/systemd/system/cortex.service.restore
mv -f /etc/systemd/system/cortex.service.restore /etc/systemd/system/cortex.service
echo "    restored the previous binary and service unit"
# The dump predates migrations 002/003, so --clean would leave their tables behind
# and a later migrate would fail. Reset the schema as the superuser (the application
# role cannot CREATE EXTENSION vector), recreate the extension, then restore into it.
cd /tmp
sudo -u postgres psql -v ON_ERROR_STOP=1 -q -d "$DBNAME" -v role="$DBROLE" <<'SQL'
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
ALTER SCHEMA public OWNER TO :"role";
GRANT USAGE ON SCHEMA public TO PUBLIC;
CREATE EXTENSION IF NOT EXISTS vector SCHEMA public;
SQL
# The extension already exists, so drop its COMMENT entry (restoring it can fail for a
# non-owner and is harmless to lose). The dump is read by root and piped, as the
# postgres user cannot read /root.
LIST="$(mktemp /tmp/cortex-restore-list.XXXXXX)"
trap 'rm -f "$LIST"' EXIT
pg_restore -l < "$DUMP" | grep -v 'COMMENT - EXTENSION' > "$LIST" || true
chmod 0644 "$LIST"
sudo -u postgres pg_restore --no-owner --role="$DBROLE" -L "$LIST" --exit-on-error -d "$DBNAME" < "$DUMP"
echo "    restored the database from $DUMP"
systemctl daemon-reload
systemctl start cortex.service
sleep 2
systemctl is-active cortex.service >/dev/null
ROLLBACK_SCRIPT

echo "==> rolling back ${TARGET#*@}"
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
