#!/usr/bin/env bash
# Container smoke test for setup-server.sh --app-host (needs Docker, network).
# Usage: scripts/test-setup-server.sh [image]   (default ubuntu:24.04)
set -euo pipefail
IMAGE="${1:-ubuntu:24.04}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

docker run --rm -v "$DIR:/scripts:ro" "$IMAGE" bash -euo pipefail -c '
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq sudo iproute2 >/dev/null
HOST=cortex.test.invalid
pass=0
ok() { echo "PASS: $1"; pass=$((pass+1)); }
fail() { echo "FAIL: $1" >&2; exit 1; }
check() { local d="$1"; shift; if "$@"; then ok "$d"; else fail "$d"; fi; }
envval() { sed -n "s/^$1=//p" /etc/cortex/cortex.env | tail -n1; }
CONF=/etc/postgresql/17/main
psqlc() { su postgres -c "psql -qAt $*"; }

# --- argument validation
if /scripts/setup-server.sh --public-host x 2>/dev/null; then fail "--public-host rejected"; else ok "--public-host rejected"; fi
if /scripts/setup-server.sh --app-host $HOST --listen-ip 10.0.0.1 2>/dev/null; then fail "app-host+listen-ip rejected"; else ok "app-host+listen-ip rejected"; fi

# --- first run
/scripts/setup-server.sh --app-host $HOST --email ops@example.com --no-cron 2>&1 | tee /tmp/run1.log | tail -15
check "no ufw/systemd fatal" grep -q "cortex app host: https://$HOST/mcp" /tmp/run1.log
check "systemd warning printed" grep -q "systemd not running" /tmp/run1.log

check "listen_addresses=localhost" test "$(psqlc "-c \"show listen_addresses\"")" = localhost
check "conf has no ssl" bash -c "! grep -q \"^ssl\" $CONF/conf.d/cortex.conf"
check "hba has no hostssl/0.0.0.0" bash -c "! sed -n \"/BEGIN cortex-managed/,/END cortex-managed/p\" $CONF/pg_hba.conf | grep -Eq \"hostssl|0.0.0.0|::/0|reject\""
check "hba has localhost rules" bash -c "sed -n \"/BEGIN cortex-managed/,/END cortex-managed/p\" $CONF/pg_hba.conf | grep -q \"127.0.0.1/32\""
if command -v ss >/dev/null 2>&1; then
  check "5432 bound only to loopback" bash -c "! ss -ltn | awk \"\\\$4 ~ /:5432\\\$/ {print \\\$4}\" | grep -Ev \"^(127\\.0\\.0\\.1|\\[::1\\]):5432\\\$\" | grep -q ."
fi

check "env file mode 600 root" test "$(stat -c %a:%U /etc/cortex/cortex.env)" = 600:root
TOKEN="$(envval CORTEX_AUTH_TOKEN)"
check "token is 64 hex" bash -c "[[ $TOKEN =~ ^[0-9a-f]{64}$ ]]"
DSN="$(envval CORTEX_DATABASE_URL)"
check "DSN is local" bash -c "[[ \"$DSN\" == postgres://cortex:*@localhost:5432/cortex?sslmode=disable ]]"
check "DSN works" psql -qAt "$DSN" -c "select extversion from pg_extension where extname=\$\$vector\$\$"
check "OPENAI_API_KEY skeleton" grep -q "^OPENAI_API_KEY=\$" /etc/cortex/cortex.env
check "cortex user is nologin system user" bash -c "getent passwd cortex | grep -q nologin"

U=/etc/systemd/system
check "service ExecStart runs serve" grep -qx "ExecStart=/usr/local/bin/cortex serve --addr 127.0.0.1:8080" $U/cortex.service
SECRET="$(envval CORTEX_SECRET_KEY)"
check "secret key is 64 hex" bash -c "[[ $SECRET =~ ^[0-9a-f]{64}$ ]]"
check "public url set" test "$(envval CORTEX_PUBLIC_URL)" = "https://$HOST"
check "bootstrap email set" test "$(envval CORTEX_BOOTSTRAP_EMAIL)" = sausheong@gmail.com
check "no .prev on a fresh install" test ! -e $U/cortex.service.prev
check "service env file" grep -qx "EnvironmentFile=/etc/cortex/cortex.env" $U/cortex.service
check "service user" grep -qx "User=cortex" $U/cortex.service
check "service Restart" grep -qx "Restart=on-failure" $U/cortex.service
check "maintain exec" grep -qx "ExecStart=/usr/local/bin/cortex maintain" $U/cortex-maintain.service
check "maintain user" grep -qx "User=cortex" $U/cortex-maintain.service
check "timer 19:15 UTC" grep -Fqx "OnCalendar=*-*-* 19:15:00 UTC" $U/cortex-maintain.timer
check "Caddyfile site" grep -qx "$HOST {" /etc/caddy/Caddyfile
check "Caddyfile proxy" grep -q "reverse_proxy 127.0.0.1:8080" /etc/caddy/Caddyfile
check "Caddyfile email" grep -q "email ops@example.com" /etc/caddy/Caddyfile
check "caddy validates" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile

# --- idempotent re-run keeps secrets, preserves user-set values
echo "OPENAI_API_KEY=sk-test" >> /etc/cortex/cortex.env
sed -i "/^OPENAI_API_KEY=\$/d" /etc/cortex/cortex.env
/scripts/setup-server.sh --app-host $HOST --email ops@example.com --no-cron >/tmp/run2.log 2>&1
check "token preserved" test "$(envval CORTEX_AUTH_TOKEN)" = "$TOKEN"
check "DSN preserved" test "$(envval CORTEX_DATABASE_URL)" = "$DSN"
check "user value preserved" test "$(envval OPENAI_API_KEY)" = sk-test
check "secret key preserved" test "$(envval CORTEX_SECRET_KEY)" = "$SECRET"
check "single secret key line" test "$(grep -c "^CORTEX_SECRET_KEY=" /etc/cortex/cortex.env)" = 1
check "single token line" test "$(grep -c "^CORTEX_AUTH_TOKEN=" /etc/cortex/cortex.env)" = 1
check "env still mode 600" test "$(stat -c %a /etc/cortex/cortex.env)" = 600
check "no duplicate hba block" test "$(grep -c "BEGIN cortex-managed" $CONF/pg_hba.conf)" = 1
check "DSN still works" psql -qAt "$DSN" -c "select 1"

# --- convert a legacy public-TLS install
sed -i "s/^listen_addresses = .localhost./listen_addresses = \x27*\x27/" $CONF/conf.d/cortex.conf
printf "ssl = on\nssl_cert_file = \x27$CONF/ssl/server.crt\x27\n" >> $CONF/conf.d/cortex.conf
sed -i "/END cortex-managed/i hostssl cortex cortex 0.0.0.0/0 scram-sha-256" $CONF/pg_hba.conf
install -d $CONF/ssl; echo x > $CONF/ssl/server.crt; echo y > $CONF/ssl/server.key
install -d /etc/letsencrypt/renewal-hooks/deploy /etc/letsencrypt/renewal
printf "#!/bin/sh\n# Managed by cortex setup-server.sh\n" > /etc/letsencrypt/renewal-hooks/deploy/cortex-postgres.sh
echo "# renewal" > /etc/letsencrypt/renewal/$HOST.conf
sed -i "/^CORTEX_DATABASE_URL=/d" /etc/cortex/cortex.env
echo "alter role cortex password \$\$forgotten\$\$;" | su postgres -c "psql -q"
/scripts/setup-server.sh --app-host $HOST --email ops@example.com --no-cron >/tmp/run3.log 2>&1
check "legacy: hostssl rule gone" bash -c "! grep -q ^hostssl $CONF/pg_hba.conf"
check "legacy: ssl off" bash -c "! grep -q \"^ssl\" $CONF/conf.d/cortex.conf"
check "legacy: listen back to localhost" test "$(psqlc "-c \"show listen_addresses\"")" = localhost
check "legacy: ssl key copy removed" test ! -e $CONF/ssl/server.key
check "legacy: deploy hook removed" test ! -e /etc/letsencrypt/renewal-hooks/deploy/cortex-postgres.sh
check "legacy: renewal conf disabled" bash -c "test ! -e /etc/letsencrypt/renewal/$HOST.conf && test -e /etc/letsencrypt/renewal/$HOST.conf.cortex-disabled"
DSN2="$(envval CORTEX_DATABASE_URL)"
check "legacy: new DSN written" test -n "$DSN2"
check "legacy: new DSN works" psql -qAt "$DSN2" -c "select 1"
check "legacy: token preserved" test "$(envval CORTEX_AUTH_TOKEN)" = "$TOKEN"
check "legacy: api key preserved" test "$(envval OPENAI_API_KEY)" = sk-test

# --- upgrading a legacy unit: previous unit saved, cortex.service NOT restarted
SHIM=/tmp/shim; mkdir -p $SHIM /run/systemd/system
cat > $SHIM/systemctl <<"EOS"
#!/bin/sh
echo "$*" >> /tmp/systemctl.log
case "$1 $2" in
  "restart postgresql@17-main"|"restart postgresql") pg_ctlcluster 17 main restart ;;
  "reload postgresql@17-main"|"reload postgresql") pg_ctlcluster 17 main reload ;;
  "list-unit-files "*) exit 1 ;;
esac
exit 0
EOS
chmod +x $SHIM/systemctl
printf "[Service]\nExecStart=/usr/local/bin/cortex mcp --transport http --addr 127.0.0.1:8080\n" > $U/cortex.service
install -m 0755 /dev/null /usr/local/bin/cortex
: > /tmp/systemctl.log
PATH=$SHIM:$PATH /scripts/setup-server.sh --app-host $HOST --email ops@example.com --no-cron >/tmp/run4.log 2>&1
check "unit now runs serve" grep -qx "ExecStart=/usr/local/bin/cortex serve --addr 127.0.0.1:8080" $U/cortex.service
check "previous unit saved as .prev" grep -q "mcp --transport http" $U/cortex.service.prev
check "units enabled via systemd" grep -q "^enable cortex.service" /tmp/systemctl.log
check "cortex.service not restarted" bash -c "! grep -Eq \"^(restart|start|stop) cortex.service\" /tmp/systemctl.log"
check "secret key still preserved" test "$(envval CORTEX_SECRET_KEY)" = "$SECRET"
PATH=$SHIM:$PATH /scripts/setup-server.sh --app-host $HOST --email ops@example.com --no-cron >/tmp/run5.log 2>&1
check ".prev kept across an identical re-run" grep -q "mcp --transport http" $U/cortex.service.prev

echo "ALL $pass CHECKS PASSED"
'
