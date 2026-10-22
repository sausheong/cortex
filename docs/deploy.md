# Deploying the cortex Postgres server

Cortex stores everything in Postgres 17 with pgvector (>= 0.8.0). One small server can hold a whole personal brain and be shared by the CLI and the MCP server.

Docker is not used on deploy targets (it is only used for local tests).

Supported hosts: Debian or Ubuntu, amd64 or arm64 (including Debian 13 "trixie" on a Raspberry Pi).

## What `scripts/setup-server.sh` does

Run as root on a fresh host. It is idempotent: re-running keeps the existing role and password and rewrites the managed config.

```bash
sudo ./scripts/setup-server.sh                                  # localhost only
sudo ./scripts/setup-server.sh --public-host cortex.example.com # public internet, TLS only
sudo ./scripts/setup-server.sh --listen-ip 192.168.1.50         # private LAN (e.g. a Pi)
```

Flags:

- `--public-host FQDN`: public, TLS-only access (see below). Optional `--email ADDR` for the Let's Encrypt account (otherwise it registers without an email).
- `--cert FILE --key FILE`: use your own certificate and key instead of certbot (also used by the tests).
- `--listen-ip IP`: listen on localhost plus this private IP. Cannot be combined with `--public-host`.
- `--no-cron`: skip the backup cron job.

It:

- installs Postgres 17 and `postgresql-17-pgvector` from the PGDG apt repo (suite derived from `VERSION_CODENAME`), and checks `extversion >= 0.8.0`;
- creates role `cortex` (not a superuser) and database `cortex`, and prints a generated 24-character password once, with a ready DSN;
- tunes `postgresql.conf` from detected RAM (`conf.d/cortex.conf`): `shared_buffers` 25%, `effective_cache_size` 60%, `maintenance_work_mem` min(RAM/8, 1 GB), `work_mem` 4 MB under 2 GB RAM else 16 MB, `max_connections=20`, `password_encryption=scram-sha-256`;
- creates a 2 GB swapfile if RAM is under 2 GB;
- manages a block in `pg_hba.conf` between `# BEGIN cortex-managed` and `# END cortex-managed` (rewritten each run);
- installs a daily 03:15 cron job for the backup script.

### Public TLS mode (`--public-host`)

Postgres is reachable directly from the internet, so everything is locked down:

- `listen_addresses='*'`, `ssl=on`, `ssl_min_protocol_version='TLSv1.2'`, `password_encryption=scram-sha-256`.
- `pg_hba.conf` allows only `hostssl cortex cortex` (IPv4 and IPv6, scram-sha-256) plus the localhost rules. Any other TCP connection without TLS is rejected (`hostnossl all all ... reject`). There is no rule for any other role or database, so the `postgres` superuser cannot log in over the network.
- The certificate comes from Let's Encrypt with `certbot certonly --standalone` (non-interactive). It is copied to `/etc/postgresql/17/main/ssl/` (owner `postgres`, key mode 0600). A deploy hook, `/etc/letsencrypt/renewal-hooks/deploy/cortex-postgres.sh`, re-copies the files and reloads Postgres on every renewal. If a valid certificate (more than 30 days left) is already present, certbot is skipped.
- `ufw` is enabled allowing only 22 (SSH), 80 (certbot standalone issuance and renewal) and 5432. If ufw cannot run (for example in a container) the script prints a warning and continues without a firewall.

Requirements: DNS for the FQDN must already point at the host, and port 80 must be reachable for issuance and renewal.

### Private network mode (`--listen-ip`)

For a host on a trusted LAN. Postgres listens on localhost plus the given IP only, and accepts `cortex` with scram-sha-256 from any source that can reach that interface. With `--cert/--key` it is TLS-only (non-TLS rejected); without them it is plain `host` scram. Do not port-forward 5432 from the internet to such a host: use `--public-host` instead.

## Droplet (DigitalOcean)

1. Create the smallest droplet (512 MB RAM, ~US$4/month), Ubuntu 24.04, in the region nearest you (SGP1). Turn **droplet backups on**.
2. DNS: create an `A` record for the host name (for example `cortex.sausheong.dev`) pointing at the droplet IP. If the zone is on Cloudflare, set the record to **DNS only (grey cloud)**. Cloudflare's proxy does not carry Postgres traffic, and certbot's HTTP challenge also needs to reach the droplet directly.
3. Optional DigitalOcean cloud firewall: if you use one, allow inbound 22, 80 and 5432 (the script already enables `ufw` with the same rules).
4. Copy `scripts/` to the host and run `sudo ./scripts/setup-server.sh --public-host cortex.sausheong.dev --email you@example.com`. Save the printed DSN.
5. Create a Spaces bucket (private) for dumps and fill in `/etc/cortex-backup.env` (see Backups).

To convert an existing localhost-only droplet, just run the same command: the existing role and password are kept (no new DSN is printed, so reuse the password you have).

## Raspberry Pi

- Use a 64-bit OS (Raspberry Pi OS 64-bit, Debian 13 trixie or 12 bookworm).
- Run from an SSD, not an SD card. Postgres write patterns wear SD cards out.
- On a home LAN use `--listen-ip <pi-lan-ip>` (ideally with `--cert/--key`). Do not port-forward 5432.
- There is no droplet backup layer here: the nightly S3 dump is the only backup. Set it up before you load data.

## Connecting from your Mac

```bash
export CORTEX_DATABASE_URL='postgres://cortex:PASSWORD@cortex.sausheong.dev:5432/cortex?sslmode=verify-full'
cortex init
```

`sslmode=verify-full` makes pgx check the certificate chain and host name against the system roots, which works with the Let's Encrypt certificate. If you supplied your own certificate that does not chain to system roots, use `sslmode=require` (encrypted, but the server is not authenticated) or add `sslrootcert=/path/ca.pem`. `--db <dsn>` overrides the variable. To bring over a v0.4 SQLite brain: `cortex import-sqlite /path/to/brain.db`.

## Security notes

- The password is generated from `/dev/urandom` (24 alphanumeric characters) and shown once. Store it in a password manager; the DSN contains it, so keep it out of shell history and shared files.
- Authentication is scram-sha-256 only, over TLS 1.2 or newer. Plain-text TCP connections are rejected.
- The application role has no superuser rights, and the `postgres` superuser has no network access path. Administer the server through SSH and `sudo -u postgres psql`.
- Only 22, 80 and 5432 are open. Consider SSH key-only login (`PasswordAuthentication no`), and `fail2ban` for SSH.
- Port 5432 is a public target for password guessing. The 24-character random password makes that impractical; do not replace it with a short one.

### Rotating the password

```bash
NEWPW="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)"
printf "set password_encryption='scram-sha-256';\nalter role cortex password '%s';\n" "$NEWPW" | sudo -u postgres psql -v ON_ERROR_STOP=1
echo "New password: $NEWPW"
```

To choose the password yourself instead, run `sudo -u postgres psql`, then `\password cortex` (hashed client-side, not echoed). Update `CORTEX_DATABASE_URL` everywhere (Mac shell profile, MCP client config, cron jobs).

## Backups

`scripts/backup.sh` runs `pg_dump -Fc cortex`, uploads to an S3-compatible bucket with `rclone`, and deletes objects older than 14 days. Configure it in `/etc/cortex-backup.env` (mode 0600):

```bash
BACKUP_S3_ENDPOINT=https://sgp1.digitaloceanspaces.com
BACKUP_S3_BUCKET=my-cortex-backups
BACKUP_S3_KEY=...
BACKUP_S3_SECRET=...
```

Test it once by hand: `sudo /usr/local/bin/cortex-backup.sh`. For local testing, `BACKUP_LOCAL_DIR=/some/dir` writes the dump to a directory instead.

### Restore

Always restore into a scratch database first.

```bash
rclone copyto cortex:my-cortex-backups/cortex-YYYYMMDDTHHMMSSZ.dump /tmp/restore.dump   # or download from the bucket
sudo -u postgres createdb cortex_restore
sudo -u postgres pg_restore -d cortex_restore /tmp/restore.dump
sudo -u postgres psql -d cortex_restore -c 'select count(*) from entities'
```

When satisfied, point cortex at it, or rename it over `cortex` after stopping clients:
`ALTER DATABASE cortex RENAME TO cortex_old; ALTER DATABASE cortex_restore RENAME TO cortex;`

## When to resize

Resize the host when the indexes no longer fit in RAM, or disk use passes 70%. Check:

```sql
select relname, pg_size_pretty(pg_relation_size(oid)) as size
from pg_class
where relkind = 'i' and relnamespace = 'public'::regnamespace
order by pg_relation_size(oid) desc;

select pg_size_pretty(pg_database_size('cortex'));
```

Compare the index total with `shared_buffers` plus free page cache (`free -m`), and `df -h` for disk. After resizing, re-run `setup-server.sh` to recompute the memory settings.

## Pinning downstream projects

`v0.4.0` is the last SQLite release. Projects that depend on cortex and are not ready for Postgres should pin it:

```bash
go get github.com/sausheong/cortex@v0.4.0
```

The Postgres version ships as `v0.5.0`.
