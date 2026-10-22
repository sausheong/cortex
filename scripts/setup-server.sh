#!/usr/bin/env bash
# Provision a Debian/Ubuntu host (amd64 or arm64) as a cortex Postgres server.
# Idempotent. Run as root.
#
# Usage: setup-server.sh [--tailscale-ip IP] [--no-tailscale] [--no-cron]
set -euo pipefail

PG_VERSION=17
TAILSCALE_IP=""
INSTALL_TAILSCALE=1
INSTALL_CRON=1

while [ $# -gt 0 ]; do
  case "$1" in
    --tailscale-ip) TAILSCALE_IP="${2:?--tailscale-ip needs a value}"; shift 2 ;;
    --no-tailscale) INSTALL_TAILSCALE=0; shift ;;
    --no-cron) INSTALL_CRON=0; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

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

# --- Tailscale ---
if [ "$INSTALL_TAILSCALE" -eq 1 ]; then
  if ! command -v tailscale >/dev/null 2>&1; then
    curl -fsSL https://tailscale.com/install.sh | sh
  fi
  if [ -z "$TAILSCALE_IP" ] && command -v tailscale >/dev/null 2>&1; then
    TAILSCALE_IP="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
  fi
  if [ -z "$TAILSCALE_IP" ]; then
    echo "note: run 'tailscale up', then re-run this script (or pass --tailscale-ip) to listen on the tailnet" >&2
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

LISTEN="localhost"
[ -n "$TAILSCALE_IP" ] && LISTEN="localhost,${TAILSCALE_IP}"

install -d "$CONF_DIR/conf.d"
cat > "$CONF_DIR/conf.d/cortex.conf" <<CONF
# Managed by cortex setup-server.sh (RAM detected: ${RAM_MB} MB)
listen_addresses = '${LISTEN}'
max_connections = 20
shared_buffers = ${SHARED_MB}MB
effective_cache_size = ${EFFECTIVE_MB}MB
maintenance_work_mem = ${MAINT_MB}MB
work_mem = ${WORK_MB}MB
CONF
grep -Eq "^include_dir *= *'conf.d'" "$CONF_DIR/postgresql.conf" || \
  echo "include_dir = 'conf.d'" >> "$CONF_DIR/postgresql.conf"

# --- Access: password auth from localhost and the tailnet ---
HBA="$CONF_DIR/pg_hba.conf"
if ! grep -q '# cortex-managed' "$HBA"; then
  {
    echo "# cortex-managed"
    echo "host cortex cortex 127.0.0.1/32 scram-sha-256"
    echo "host cortex cortex ::1/128 scram-sha-256"
    echo "host cortex cortex 100.64.0.0/10 scram-sha-256"
  } >> "$HBA"
fi

# --- Role and database ---
PASSWORD_PRINTED=""
if [ "$(su postgres -c "psql -qAt -c \"select 1 from pg_roles where rolname='cortex'\"")" != "1" ]; then
  PASSWORD="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)"
  su postgres -c "psql -q -v ON_ERROR_STOP=1 -c \"create role cortex login password '${PASSWORD}'\""
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
  HOST="${TAILSCALE_IP:-<tailscale-ip>}"
  echo "DSN: postgres://cortex:${PASSWORD_PRINTED}@${HOST}:5432/cortex?sslmode=prefer"
else
  echo "Role already existed; password unchanged."
fi
