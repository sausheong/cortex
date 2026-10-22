#!/usr/bin/env bash
# Container smoke test for deploy-app.sh and rollback-app.sh. ssh, scp, go, curl,
# systemctl and pg_dump are replaced by logging shims, so nothing here
# needs a real server; the rollback runs against a real PostgreSQL with pgvector installed in the
# container. Needs Docker and network access (apt). Usage: scripts/test-deploy-app.sh [image]
set -euo pipefail
IMAGE="${1:-ubuntu:24.04}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

docker run --rm -v "$DIR:/scripts:ro" "$IMAGE" bash -euo pipefail -c '
pass=0
ok() { echo "PASS: $1"; pass=$((pass+1)); }
fail() { echo "FAIL: $1" >&2; exit 1; }
check() { local d="$1"; shift; if "$@"; then ok "$d"; else fail "$d"; fi; }

echo "installing postgresql + pgvector"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq sudo postgresql postgresql-16-pgvector >/dev/null
pg_ctlcluster 16 main start
until sudo -u postgres pg_isready -q; do sleep 0.5; done
sudo -u postgres psql -v ON_ERROR_STOP=1 -q -c "CREATE ROLE cortex LOGIN PASSWORD '"'"'cpw'"'"'" -c "CREATE DATABASE cortex OWNER cortex"
sudo -u postgres psql -v ON_ERROR_STOP=1 -q -d cortex -c "CREATE EXTENSION vector"
DBURL=postgres://cortex:cpw@127.0.0.1/cortex
cq() { psql -Atq -v ON_ERROR_STOP=1 "$DBURL" -c "$1"; }

SHIM=/tmp/shim; mkdir -p $SHIM
cat > $SHIM/ssh <<"EOS"
#!/bin/bash
echo "ssh $*" >> /tmp/ssh.log
case " $* " in *" -O "*) exit 0 ;; esac
while [ $# -gt 0 ]; do case "$1" in -o) shift 2 ;; -*) shift ;; *) break ;; esac; done
shift # target
exec bash -c "$*"
EOS
cat > $SHIM/scp <<"EOS"
#!/bin/bash
args=(); while [ $# -gt 0 ]; do case "$1" in -o) shift 2 ;; -*) shift ;; *) args+=("$1"); shift ;; esac; done
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
chmod +x $SHIM/*
export PATH=$SHIM:$PATH

reset_server() {
  rm -rf /etc/cortex /root/cortex /usr/local/bin/cortex* /etc/systemd/system/cortex.service* /tmp/calls.log /tmp/migrate.log /run/cortex-deploy.lock
  mkdir -p /etc/cortex /etc/systemd/system /usr/local/bin
  printf "CORTEX_DATABASE_URL=$DBURL\nCORTEX_AUTH_TOKEN=tok\nCORTEX_SECRET_KEY=sk\nCORTEX_PUBLIC_URL=https://cortex.test\nOPENAI_API_KEY=oa\n" > /etc/cortex/cortex.env
  printf "#!/bin/sh\necho old\n" > /usr/local/bin/cortex; chmod +x /usr/local/bin/cortex
  echo "ExecStart=/usr/local/bin/cortex serve" > /etc/systemd/system/cortex.service
  echo "ExecStart=/usr/local/bin/cortex mcp --transport http" > /etc/systemd/system/cortex.service.prev
  : > /tmp/calls.log
}
envval() { sed -n "s/^$1=//p" /etc/cortex/cortex.env | tail -n1; }
dump_file() { ls -1 /root/cortex/pre-multiuser-*.dump 2>/dev/null | sort | tail -n1; }
line_of() { grep -n "$1" /tmp/calls.log | head -n1 | cut -d: -f1; }

printf "GOOGLE_OAUTH_CLIENT_ID=gid-123\nGOOGLE_OAUTH_CLIENT_SECRET=\"topsecret-xyz\"\nCORTEX_BOOTSTRAP_EMAIL=owner@example.com\nOPENAI_API_KEY=gov-key\nOPENAI_BASE_URL=https://gov.example\nLLM_MODEL=m\nEMBEDDING_API_KEY=ek\nANTHROPIC_API_KEY=ak\n" > /.env

# --- success path
reset_server
/scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out1.log 2>&1 || { cat /tmp/out1.log; fail "deploy succeeds"; }
ok "deploy succeeds"
check "stop before dump" test "$(line_of "systemctl stop cortex.service")" -lt "$(line_of "pg_dump")"
check "dump is custom format to a timestamped path" grep -Eq "pg_dump -Fc -f /root/cortex/pre-multiuser-[0-9]{8}T[0-9]{6}Z[.]dump[.]tmp" /tmp/calls.log
check "dump file in place" test "$(cat "$(dump_file)")" = DUMP
check "deploy lock released" test ! -e /run/cortex-deploy.lock
check "ssh is multiplexed" bash -c "grep -q ControlMaster=auto /tmp/ssh.log && grep -q ControlPersist=600 /tmp/ssh.log && grep -q ControlPath=/tmp/cx-%C /tmp/ssh.log"
check "start after migrate" bash -c "grep -q \"^migrate --timeout 30m\" /tmp/migrate.log && test \"$(line_of "pg_dump")\" -lt \"$(line_of "systemctl start cortex.service")\""
check "daemon-reload before start" test "$(line_of "systemctl daemon-reload")" -lt "$(line_of "systemctl start cortex.service")"
check "single start, no restart" bash -c "test \"$(grep -c "start cortex.service" /tmp/calls.log)\" = 1 && ! grep -q restart /tmp/calls.log"
check "previous binary kept" bash -c "grep -q old /usr/local/bin/cortex.prev"
check "new binary installed" bash -c "[ \"\$(/usr/local/bin/cortex)\" = new ]"
check "google id forwarded" test "$(envval GOOGLE_OAUTH_CLIENT_ID)" = gid-123
check "google secret forwarded" test "$(envval GOOGLE_OAUTH_CLIENT_SECRET)" = topsecret-xyz
check "bootstrap email forwarded" test "$(envval CORTEX_BOOTSTRAP_EMAIL)" = owner@example.com
check "provider settings not forwarded" bash -c "! grep -Eq \"^(OPENAI_BASE_URL|LLM_MODEL|EMBEDDING_API_KEY|ANTHROPIC_API_KEY)=\" /etc/cortex/cortex.env"
check "existing OPENAI_API_KEY on server untouched" test "$(envval OPENAI_API_KEY)" = oa
check "secret never echoed" bash -c "! grep -q topsecret-xyz /tmp/out1.log"
check "existing env kept" test "$(envval CORTEX_AUTH_TOKEN)" = tok

# --- rollback restores binary, unit and a REAL database (vector extension + data)
DUMPF="$(dump_file)"
cq "CREATE TABLE items (id int PRIMARY KEY, v vector(3)); INSERT INTO items VALUES (1,ARRAY[1,0,0]::vector),(2,ARRAY[0,1,0]::vector),(3,ARRAY[0,0,1]::vector); CREATE TABLE notes (id serial PRIMARY KEY, body text); INSERT INTO notes (body) VALUES (md5(1::text)),(md5(2::text))"
rm -f "$DUMPF"
/usr/bin/pg_dump -Fc -f "$DUMPF" "$DBURL"; chmod 0600 "$DUMPF"
# State written after the dump: must be lost; a table the dump does not know must vanish.
cq "INSERT INTO items VALUES (4,ARRAY[1,1,1]::vector); CREATE TABLE migrated_later (id int); INSERT INTO migrated_later VALUES (1)"
printf "ExecStart=/usr/local/bin/cortex serve --addr x\n" > /etc/systemd/system/cortex.service
: > /tmp/calls.log
/scripts/rollback-app.sh --fqdn cortex.test root@cortex.test >/tmp/out2.log 2>&1 || { cat /tmp/out2.log; fail "rollback succeeds"; }
ok "rollback succeeds"
check "rollback restored old binary" bash -c "[ \"\$(/usr/local/bin/cortex)\" = old ]"
check "rollback restored old unit" grep -q "mcp --transport http" /etc/systemd/system/cortex.service
check "rollback used the newest timestamped dump" grep -q "using dump $DUMPF" /tmp/out2.log
check "rollback stop, reload, start" test "$(line_of "systemctl stop")" -lt "$(line_of "systemctl daemon-reload")" -a "$(line_of "systemctl daemon-reload")" -lt "$(line_of "systemctl start cortex.service")"
check "vector extension exists after rollback" test "$(cq "SELECT count(*) FROM pg_extension WHERE extname = '"'"'vector'"'"'")" = 1
check "items row count restored" test "$(cq "SELECT count(*) FROM items")" = 3
check "notes row count restored" test "$(cq "SELECT count(*) FROM notes")" = 2
check "post-dump table is gone" test "$(cq "SELECT count(*) FROM pg_tables WHERE tablename = '"'"'migrated_later'"'"'")" = 0
check "vector search works" test "$(cq "SELECT id FROM items ORDER BY v <-> ARRAY[0.9,0.1,0]::vector LIMIT 1")" = 1
check "tables owned by cortex" test "$(cq "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tableowner <> current_user")" = 0
check "cortex can write and run DDL" cq "INSERT INTO items VALUES (9,ARRAY[2,2,2]::vector); INSERT INTO notes (body) VALUES (md5(3::text)); CREATE TABLE ddl_ok (id int); DROP TABLE ddl_ok"
check "cortex write is visible" test "$(cq "SELECT count(*) FROM items")" = 4
check "rollback lock released" test ! -e /run/cortex-deploy.lock
# A second rollback over the now-populated DB must also work (schema reset again).
/scripts/rollback-app.sh --fqdn cortex.test root@cortex.test >/tmp/out2b.log 2>&1 || { cat /tmp/out2b.log; fail "second rollback succeeds"; }
check "second rollback restores the same rows" test "$(cq "SELECT count(*) FROM items")" = 3

# --- deploy refuses concurrent runs and never overwrites a dump
reset_server
mkdir /run/cortex-deploy.lock
if /scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out8.log 2>&1; then fail "deploy refuses while the lock is held"; else ok "deploy refuses while the lock is held"; fi
check "lock message printed" grep -q "holds /run/cortex-deploy.lock" /tmp/out8.log
check "foreign lock left in place" test -d /run/cortex-deploy.lock
check "locked deploy changed nothing" bash -c "[ \"\$(/usr/local/bin/cortex)\" = old ] && ! grep -q stop /tmp/calls.log"
if /scripts/rollback-app.sh --fqdn cortex.test root@cortex.test >/tmp/out8b.log 2>&1; then fail "rollback refuses while the lock is held"; else ok "rollback refuses while the lock is held"; fi
rmdir /run/cortex-deploy.lock
mkdir -p /root/cortex; echo PRECIOUS > /root/cortex/keep.dump
if DUMP_PATH=/root/cortex/keep.dump /scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out9.log 2>&1; then fail "deploy refuses to overwrite an existing dump"; else ok "deploy refuses to overwrite an existing dump"; fi
check "existing dump untouched" test "$(cat /root/cortex/keep.dump)" = PRECIOUS
check "lock released after refusal" test ! -e /run/cortex-deploy.lock
rm -f /root/cortex/keep.dump
/scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out10.log 2>&1 || { cat /tmp/out10.log; fail "first deploy"; }
sleep 1
/scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out11.log 2>&1 || { cat /tmp/out11.log; fail "second deploy"; }
check "each deploy keeps its own dump" test "$(ls -1 /root/cortex/pre-multiuser-*.dump | wc -l)" = 2

# --- rollback refuses when something is missing and changes nothing
rm -f /usr/local/bin/cortex.prev; : > /tmp/calls.log
if /scripts/rollback-app.sh --fqdn cortex.test root@cortex.test >/tmp/out3.log 2>&1; then fail "rollback without cortex.prev fails"; else ok "rollback without cortex.prev fails"; fi
check "nothing stopped or restored" bash -c "! grep -Eq stop /tmp/calls.log"

# --- failing migrate: new binary never started, rollback command printed
reset_server
if FAKE_MIGRATE_FAIL=1 /scripts/deploy-app.sh --fqdn cortex.test root@cortex.test >/tmp/out4.log 2>&1; then fail "failing migrate exits non-zero"; else ok "failing migrate exits non-zero"; fi
check "service never started" bash -c "! grep -q \"start cortex.service\" /tmp/calls.log"
check "rollback command printed" grep -q "rollback-app.sh" /tmp/out4.log
check "dump was taken before the failure" test -n "$(dump_file)"

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
