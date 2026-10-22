#!/usr/bin/env bash
# Container smoke test for deploy-app.sh and rollback-app.sh. ssh, scp, go, curl,
# systemctl, pg_dump and pg_restore are replaced by logging shims, so nothing here
# needs a real server. Needs Docker. Usage: scripts/test-deploy-app.sh [image]
set -euo pipefail
IMAGE="${1:-ubuntu:24.04}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

docker run --rm -v "$DIR:/scripts:ro" "$IMAGE" bash -euo pipefail -c '
pass=0
ok() { echo "PASS: $1"; pass=$((pass+1)); }
fail() { echo "FAIL: $1" >&2; exit 1; }
check() { local d="$1"; shift; if "$@"; then ok "$d"; else fail "$d"; fi; }

SHIM=/tmp/shim; mkdir -p $SHIM
cat > $SHIM/ssh <<"EOS"
#!/bin/bash
while [ $# -gt 0 ]; do case "$1" in -o) shift 2 ;; -*) shift ;; *) break ;; esac; done
shift # target
exec bash -c "$*"
EOS
cat > $SHIM/scp <<"EOS"
#!/bin/bash
args=(); for a in "$@"; do case "$a" in -*) ;; *) args+=("$a") ;; esac; done
cp "${args[0]}" "${args[1]#*:}"
EOS
cat > $SHIM/go <<"EOS"
#!/bin/bash
out=""; while [ $# -gt 0 ]; do if [ "$1" = -o ]; then out="$2"; shift; fi; shift; done
cat > "$out" <<"BIN"
#!/bin/sh
case "$1" in
  migrate) echo "$@" >> /tmp/migrate.log; [ -z "${FAKE_MIGRATE_FAIL:-}" ] || { echo "migrate boom" >&2; exit 1; }; echo "migrations applied: [1 2 3]" ;;
  *) echo new ;;
esac
BIN
chmod +x "$out"
EOS
cat > $SHIM/curl <<"EOS"
#!/bin/bash
case "$*" in *http_code*) printf 401 ;; *) echo "{\"status\":\"ok\"}" ;; esac
EOS
cat > $SHIM/systemctl <<"EOS"
#!/bin/bash
echo "systemctl $*" >> /tmp/calls.log
EOS
cat > $SHIM/pg_dump <<"EOS"
#!/bin/bash
echo "pg_dump $*" >> /tmp/calls.log
[ -z "${FAKE_DUMP_FAIL:-}" ] || exit 1
while [ $# -gt 0 ]; do if [ "$1" = -f ]; then echo DUMP > "$2"; fi; shift; done
EOS
cat > $SHIM/pg_restore <<"EOS"
#!/bin/bash
echo "pg_restore $*" >> /tmp/calls.log
EOS
cat > $SHIM/psql <<"EOS"
#!/bin/bash
echo "psql $*" >> /tmp/calls.log
EOS
chmod +x $SHIM/*
export PATH=$SHIM:$PATH

reset_server() {
  rm -rf /etc/cortex /root/cortex /usr/local/bin/cortex* /etc/systemd/system/cortex.service* /tmp/calls.log /tmp/migrate.log
  mkdir -p /etc/cortex /etc/systemd/system /usr/local/bin
  printf "CORTEX_DATABASE_URL=postgres://u:p@localhost/cortex\nCORTEX_AUTH_TOKEN=tok\nCORTEX_SECRET_KEY=sk\nCORTEX_PUBLIC_URL=https://cortex.test\nOPENAI_API_KEY=oa\n" > /etc/cortex/cortex.env
  printf "#!/bin/sh\necho old\n" > /usr/local/bin/cortex; chmod +x /usr/local/bin/cortex
  echo "ExecStart=/usr/local/bin/cortex serve" > /etc/systemd/system/cortex.service
  echo "ExecStart=/usr/local/bin/cortex mcp --transport http" > /etc/systemd/system/cortex.service.prev
  : > /tmp/calls.log
}
envval() { sed -n "s/^$1=//p" /etc/cortex/cortex.env | tail -n1; }
line_of() { grep -n "$1" /tmp/calls.log | head -n1 | cut -d: -f1; }

printf "GOOGLE_OAUTH_CLIENT_ID=gid-123\nGOOGLE_OAUTH_CLIENT_SECRET=topsecret-xyz\nCORTEX_BOOTSTRAP_EMAIL=owner@example.com\n" > /.env

# --- success path
reset_server
/scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out1.log 2>&1 || { cat /tmp/out1.log; fail "deploy succeeds"; }
ok "deploy succeeds"
check "stop before dump" test "$(line_of "systemctl stop cortex.service")" -lt "$(line_of "pg_dump")"
check "dump is custom format to the default path" grep -q "pg_dump -Fc -f /root/cortex/pre-multiuser.dump.tmp" /tmp/calls.log
check "dump file in place" test "$(cat /root/cortex/pre-multiuser.dump)" = DUMP
check "start after migrate" bash -c "grep -q \"^migrate --timeout 30m\" /tmp/migrate.log && test \"$(line_of "pg_dump")\" -lt \"$(line_of "systemctl start cortex.service")\""
check "daemon-reload before start" test "$(line_of "systemctl daemon-reload")" -lt "$(line_of "systemctl start cortex.service")"
check "single start, no restart" bash -c "test \"$(grep -c "start cortex.service" /tmp/calls.log)\" = 1 && ! grep -q restart /tmp/calls.log"
check "previous binary kept" bash -c "grep -q old /usr/local/bin/cortex.prev"
check "new binary installed" bash -c "[ \"\$(/usr/local/bin/cortex)\" = new ]"
check "google id forwarded" test "$(envval GOOGLE_OAUTH_CLIENT_ID)" = gid-123
check "google secret forwarded" test "$(envval GOOGLE_OAUTH_CLIENT_SECRET)" = topsecret-xyz
check "bootstrap email forwarded" test "$(envval CORTEX_BOOTSTRAP_EMAIL)" = owner@example.com
check "secret never echoed" bash -c "! grep -q topsecret-xyz /tmp/out1.log"
check "existing env kept" test "$(envval CORTEX_AUTH_TOKEN)" = tok

# --- rollback restores binary, unit and dump
printf "ExecStart=/usr/local/bin/cortex serve --addr x\n" > /etc/systemd/system/cortex.service
: > /tmp/calls.log
/scripts/rollback-app.sh --fqdn cortex.test root@cortex.test >/tmp/out2.log 2>&1 || { cat /tmp/out2.log; fail "rollback succeeds"; }
ok "rollback succeeds"
check "rollback restored old binary" bash -c "[ \"\$(/usr/local/bin/cortex)\" = old ]"
check "rollback restored old unit" grep -q "mcp --transport http" /etc/systemd/system/cortex.service
check "rollback resets the schema before restoring" test "$(line_of "psql .*DROP SCHEMA public CASCADE")" -lt "$(line_of "pg_restore")"
check "rollback pg_restore from the dump" grep -Eq "pg_restore .*/root/cortex/pre-multiuser.dump" /tmp/calls.log
check "rollback stop, reload, start" test "$(line_of "systemctl stop")" -lt "$(line_of "systemctl daemon-reload")" -a "$(line_of "systemctl daemon-reload")" -lt "$(line_of "systemctl start cortex.service")"

# --- rollback refuses when something is missing and changes nothing
rm -f /usr/local/bin/cortex.prev; : > /tmp/calls.log
if /scripts/rollback-app.sh --fqdn cortex.test root@cortex.test >/tmp/out3.log 2>&1; then fail "rollback without cortex.prev fails"; else ok "rollback without cortex.prev fails"; fi
check "nothing stopped or restored" bash -c "! grep -Eq \"stop|pg_restore\" /tmp/calls.log"

# --- failing migrate: new binary never started, rollback command printed
reset_server
if FAKE_MIGRATE_FAIL=1 /scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out4.log 2>&1; then fail "failing migrate exits non-zero"; else ok "failing migrate exits non-zero"; fi
check "service never started" bash -c "! grep -q \"start cortex.service\" /tmp/calls.log"
check "rollback command printed" grep -q "rollback-app.sh" /tmp/out4.log
check "dump was taken before the failure" test -f /root/cortex/pre-multiuser.dump

# --- failing dump: aborts before touching the binary
reset_server
if FAKE_DUMP_FAIL=1 /scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out5.log 2>&1; then fail "failing dump exits non-zero"; else ok "failing dump exits non-zero"; fi
check "old binary untouched after dump failure" bash -c "[ \"\$(/usr/local/bin/cortex)\" = old ]"
check "no migrate after dump failure" test ! -e /tmp/migrate.log
check "no cortex.prev after dump failure" test ! -e /usr/local/bin/cortex.prev

# --- preflight: missing rollback unit / secrets abort before the cutover
reset_server
rm -f /etc/systemd/system/cortex.service.prev
if /scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out6.log 2>&1; then fail "preflight rejects missing cortex.service.prev"; else ok "preflight rejects missing cortex.service.prev"; fi
check "preflight names the missing unit" grep -q "cortex.service.prev" /tmp/out6.log
check "service not stopped by failed preflight" bash -c "! grep -q stop /tmp/calls.log"
reset_server
sed -i "/^CORTEX_SECRET_KEY=/d" /etc/cortex/cortex.env
if /scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out7.log 2>&1; then fail "preflight rejects missing secret key"; else ok "preflight rejects missing secret key"; fi
check "preflight names CORTEX_SECRET_KEY" grep -q "CORTEX_SECRET_KEY" /tmp/out7.log

echo "ALL $pass CHECKS PASSED"
'
