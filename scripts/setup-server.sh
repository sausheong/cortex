#!/usr/bin/env bash
# Provision a Debian/Ubuntu host (amd64 or arm64) as a cortex Postgres server.
# Idempotent. Run as root.
#
# Usage: setup-server.sh [--public-host FQDN [--email ADDR]] [--cert FILE --key FILE]
#                        [--listen-ip IP] [--no-cron]
#
#   (no access flags)    Postgres listens on localhost only.
#   --public-host FQDN   Public internet access, TLS only. Postgres listens on '*',
#                        TLS >= 1.2, scram-sha-256, pg_hba accepts only
#                        "hostssl cortex cortex" and rejects non-TLS TCP. Gets a
#                        Let's Encrypt cert via certbot --standalone (needs port 80
#                        reachable and DNS for FQDN pointing here) plus a renewal
#                        hook, and enables ufw allowing only 22, 80 and 5432.
#   --email ADDR         Let's Encrypt account email (else registers without one).
#   --cert F --key F     Use this certificate/key instead of certbot.
#   --listen-ip IP       Private-network host (e.g. a Pi on a LAN): listen on
#                        localhost and IP only; scram-sha-256 from any source that
#                        can reach that interface (TLS-only if --cert/--key given).
#                        Cannot be combined with --public-host.
#   --no-cron            Skip the backup cron job.
set -euo pipefail

PG_VERSION=17
PUBLIC_HOST=""
LE_EMAIL=""
CERT_FILE=""
KEY_FILE=""
LISTEN_IP=""
INSTALL_CRON=1

while [ $# -gt 0 ]; do
  case "$1" in
    --public-host) PUBLIC_HOST="${2:?--public-host needs a value}"; shift 2 ;;
    --email) LE_EMAIL="${2:?--email needs a value}"; shift 2 ;;
    --cert) CERT_FILE="${2:?--cert needs a value}"; shift 2 ;;
    --key) KEY_FILE="${2:?--key needs a value}"; shift 2 ;;
    --listen-ip) LISTEN_IP="${2:?--listen-ip needs a value}"; shift 2 ;;
    --no-cron) INSTALL_CRON=0; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -n "$PUBLIC_HOST" ] && [ -n "$LISTEN_IP" ]; then
  echo "--public-host and --listen-ip are mutually exclusive" >&2; exit 2
fi
if [ -n "$CERT_FILE" ] || [ -n "$KEY_FILE" ]; then
  if [ -z "$CERT_FILE" ] || [ -z "$KEY_FILE" ]; then
    echo "--cert and --key must be given together" >&2; exit 2
  fi
  if [ ! -r "$CERT_FILE" ] || [ ! -r "$KEY_FILE" ]; then
    echo "cannot read --cert/--key files" >&2; exit 2
  fi
fi
if [ -n "$PUBLIC_HOST" ] && ! printf '%s' "$PUBLIC_HOST" | grep -Eq '^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$'; then
  echo "invalid --public-host '$PUBLIC_HOST'" >&2; exit 2
fi
if [ -n "$LISTEN_IP" ] && ! printf '%s' "$LISTEN_IP" | grep -Eq '^[0-9A-Fa-f:.]+$'; then
  echo "invalid --listen-ip '$LISTEN_IP'" >&2; exit 2
fi
if [ -n "$LE_EMAIL" ] && { [ -z "$PUBLIC_HOST" ] || [ -n "$CERT_FILE" ]; }; then
  echo "warning: --email is only used with --public-host and certbot; ignoring" >&2
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

export DEBIAN_FRONTEND=noninteractive
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

# --- Firewall (public mode): allow only 22, 80 (certbot) and 5432 ---
if [ -n "$PUBLIC_HOST" ]; then
  if ! command -v ufw >/dev/null 2>&1; then
    apt-get install -y -qq ufw >/dev/null
  fi
  if ! { ufw default deny incoming >/dev/null \
      && ufw default allow outgoing >/dev/null \
      && ufw limit 22/tcp >/dev/null \
      && ufw allow 80/tcp >/dev/null \
      && ufw allow 5432/tcp >/dev/null \
      && ufw --force enable >/dev/null; } 2>/dev/null; then
    echo "warning: could not configure/enable ufw (container or no netfilter?); firewall NOT active" >&2
  fi
fi

# --- TLS certificate ---
USE_SSL=0
if [ -n "$CERT_FILE" ]; then
  install_certs "$CERT_FILE" "$KEY_FILE"
  USE_SSL=1
elif [ -n "$PUBLIC_HOST" ]; then
  apt-get install -y -qq certbot openssl >/dev/null
  LIVE="/etc/letsencrypt/live/${PUBLIC_HOST}"
  # Skip issuance when a cert valid for > 30 more days is already present.
  if [ -f "$LIVE/fullchain.pem" ] && openssl x509 -in "$LIVE/fullchain.pem" -noout -checkend 2592000 >/dev/null 2>&1; then
    echo "certificate for ${PUBLIC_HOST} already present"
  else
    CB_ARGS=(certonly --standalone --non-interactive --agree-tos --keep-until-expiring -d "$PUBLIC_HOST")
    if [ -n "$LE_EMAIL" ]; then
      CB_ARGS+=(--email "$LE_EMAIL")
    else
      CB_ARGS+=(--register-unsafely-without-email)
    fi
    if ! certbot "${CB_ARGS[@]}"; then
      echo "certbot failed: check that ${PUBLIC_HOST} has a DNS-only (grey-cloud) A record pointing at this host and that port 80 is free" >&2
      exit 1
    fi
  fi
  install_certs "$LIVE/fullchain.pem" "$LIVE/privkey.pem"
  USE_SSL=1
  # Re-copy and reload on every renewal.
  install -d /etc/letsencrypt/renewal-hooks/deploy
  cat > /etc/letsencrypt/renewal-hooks/deploy/cortex-postgres.sh <<HOOK
#!/bin/sh
# Managed by cortex setup-server.sh
set -e
L="\${RENEWED_LINEAGE:-${LIVE}}"
install -o postgres -g postgres -m 0644 "\$L/fullchain.pem" "${SSL_DIR}/server.crt"
install -o postgres -g postgres -m 0600 "\$L/privkey.pem" "${SSL_DIR}/server.key"
if [ -d /run/systemd/system ]; then
  systemctl reload postgresql@${PG_VERSION}-main || systemctl reload postgresql
else
  pg_ctlcluster ${PG_VERSION} main reload
fi
HOOK
  chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/cortex-postgres.sh
fi

LISTEN="localhost"
[ -n "$LISTEN_IP" ] && LISTEN="localhost,${LISTEN_IP}"
[ -n "$PUBLIC_HOST" ] && LISTEN="*"

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
  if [ -n "$PUBLIC_HOST" ]; then
    echo "hostssl cortex cortex 0.0.0.0/0 scram-sha-256"
    echo "hostssl cortex cortex ::/0 scram-sha-256"
    echo "hostnossl all all 0.0.0.0/0 reject"
    echo "hostnossl all all ::/0 reject"
  elif [ -n "$LISTEN_IP" ]; then
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

# --- Role and database ---
PASSWORD_PRINTED=""
if [ "$(su postgres -c "psql -qAt -c \"select 1 from pg_roles where rolname='cortex'\"")" != "1" ]; then
  PASSWORD="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)"
  printf "set password_encryption='scram-sha-256';\ncreate role cortex login nosuperuser password '%s';\n" "$PASSWORD" \
    | su postgres -c "psql -q -v ON_ERROR_STOP=1"
  PASSWORD_PRINTED="$PASSWORD"
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
if [ -n "$PASSWORD_PRINTED" ]; then
  echo "Generated password (shown once): ${PASSWORD_PRINTED}"
  if [ -n "$PUBLIC_HOST" ]; then
    DSN_HOST="$PUBLIC_HOST"; SSLMODE="verify-full"
    [ -n "$CERT_FILE" ] && SSLMODE="require"   # custom cert: may not chain to system roots
  elif [ -n "$LISTEN_IP" ]; then
    DSN_HOST="$LISTEN_IP"; SSLMODE="prefer"
    [ "$USE_SSL" -eq 1 ] && SSLMODE="require"
  else
    DSN_HOST="localhost"; SSLMODE="disable"
  fi
  echo "DSN: postgres://cortex:${PASSWORD_PRINTED}@${DSN_HOST}:5432/cortex?sslmode=${SSLMODE}"
else
  echo "Role already existed; password unchanged."
fi
