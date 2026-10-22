# Deploying the cortex Postgres server

Cortex stores everything in Postgres 17 with pgvector (>= 0.8.0). One small server can hold a whole personal brain. In the recommended layout the cortex MCP server runs on the same host as Postgres and is the only thing exposed to the internet, over HTTPS with a bearer token; Postgres itself is never reachable from outside.

Docker is not used on deploy targets (it is only used for local tests).

Supported hosts: Debian or Ubuntu, amd64 or arm64 (including Debian 13 "trixie" on a Raspberry Pi).

## What `scripts/setup-server.sh` does

Run as root on a fresh host. It is idempotent: re-running keeps the existing role and password and rewrites the managed config.

```bash
sudo ./scripts/setup-server.sh                              # Postgres on localhost only
sudo ./scripts/setup-server.sh --app-host cortex.example.com # + cortex app behind HTTPS (Caddy)
sudo ./scripts/setup-server.sh --listen-ip 192.168.1.50      # private LAN (e.g. a Pi)
```

Flags:

- `--app-host FQDN`: run the cortex app on this host behind HTTPS (see below). Optional `--email ADDR` for the Let's Encrypt account that Caddy registers.
- `--cert FILE --key FILE`: Postgres server certificate for `--listen-ip` mode.
- `--listen-ip IP`: listen on localhost plus this private IP. Cannot be combined with `--app-host`.
- `--no-cron`: skip the backup cron job.

It:

- installs Postgres 17 and `postgresql-17-pgvector` from the PGDG apt repo (suite derived from `VERSION_CODENAME`), and checks `extversion >= 0.8.0`;
- creates role `cortex` (not a superuser) and database `cortex`. Without `--app-host` it prints a generated 24-character password once, with a ready DSN; with `--app-host` the password goes straight into `/etc/cortex/cortex.env` and is not printed;
- tunes `postgresql.conf` from detected RAM (`conf.d/cortex.conf`): `shared_buffers` 25%, `effective_cache_size` 60%, `maintenance_work_mem` min(RAM/8, 1 GB), `work_mem` 4 MB under 2 GB RAM else 16 MB, `max_connections=20`, `password_encryption=scram-sha-256`;
- creates a 2 GB swapfile if RAM is under 2 GB;
- manages a block in `pg_hba.conf` between `# BEGIN cortex-managed` and `# END cortex-managed` (rewritten each run);
- installs a daily 03:15 cron job for the backup script.

### App host mode (`--app-host`)

Postgres and cortex share one host. Clients (Claude Code, Cursor, scripts) talk only to cortex over HTTPS; nothing talks to Postgres from outside, so networks that interfere with port 5432 do not matter.

```
client --HTTPS 443--> Caddy --> cortex mcp (127.0.0.1:8080) --> Postgres (localhost:5432)
```

What the script sets up:

- **Postgres**: `listen_addresses='localhost'`, no TLS, scram-sha-256, `pg_hba.conf` with only the localhost rules. Port 5432 is closed in `ufw` (an old rule is removed).
- **User and secrets**: system user `cortex` (no login shell), and `/etc/cortex/cortex.env` (root-owned, mode 0600) holding `CORTEX_DATABASE_URL` (local connection as role `cortex`), `OPENAI_API_KEY` (empty until `deploy-app.sh` fills it), `CORTEX_ADDR`, and `CORTEX_AUTH_TOKEN` (32 random bytes as hex). Existing values are kept on re-runs, so the token never changes unless you change it. If the role exists but the file has no database URL (for example when converting an older install) the role password is reset and the new URL is stored.
- **systemd**: `cortex.service` runs `cortex mcp --transport http --addr 127.0.0.1:8080` as user `cortex` with that env file (`Restart=on-failure`). `cortex-maintain.timer` runs `cortex maintain` (via `cortex-maintain.service`) every day at 03:15 SGT (19:15 UTC) as the same user. Where systemd is absent (containers) the units are written and a warning is printed.
- **Caddy** (official apt repo): obtains and renews its own Let's Encrypt certificate for the FQDN, serves 443 (and 80 for ACME and redirect) and reverse-proxies to `127.0.0.1:8080` with response buffering disabled (streamable HTTP).
- **Auth**: cortex always requires `Authorization: Bearer <token>` on `/mcp` when `CORTEX_AUTH_TOKEN` is set, even on a loopback bind. `/healthz` is unauthenticated.
- **ufw**: `limit 22`, `allow 80`, `allow 443`, default deny incoming.
- **certbot**: if an earlier public-Postgres setup left a certbot certificate, its renewal config is renamed to `*.conf.cortex-disabled`, the Postgres deploy hook and the copied Postgres key are removed, and `certbot.timer` is disabled (certbot standalone would fight Caddy for port 80). The certbot package itself is left installed.

Requirements: DNS for the FQDN must already point at the host (grey-cloud/DNS-only if the zone is on Cloudflare), and ports 80 and 443 must be reachable.

After the server is prepared, install the binary and settings from your workstation:

```bash
export OPENAI_API_KEY=sk-...          # plus LLM_* / EMBEDDING_* if you use them
./scripts/deploy-app.sh root@cortex.example.com
```

`deploy-app.sh` detects the server architecture over ssh, cross-compiles cortex (`GOOS=linux CGO_ENABLED=0`), copies it to `/usr/local/bin/cortex` atomically, writes `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `LLM_PROVIDER`, `LLM_MODEL`, `EMBEDDING_API_KEY`, `EMBEDDING_BASE_URL`, `EMBEDDING_MODEL` and `EMBEDDING_DIMS` from your environment into `/etc/cortex/cortex.env` (sent over ssh stdin, never echoed), restarts the service, checks `https://<fqdn>/healthz` and that an unauthenticated `/mcp` request gets 401, then prints the MCP URL. The token is not printed; it says where it is. Use `--show-token` to print it, or `ssh root@host grep CORTEX_AUTH_TOKEN /etc/cortex/cortex.env`. Pass `--fqdn` when the ssh target is an IP address.

Re-running `setup-server.sh --app-host ...` is safe on a live host (it also converts an older public-TLS install). Re-running `deploy-app.sh` upgrades the binary.

### Private network mode (`--listen-ip`)

For a host on a trusted LAN. Postgres listens on localhost plus the given IP only, and accepts `cortex` with scram-sha-256 from any source that can reach that interface. With `--cert/--key` it is TLS-only (non-TLS rejected); without them it is plain `host` scram. Do not port-forward 5432 from the internet to such a host.

## Droplet (DigitalOcean)

1. Create the smallest droplet (512 MB RAM, ~US$4/month), Ubuntu 24.04, in the region nearest you (SGP1). Turn **droplet backups on**.
2. DNS: create an `A` record for the host name (for example `cortex.sausheong.dev`) pointing at the droplet IP. If the zone is on Cloudflare, set the record to **DNS only (grey cloud)** so Caddy's ACME challenge reaches the droplet.
3. Optional DigitalOcean cloud firewall: if you use one, allow inbound 22, 80 and 443 only (the script already enables `ufw` with the same rules). Do not open 5432.
4. Copy `scripts/` to the host and run `sudo ./scripts/setup-server.sh --app-host cortex.sausheong.dev --email you@example.com`.
5. From your workstation run `./scripts/deploy-app.sh root@cortex.sausheong.dev`.
6. Create a Spaces bucket (private) for dumps and fill in `/etc/cortex-backup.env` (see Backups).

To convert a droplet that was set up in the old public-Postgres mode, run step 4 again: the script closes 5432, returns Postgres to localhost, removes the TLS key copy and certbot hook, and stores a fresh database password in `/etc/cortex/cortex.env`. Then run step 5.

## Raspberry Pi

- Use a 64-bit OS (Raspberry Pi OS 64-bit, Debian 13 trixie or 12 bookworm).
- Run from an SSD, not an SD card. Postgres write patterns wear SD cards out.
- On a home LAN use `--listen-ip <pi-lan-ip>` (ideally with `--cert/--key`). Do not port-forward 5432. To expose cortex itself, use `--app-host` instead.
- There is no droplet backup layer here: the nightly S3 dump is the only backup. Set it up before you load data.

## Connecting from your Mac

With an app host there is nothing to connect to directly: point your MCP client at the HTTPS endpoint.

```bash
claude mcp add --transport http cortex https://cortex.sausheong.dev/mcp \
  --header "Authorization: Bearer <token>"
```

Or in a client config: `{"type": "http", "url": "https://cortex.sausheong.dev/mcp", "headers": {"Authorization": "Bearer <token>"}}`.

To run the CLI against the server's database (for example `cortex import-sqlite` to bring over a v0.4 brain), run it on the server over ssh, or tunnel Postgres through ssh instead of opening 5432:

```bash
ssh -N -L 55432:localhost:5432 root@cortex.sausheong.dev &
export CORTEX_DATABASE_URL='postgres://cortex:PASSWORD@localhost:55432/cortex?sslmode=disable'   # PASSWORD from /etc/cortex/cortex.env
cortex import-sqlite /path/to/brain.db
```

For a LAN host set up with `--listen-ip`, use `CORTEX_DATABASE_URL=postgres://cortex:PASSWORD@<ip>:5432/cortex?sslmode=require`.

## Security notes

- The only public services are SSH (rate-limited), HTTP (ACME and redirect) and HTTPS. Postgres listens on localhost only and port 5432 is closed.
- `/mcp` requires the bearer token. Treat `CORTEX_AUTH_TOKEN` like a password: anyone holding it can read and delete your memory. `/etc/cortex/cortex.env` is root-only (0600).
- Rotate the token by editing `CORTEX_AUTH_TOKEN` in `/etc/cortex/cortex.env` (for example `openssl rand -hex 32`) and `systemctl restart cortex`, then update your clients.
- The database password is generated from `/dev/urandom` (24 alphanumeric characters), scram-sha-256, localhost only. The application role has no superuser rights. Administer the server through SSH and `sudo -u postgres psql`.
- Consider SSH key-only login (`PasswordAuthentication no`) and `fail2ban` for SSH.

### Rotating the password

```bash
NEWPW="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)"
printf "set password_encryption='scram-sha-256';\nalter role cortex password '%s';\n" "$NEWPW" | sudo -u postgres psql -v ON_ERROR_STOP=1
echo "New password: $NEWPW"
```

To choose the password yourself instead, run `sudo -u postgres psql`, then `\password cortex` (hashed client-side, not echoed). Update `CORTEX_DATABASE_URL` in `/etc/cortex/cortex.env` and `systemctl restart cortex`.

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

## Clients on a filtering network (SSH tunnel)

Some networks block new or uncategorised domains by name, so `https://<fqdn>/mcp`
is reset even though port 443 is open. SSH usually still gets through. Forward a
local port to cortex over SSH and point the MCP client at it:

```bash
ssh -N -L 127.0.0.1:8443:127.0.0.1:8080 <user>@<fqdn>
claude mcp add --transport http --scope user cortex http://127.0.0.1:8443/mcp \
  --header "Authorization: Bearer <token>"
```

The bearer token is still required: the tunnel only changes the transport. To
keep the tunnel up, run it from a launchd agent (macOS) with `KeepAlive` and a
`ThrottleInterval` of at least 30 s, because ufw rate-limits new SSH connections
(6 per 30 s) and a fast reconnect loop gets refused.
