#!/usr/bin/env bash
# Provision a Debian/Ubuntu host (amd64 or arm64) as a cortex Postgres server.
# Idempotent. Run as root.
#
# Usage: setup-server.sh [--app-host FQDN [--email ADDR]] [--listen-ip IP]
#                        [--cert FILE --key FILE] [--no-cron]
#
#   (no access flags)    Postgres listens on localhost only.
#   --app-host FQDN      Run cortex on this same host behind HTTPS. Postgres stays on
#                        localhost (port 5432 is never exposed). Sets up a 'cortex'
#                        system user, /etc/cortex/cortex.env (database URL, auth token
#                        generated once and preserved, plus CORTEX_SECRET_KEY,
#                        CORTEX_PUBLIC_URL and CORTEX_BOOTSTRAP_EMAIL), systemd units for
#                        `cortex serve --addr 127.0.0.1:8080` (written, never restarted
#                        here; the old unit is kept as cortex.service.prev) and a
#                        nightly `cortex maintain` timer, Caddy (own Let's Encrypt cert,
#                        reverse proxy on 443) and ufw allowing only 22, 80 and 443.
#                        DNS for FQDN must already point here. Install the binary
#                        with scripts/deploy-app.sh.
#   --email ADDR         Let's Encrypt account email for Caddy (with --app-host).
#   --listen-ip IP       Private-network host (e.g. a Pi on a LAN): listen on
#                        localhost and IP only; scram-sha-256 from any source that
#                        can reach that interface (TLS-only if --cert/--key given).
#                        Cannot be combined with --app-host.
#   --cert F --key F     Postgres server certificate/key (LAN mode).
#   --no-cron            Skip the backup cron job.
set -euo pipefail

PG_VERSION=17
APP_HOST=""
LE_EMAIL=""
CERT_FILE=""
KEY_FILE=""
LISTEN_IP=""
INSTALL_CRON=1
APP_PORT=8080
ENV_DIR="/etc/cortex"
ENV_FILE="${ENV_DIR}/cortex.env"
CORTEX_BIN="/usr/local/bin/cortex"

while [ $# -gt 0 ]; do
  case "$1" in
    --app-host) APP_HOST="${2:?--app-host needs a value}"; shift 2 ;;
    --public-host) echo "--public-host was removed: Postgres is no longer exposed publicly. Use --app-host FQDN (see docs/ops/deploy.md)" >&2; exit 2 ;;
    --email) LE_EMAIL="${2:?--email needs a value}"; shift 2 ;;
    --cert) CERT_FILE="${2:?--cert needs a value}"; shift 2 ;;
    --key) KEY_FILE="${2:?--key needs a value}"; shift 2 ;;
    --listen-ip) LISTEN_IP="${2:?--listen-ip needs a value}"; shift 2 ;;
    --no-cron) INSTALL_CRON=0; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -n "$APP_HOST" ] && [ -n "$LISTEN_IP" ]; then
  echo "--app-host and --listen-ip are mutually exclusive" >&2; exit 2
fi
if [ -n "$CERT_FILE" ] || [ -n "$KEY_FILE" ]; then
  if [ -z "$CERT_FILE" ] || [ -z "$KEY_FILE" ]; then
    echo "--cert and --key must be given together" >&2; exit 2
  fi
  if [ ! -r "$CERT_FILE" ] || [ ! -r "$KEY_FILE" ]; then
    echo "cannot read --cert/--key files" >&2; exit 2
  fi
  if [ -n "$APP_HOST" ]; then
    echo "--cert/--key cannot be combined with --app-host (Postgres is localhost-only there; Caddy manages its own certificate)" >&2; exit 2
  fi
fi
if [ -n "$APP_HOST" ] && ! printf '%s' "$APP_HOST" | grep -Eq '^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$'; then
  echo "invalid --app-host '$APP_HOST'" >&2; exit 2
fi
if [ -n "$LISTEN_IP" ] && ! printf '%s' "$LISTEN_IP" | grep -Eq '^[0-9A-Fa-f:.]+$'; then
  echo "invalid --listen-ip '$LISTEN_IP'" >&2; exit 2
fi
if [ -n "$LE_EMAIL" ] && [ -z "$APP_HOST" ]; then
  echo "warning: --email is only used with --app-host; ignoring" >&2
fi
if [ -n "$LE_EMAIL" ] && ! printf '%s' "$LE_EMAIL" | grep -Eq '^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$'; then
  echo "invalid --email '$LE_EMAIL'" >&2; exit 2
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "must run as root" >&2; exit 1
fi

# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}" in
  debian|ubuntu) ;;
  *) echo "unsupported OS '${ID:-unknown}': Debian or Ubuntu required" >&2; exit 1 ;;
esac
ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
  amd64|arm64) ;;
  *) echo "unsupported architecture '$ARCH': amd64 or arm64 required" >&2; exit 1 ;;
esac
CODENAME="${VERSION_CODENAME:-}"
if [ -z "$CODENAME" ]; then
  echo "cannot determine VERSION_CODENAME" >&2; exit 1
fi

have_systemd() { command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; }

export DEBIAN_FRONTEND=noninteractive
# A previously added Caddy upstream repo may be unverifiable (see setup_app_host); it is re-added there.
rm -f /etc/apt/sources.list.d/caddy-stable.list

apt-get update -qq
apt-get install -y -qq ca-certificates curl gnupg lsb-release cron rclone >/dev/null

# --- PGDG repository ---
install -d /usr/share/postgresql-common/pgdg
if [ ! -f /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc ]; then
  curl -fsSL -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
    https://www.postgresql.org/media/keys/ACCC4CF8.asc
fi
echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt ${CODENAME}-pgdg main" \
  > /etc/apt/sources.list.d/pgdg.list
apt-get update -qq
# Avoid starting a default cluster before we have tuned it.
apt-get install -y -qq "postgresql-${PG_VERSION}" "postgresql-${PG_VERSION}-pgvector" >/dev/null

# --- Start the cluster (containers have no systemd) ---
CONF_DIR="/etc/postgresql/${PG_VERSION}/main"
if ! pg_lsclusters -h | grep -q "^${PG_VERSION} main .*online"; then
  pg_ctlcluster "$PG_VERSION" main start 2>/dev/null || service postgresql start
fi
for _ in $(seq 1 30); do
  su postgres -c "psql -qAt -c 'select 1'" >/dev/null 2>&1 && break
  sleep 1
done

# --- Swap if RAM < 2 GB ---
RAM_KB="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
RAM_MB=$((RAM_KB / 1024))
if [ "$RAM_MB" -lt 2048 ] && ! swapon --show=NAME --noheadings | grep -q .; then
  if [ ! -f /swapfile ]; then
    if ! { fallocate -l 2G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=2048 status=none; }; then
      echo "warning: could not create swapfile" >&2
    fi
    chmod 600 /swapfile
    mkswap /swapfile >/dev/null || true
  fi
  if swapon /swapfile 2>/dev/null; then
    grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
  else
    echo "warning: could not enable swap (container?)" >&2
  fi
fi

# --- Tuning ---
SHARED_MB=$((RAM_MB / 4))
EFFECTIVE_MB=$((RAM_MB * 60 / 100))
MAINT_MB=$((RAM_MB / 8))
[ "$MAINT_MB" -gt 1024 ] && MAINT_MB=1024
[ "$MAINT_MB" -lt 16 ] && MAINT_MB=16
[ "$SHARED_MB" -lt 16 ] && SHARED_MB=16
if [ "$RAM_MB" -lt 2048 ]; then WORK_MB=4; else WORK_MB=16; fi

SSL_DIR="$CONF_DIR/ssl"
install_certs() {
  install -d -o postgres -g postgres -m 0700 "$SSL_DIR"
  install -o postgres -g postgres -m 0644 "$1" "$SSL_DIR/server.crt"
  install -o postgres -g postgres -m 0600 "$2" "$SSL_DIR/server.key"
}

reload_postgres() {
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl reload "postgresql@${PG_VERSION}-main" || systemctl reload postgresql
  else
    pg_ctlcluster "$PG_VERSION" main reload
  fi
}

# --- Postgres TLS (LAN mode with --cert/--key only) ---
USE_SSL=0
if [ -n "$CERT_FILE" ]; then
  install_certs "$CERT_FILE" "$KEY_FILE"
  USE_SSL=1
fi

# --- Legacy cleanup: earlier versions exposed Postgres publicly with a certbot cert ---
if [ -n "$APP_HOST" ]; then
  LEGACY_HOOK=/etc/letsencrypt/renewal-hooks/deploy/cortex-postgres.sh
  if [ -f "$LEGACY_HOOK" ]; then
    echo "removing legacy certbot deploy hook and Postgres TLS key copy"
    rm -f "$LEGACY_HOOK" "$SSL_DIR/server.crt" "$SSL_DIR/server.key"
  fi
  # Caddy now owns port 80/443 and its own certificate; certbot standalone renewal
  # would conflict with it. Leave the certbot package installed but stop renewing.
  if [ -f "/etc/letsencrypt/renewal/${APP_HOST}.conf" ]; then
    mv "/etc/letsencrypt/renewal/${APP_HOST}.conf" "/etc/letsencrypt/renewal/${APP_HOST}.conf.cortex-disabled"
    echo "disabled certbot renewal for ${APP_HOST}"
  fi
  if have_systemd && systemctl list-unit-files certbot.timer >/dev/null 2>&1; then
    systemctl disable --now certbot.timer >/dev/null 2>&1 || true
  fi
fi

LISTEN="localhost"
[ -n "$LISTEN_IP" ] && LISTEN="localhost,${LISTEN_IP}"

install -d "$CONF_DIR/conf.d"
{
  cat <<CONF
# Managed by cortex setup-server.sh (RAM detected: ${RAM_MB} MB)
listen_addresses = '${LISTEN}'
max_connections = 20
shared_buffers = ${SHARED_MB}MB
effective_cache_size = ${EFFECTIVE_MB}MB
maintenance_work_mem = ${MAINT_MB}MB
work_mem = ${WORK_MB}MB
password_encryption = 'scram-sha-256'
CONF
  if [ "$USE_SSL" -eq 1 ]; then
    cat <<CONF
ssl = on
ssl_cert_file = '${SSL_DIR}/server.crt'
ssl_key_file = '${SSL_DIR}/server.key'
ssl_min_protocol_version = 'TLSv1.2'
CONF
  fi
} > "$CONF_DIR/conf.d/cortex.conf"
grep -Eq "^include_dir *= *'conf.d'" "$CONF_DIR/postgresql.conf" || \
  echo "include_dir = 'conf.d'" >> "$CONF_DIR/postgresql.conf"

# --- Access (pg_hba.conf): managed block is rewritten on every run ---
HBA="$CONF_DIR/pg_hba.conf"
HBA_TMP="$(mktemp)"
# Drop the current block and the legacy (pre-TLS) unmarked block.
sed -e '/^# BEGIN cortex-managed$/,/^# END cortex-managed$/d' \
    -e '/^# cortex-managed$/d' \
    -e '/^host cortex cortex /d' "$HBA" > "$HBA_TMP"
{
  echo "# BEGIN cortex-managed"
  echo "host cortex cortex 127.0.0.1/32 scram-sha-256"
  echo "host cortex cortex ::1/128 scram-sha-256"
  if [ -n "$LISTEN_IP" ]; then
    if [ "$USE_SSL" -eq 1 ]; then
      echo "hostssl cortex cortex 0.0.0.0/0 scram-sha-256"
      echo "hostssl cortex cortex ::/0 scram-sha-256"
      echo "hostnossl all all 0.0.0.0/0 reject"
      echo "hostnossl all all ::/0 reject"
    else
      echo "host cortex cortex 0.0.0.0/0 scram-sha-256"
      echo "host cortex cortex ::/0 scram-sha-256"
    fi
  fi
  echo "# END cortex-managed"
} >> "$HBA_TMP"
cat "$HBA_TMP" > "$HBA"
rm -f "$HBA_TMP"

# --- Env file helpers (app-host mode) ---
env_get() {
  [ -f "$ENV_FILE" ] || return 0
  sed -n "s/^$1=//p" "$ENV_FILE" | tail -n1
}
env_set() { # KEY VALUE: replace or append, keep everything else, atomic, mode 0600
  local tmp
  tmp="$(mktemp)"
  if [ -f "$ENV_FILE" ]; then grep -v "^$1=" "$ENV_FILE" > "$tmp" || true; fi
  printf '%s=%s\n' "$1" "$2" >> "$tmp"
  install -d -m 0755 "$ENV_DIR"
  install -o root -g root -m 0600 "$tmp" "$ENV_FILE"
  rm -f "$tmp"
}
env_add_if_missing() { # KEY VALUE
  if ! grep -q "^$1=" "$ENV_FILE" 2>/dev/null; then env_set "$1" "$2"; fi
}

# --- Role and database ---
PASSWORD_PRINTED=""
DSN_NEW_PASSWORD=""
ROLE_EXISTS="$(su postgres -c "psql -qAt -c \"select 1 from pg_roles where rolname='cortex'\"")"
if [ "$ROLE_EXISTS" != "1" ]; then
  PASSWORD="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)"
  printf "set password_encryption='scram-sha-256';\ncreate role cortex login nosuperuser password '%s';\n" "$PASSWORD" \
    | su postgres -c "psql -q -v ON_ERROR_STOP=1"
  if [ -n "$APP_HOST" ]; then DSN_NEW_PASSWORD="$PASSWORD"; else PASSWORD_PRINTED="$PASSWORD"; fi
elif [ -n "$APP_HOST" ] && [ -z "$(env_get CORTEX_DATABASE_URL)" ]; then
  # Role predates the env file (e.g. converting an older install): its password is
  # not recorded anywhere we can read, so set a fresh one and store it in the env file.
  PASSWORD="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)"
  printf "set password_encryption='scram-sha-256';\nalter role cortex password '%s';\n" "$PASSWORD" \
    | su postgres -c "psql -q -v ON_ERROR_STOP=1"
  DSN_NEW_PASSWORD="$PASSWORD"
  echo "reset the cortex role password (stored in ${ENV_FILE})"
fi
if [ "$(su postgres -c "psql -qAt -c \"select 1 from pg_database where datname='cortex'\"")" != "1" ]; then
  su postgres -c "psql -q -v ON_ERROR_STOP=1 -c 'create database cortex owner cortex'"
fi
su postgres -c "psql -q -v ON_ERROR_STOP=1 -d cortex -c 'create extension if not exists vector'"

# Restart to apply listen_addresses / memory settings.
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl restart "postgresql@${PG_VERSION}-main" || systemctl restart postgresql
else
  pg_ctlcluster "$PG_VERSION" main restart
fi
for _ in $(seq 1 30); do
  su postgres -c "psql -qAt -c 'select 1'" >/dev/null 2>&1 && break
  sleep 1
done

# --- Verify pgvector >= 0.8.0 ---
EXTVER="$(su postgres -c "psql -qAt -d cortex -c \"select extversion from pg_extension where extname='vector'\"")"
if [ "$(printf '%s\n%s\n' "0.8.0" "$EXTVER" | sort -V | head -n1)" != "0.8.0" ]; then
  echo "pgvector $EXTVER is older than 0.8.0" >&2; exit 1
fi
echo "pgvector $EXTVER OK"

# --- cortex app host: user, env file, systemd, Caddy, firewall ---
setup_app_host() {
  local token unit_dir=/etc/systemd/system

  # Firewall: SSH (rate-limited), HTTP (ACME + redirect), HTTPS. Postgres stays closed.
  if ! command -v ufw >/dev/null 2>&1; then
    apt-get install -y -qq ufw >/dev/null
  fi
  if ! { ufw default deny incoming >/dev/null \
      && ufw default allow outgoing >/dev/null \
      && ufw limit 22/tcp >/dev/null \
      && ufw allow 80/tcp >/dev/null \
      && ufw allow 443/tcp >/dev/null \
      && { ufw --force delete allow 5432/tcp >/dev/null 2>&1 || true; } \
      && ufw --force enable >/dev/null; } 2>/dev/null; then
    echo "warning: could not configure/enable ufw (container or no netfilter?); firewall NOT active" >&2
  fi

  # Service user
  if ! id cortex >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/cortex --create-home --shell /usr/sbin/nologin cortex
  fi

  # Env file: created once, existing values (token, DSN, API keys) preserved.
  install -d -m 0755 "$ENV_DIR"
  if [ ! -f "$ENV_FILE" ]; then
    install -o root -g root -m 0600 /dev/null "$ENV_FILE"
  fi
  chown root:root "$ENV_FILE"; chmod 0600 "$ENV_FILE"
  if [ -n "$DSN_NEW_PASSWORD" ]; then
    env_set CORTEX_DATABASE_URL "postgres://cortex:${DSN_NEW_PASSWORD}@localhost:5432/cortex?sslmode=disable"
  fi
  if [ -z "$(env_get CORTEX_DATABASE_URL)" ]; then
    echo "error: ${ENV_FILE} has no CORTEX_DATABASE_URL and the role already existed" >&2; exit 1
  fi
  if [ -z "$(env_get CORTEX_AUTH_TOKEN)" ]; then
    token="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    env_set CORTEX_AUTH_TOKEN "$token"
    echo "generated CORTEX_AUTH_TOKEN (stored in ${ENV_FILE})"
  fi
  if [ -z "$(env_get CORTEX_SECRET_KEY)" ]; then
    # 32 random bytes, hex. Generated once and preserved: it encrypts every user's
    # provider key, so losing it makes them unreadable. Back it up off the box.
    env_set CORTEX_SECRET_KEY "$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    echo "generated CORTEX_SECRET_KEY (stored in ${ENV_FILE}; back it up off this machine)"
  fi
  env_set CORTEX_PUBLIC_URL "https://${APP_HOST}"
  env_add_if_missing CORTEX_BOOTSTRAP_EMAIL "sausheong@gmail.com"
  env_add_if_missing OPENAI_API_KEY ""
  env_add_if_missing CORTEX_ADDR "127.0.0.1:${APP_PORT}"

  # systemd units. The previous service unit is saved once as cortex.service.prev
  # (rollback-app.sh restores it); nothing here restarts cortex.service.
  local new_unit
  new_unit="$(mktemp)"
  cat > "$new_unit" <<UNIT
[Unit]
Description=cortex multi-user service (web, OAuth and MCP, behind Caddy)
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=cortex
Group=cortex
EnvironmentFile=${ENV_FILE}
ExecStart=${CORTEX_BIN} serve --addr 127.0.0.1:${APP_PORT}
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
UNIT
  if [ -f "$unit_dir/cortex.service" ] && ! cmp -s "$new_unit" "$unit_dir/cortex.service"; then
    cp -p "$unit_dir/cortex.service" "$unit_dir/cortex.service.prev"
  fi
  install -m 0644 "$new_unit" "$unit_dir/cortex.service"
  rm -f "$new_unit"
  cat > "$unit_dir/cortex-maintain.service" <<UNIT
[Unit]
Description=cortex nightly maintenance
After=network-online.target postgresql.service

[Service]
Type=oneshot
User=cortex
Group=cortex
EnvironmentFile=${ENV_FILE}
ExecStart=${CORTEX_BIN} maintain
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
UNIT
  cat > "$unit_dir/cortex-maintain.timer" <<UNIT
[Unit]
Description=Run cortex maintenance nightly at 03:15 SGT

[Timer]
OnCalendar=*-*-* 19:15:00 UTC
Persistent=true

[Install]
WantedBy=timers.target
UNIT
  chmod 0644 "$unit_dir"/cortex.service "$unit_dir"/cortex-maintain.service "$unit_dir"/cortex-maintain.timer

  # Caddy (official apt repo)
  if [ ! -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg ]; then
    curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/gpg.key \
      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  fi
  curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt \
    -o /etc/apt/sources.list.d/caddy-stable.list
  if ! apt-get update -qq; then
    # The upstream repo's signing subkey is rejected by apt's sqv policy on some
    # releases (NO_PUBKEY). Drop the repo and use the distro's caddy package.
    echo "warning: Caddy upstream apt repo unusable; falling back to the distro caddy package" >&2
    rm -f /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq
  fi
  apt-get install -y -qq caddy >/dev/null
  install -d /etc/caddy
  {
    if [ -n "$LE_EMAIL" ]; then
      printf '{\n\temail %s\n}\n\n' "$LE_EMAIL"
    fi
    cat <<CADDY
# Managed by cortex setup-server.sh
${APP_HOST} {
	reverse_proxy 127.0.0.1:${APP_PORT} {
		flush_interval -1
	}
}
CADDY
  } > /etc/caddy/Caddyfile
  if command -v caddy >/dev/null 2>&1; then
    caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 \
      || echo "warning: caddy validate reported a problem with /etc/caddy/Caddyfile" >&2
  fi

  # Start everything (containers have no systemd)
  if have_systemd; then
    systemctl daemon-reload
    systemctl enable cortex.service cortex-maintain.timer >/dev/null 2>&1
    systemctl enable caddy >/dev/null 2>&1 || true
    systemctl restart caddy || echo "warning: caddy failed to start; check 'journalctl -u caddy' (DNS for ${APP_HOST} must point here and port 80 must be reachable)" >&2
    systemctl start cortex-maintain.timer
    # cortex.service is NOT restarted here: scripts/deploy-app.sh does the single restart.
    if [ ! -x "$CORTEX_BIN" ]; then
      echo "note: ${CORTEX_BIN} not installed yet; run scripts/deploy-app.sh from your workstation" >&2
    fi
  else
    echo "warning: systemd not running; units written but NOT enabled/started" >&2
  fi
}
if [ -n "$APP_HOST" ]; then setup_app_host; fi

# --- Backup cron ---
if [ "$INSTALL_CRON" -eq 1 ]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  install -m 0755 "$SCRIPT_DIR/backup.sh" /usr/local/bin/cortex-backup.sh
  [ -f /etc/cortex-backup.env ] || install -m 0600 /dev/null /etc/cortex-backup.env
  cat > /etc/cron.d/cortex-backup <<'CRON'
15 3 * * * root /usr/local/bin/cortex-backup.sh >> /var/log/cortex-backup.log 2>&1
CRON
  chmod 0644 /etc/cron.d/cortex-backup
  echo "Backup cron installed. Put BACKUP_S3_ENDPOINT/BUCKET/KEY/SECRET in /etc/cortex-backup.env."
fi

echo
echo "Postgres ${PG_VERSION} ready. Database: cortex  Role: cortex"
if [ -n "$APP_HOST" ]; then
  echo "Postgres listens on localhost only; port 5432 is closed."
  echo "cortex app host: https://${APP_HOST}/mcp"
  echo "Secrets live in ${ENV_FILE} (root-only). Install/update the binary with scripts/deploy-app.sh."
elif [ -n "$PASSWORD_PRINTED" ]; then
  echo "Generated password (shown once): ${PASSWORD_PRINTED}"
  if [ -n "$LISTEN_IP" ]; then
    DSN_HOST="$LISTEN_IP"; SSLMODE="prefer"
    [ "$USE_SSL" -eq 1 ] && SSLMODE="require"
  else
    DSN_HOST="localhost"; SSLMODE="disable"
  fi
  echo "DSN: postgres://cortex:${PASSWORD_PRINTED}@${DSN_HOST}:5432/cortex?sslmode=${SSLMODE}"
else
  echo "Role already existed; password unchanged."
fi
