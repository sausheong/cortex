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
client --HTTPS 443--> Caddy --> cortex serve (127.0.0.1:8080) --> Postgres (localhost:5432)
```

What the script sets up:

- **Postgres**: `listen_addresses='localhost'`, no TLS, scram-sha-256, `pg_hba.conf` with only the localhost rules. Port 5432 is closed in `ufw` (an old rule is removed).
- **User and secrets**: system user `cortex` (no login shell), and `/etc/cortex/cortex.env` (root-owned, mode 0600) holding `CORTEX_DATABASE_URL` (local connection as role `cortex`), `CORTEX_ADDR`, `CORTEX_PUBLIC_URL` (`https://<fqdn>`), `CORTEX_BOOTSTRAP_EMAIL` (default `sausheong@gmail.com`), `CORTEX_SECRET_KEY` (32 random bytes as hex, generated once; it encrypts every user's provider key) and `CORTEX_AUTH_TOKEN` (32 random bytes as hex; it becomes the bootstrap user's agent token "claude-code (tunnel)"). Existing values are kept on re-runs, so the token never changes unless you change it. If the role exists but the file has no database URL (for example when converting an older install) the role password is reset and the new URL is stored.
- **systemd**: `cortex.service` runs `cortex serve --addr 127.0.0.1:8080` as user `cortex` with that env file (`Restart=on-failure`). The previous unit is saved once as `cortex.service.prev` for rollback, and the script never restarts `cortex.service` (`deploy-app.sh` does the single restart). `cortex-maintain.timer` runs a plain `cortex maintain` (via `cortex-maintain.service`) every day at 03:15 SGT (19:15 UTC) as the same user: per-user maintenance, then the purge (see below). Where systemd is absent (containers) the units are written and a warning is printed.
- **Caddy** (official apt repo): obtains and renews its own Let's Encrypt certificate for the FQDN, serves 443 (and 80 for ACME and redirect) and reverse-proxies to `127.0.0.1:8080` with response buffering disabled (streamable HTTP).
- **Auth**: `/mcp` always requires a bearer token (an OAuth access token or an agent token, including the old `CORTEX_AUTH_TOKEN`), even on a loopback bind. `/healthz` is unauthenticated.
- **ufw**: `limit 22`, `allow 80`, `allow 443`, default deny incoming.
- **certbot**: if an earlier public-Postgres setup left a certbot certificate, its renewal config is renamed to `*.conf.cortex-disabled`, the Postgres deploy hook and the copied Postgres key are removed, and `certbot.timer` is disabled (certbot standalone would fight Caddy for port 80). The certbot package itself is left installed.

Requirements: DNS for the FQDN must already point at the host (grey-cloud/DNS-only if the zone is on Cloudflare), and ports 80 and 443 must be reachable.

After the server is prepared, install the binary and settings from your workstation:

```bash
export GOOGLE_OAUTH_CLIENT_ID=... GOOGLE_OAUTH_CLIENT_SECRET=...   # or put them in the repo .env
./scripts/deploy-app.sh root@cortex.example.com
```

`deploy-app.sh` detects the server architecture over ssh, cross-compiles cortex (`GOOS=linux CGO_ENABLED=0`) and uploads it without installing it. It writes only `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` and `CORTEX_BOOTSTRAP_EMAIL` from your environment (and from the repo `.env`, if present; those three keys are parsed out of it, the file is never sourced). Provider settings (`OPENAI_*`, `ANTHROPIC_*`, `LLM_*`, `EMBEDDING_*`) are never forwarded and need not be set: each user's own settings hold the provider config (settings page, or `cortex users set-provider`), and the operator key is never used for users into `/etc/cortex/cortex.env` (sent over ssh stdin, never echoed). Then it does the single restart, in this order:

1. stop `cortex.service`;
2. final `pg_dump -Fc` to `/root/cortex/pre-multiuser.dump` (taken after the stop, so a rollback loses no writes; the script aborts if the dump fails);
3. keep the installed binary as `/usr/local/bin/cortex.prev`;
4. install the new binary and run `cortex migrate --timeout 30m`;
5. `systemctl daemon-reload`, start the service, poll `https://<fqdn>/healthz`, and check that an unauthenticated `/mcp` request gets 401.

A failed migrate never starts the new binary; a failed start or health check also exits non-zero. Both print the rollback command. The token is not printed; it says where it is. Use `--show-token` to print it. Pass `--fqdn` when the ssh target is an IP address.

Re-running `setup-server.sh --app-host ...` is safe on a live host (it also converts an older public-TLS install). Re-running `deploy-app.sh` upgrades the binary.

#### Rollout and rollback runbook

Rollout (the first multi-user upgrade, repeated in Task 15 of the plan):

1. Rehearse on a copy of the production data first (restore a recent backup into a scratch database and run `cortex migrate`).
2. Create the Google OAuth client (below) and put `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` in the repo `.env`.
3. `scripts/setup-server.sh --app-host cortex.sausheong.dev` on the droplet. It adds `CORTEX_SECRET_KEY`, writes the new unit and saves the old one as `cortex.service.prev`. Back up `CORTEX_SECRET_KEY` off the box now.
4. `scripts/deploy-app.sh root@cortex.sausheong.dev`.
5. Sign in at `https://cortex.sausheong.dev` with the bootstrap Google account; it binds the existing brain. Check that Claude Code still works unchanged with the old token.

Rollback (if migrate, the start or the health check fails, or the result is wrong):

```bash
./scripts/rollback-app.sh --fqdn cortex.sausheong.dev root@cortex.sausheong.dev
```

It stops the service, restores `cortex.prev` and `cortex.service.prev`, runs `pg_restore --clean --if-exists` from the dump (default `/root/cortex/pre-multiuser.dump`, change with `--dump PATH`), reloads systemd, starts the service and polls `/healthz`. It refuses to change anything unless the previous binary, the previous unit and the dump all exist. Anything written after the dump is lost.

#### Google sign-in

Create an OAuth client (Google Cloud console, APIs and Services, Credentials, OAuth client ID, type "Web application"). Set the authorized redirect URI to exactly `https://cortex.sausheong.dev/auth/google/callback`. Put the client ID and secret in `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET`, and ship them with `deploy-app.sh`. Sign-up is open to any Google account. Only a sign-in with `CORTEX_BOOTSTRAP_EMAIL` and `email_verified=true` binds the pre-existing brain.

#### Connecting assistants

- **ChatGPT**: Plugins, then +, then Add custom MCP server, URL `https://cortex.sausheong.dev/mcp`, authentication OAuth. Sign in with Google and approve the consent screen.
- **claude.ai**: Settings, Connectors, add a custom connector with the URL `https://cortex.sausheong.dev/mcp`, then sign in and approve.
- **Your own agents** (Claude Code and others): create a named agent token on the Connections page and send `Authorization: Bearer <token>`.

Suggested custom instruction for the assistant: "You have a memory tool called cortex. At the start of a conversation call `context` to load what you know about me. When I tell you something worth keeping, store it with `remember`. Before answering questions about my life, projects or preferences, call `recall`."

#### Provider URL policy and `CORTEX_ALLOW_PRIVATE_PROVIDERS`

Users bring their own AI provider. Chat and embedding base URLs must be public `https`; the dial-time check rejects loopback, private, link-local (including `169.254.169.254`), CGNAT and unique-local addresses, redirects are not followed and environment proxies are ignored. `CORTEX_ALLOW_PRIVATE_PROVIDERS=1` in `/etc/cortex/cortex.env` lifts the address and `https` restrictions for local providers such as Ollama. It is for the operator only: it is read once at `cortex serve` start and cannot be set from the web. It never applies to OAuth client metadata fetches.

#### Nightly maintenance, purge and admin commands

`cortex maintain` (no `--user`) runs two independent phases:

1. **Maintenance** for every live user with valid provider settings: reconcile, relate, decay, expire and profile with that user's own providers, then refresh the storage figures. A user's error is stored in `users.last_maintenance_error` (shown on their settings page) and the run continues. If a user's desired embedder differs from the active one and no re-embed is queued or running, a `reembed` job is queued (the service picks it up within 30 seconds).
2. **Purge**, for all users including deleted and settings-less ones: rows soft-deleted more than 30 days ago, accounts deleted more than 30 days ago (hard delete with all rows), expired unauthorized DCR clients, authorize requests, codes, login attempts and sessions.

The command exits non-zero if any user failed, after the purge has run. Admin commands:

```bash
cortex users list                                  # ID, email (verified flag), created, usage
printf '%s\n' "$KEY" | cortex users set-provider --user <id|verified-email> \
  [--provider openai-compatible|anthropic] [--base-url URL] [--chat-model M] \
  [--embed-model M] [--embed-dims N] [--embed-base-url URL] [--skip-test]
cortex maintain --user <id|verified-email> [--dry-run]
cortex recall   --user <id|verified-email> "query"
cortex profile  --user <id|verified-email>
cortex sync     --user <id|verified-email> <dir>
cortex import-sqlite --user <id|verified-email> brain.db
cortex migrate [--timeout 30m]                     # apply migrations, print versions and elapsed time
```

`users set-provider` reads the API key from stdin (line 1; optional line 2 is the embedding key) or `CORTEX_PROVIDER_API_KEY` / `CORTEX_PROVIDER_EMBED_API_KEY`, never argv; unset flags keep the user's current values. It tests the provider first (`--skip-test` bypasses), saves through the same validated, encrypted path as the settings page, and queues `index`/`reembed` jobs when needed (it prints when a re-embed is required). It needs `CORTEX_SECRET_KEY`.

`--user` takes a user ID or a verified email only. An email that matches several accounts, or only an unverified account, is refused with the matching IDs listed. With `--user` the command uses that user's own saved provider settings, so it needs `CORTEX_SECRET_KEY` in the environment; the operator's `OPENAI_API_KEY` is never used for another user.

#### Per-user encryption migration and old copies of plaintext

Passkey enrollment is on by default (`CORTEX_ENCRYPTION_ENROLL=0` turns it off). When an existing user enrolls, a background job rewrites all of their rows as ciphertext in batches. It runs only while that user has a key attached (any signed-in request or reconnected assistant), resumes by itself after a restart, and finishes with a post-check that flips the account to `encrypted`. Then it runs `VACUUM (FULL)` and `ANALYZE` on each content table, one table at a time. `VACUUM FULL` takes an exclusive lock on the table while it rewrites it, so expect short pauses for every user of the instance, longer on large tables. A long-running transaction elsewhere in the database can stop the rewrite from discarding old row versions; if the instance had one, run `VACUUM (FULL)` on the content tables again once it is idle. Schedule the first enrollments for a quiet hour.

Encrypting the live database does not reach copies made earlier. Backups, `pg_dump` files, replicas and WAL archives taken before the migration still contain that user's plaintext. After the post-check passes, the operator must purge or age out those copies (see Backups below); until then the user is only as private as the oldest backup.

#### Encryption closeout checklist

Run this once the owner's post-check reports nothing left in plaintext and before the gated cleanup migration 012 ships.

1. Confirm `users.encryption_state = 'encrypted'` for the owner and the `enc_migration_done:<user id>` marker in `cortex_meta`.
2. Purge the pre-migration backups: every nightly `pg_dump` file in the Spaces bucket taken before the migration, local dump copies, and any droplet backup or snapshot from before it. Keep none "just in case".
3. Purge WAL: delete archived WAL and base backups from before the migration, and drop any replica or restored copy of the old database.
4. Take one fresh dump after the migration and grep it for a few distinctive words from your own memories. Expect no hits.
5. Confirm `CORTEX_ENCRYPTION_ENROLL` is not set to `0` in `/etc/cortex/cortex.env`, and that the static owner token is gone from the environment.
6. Only then ship 012, which drops the legacy plaintext columns and indexes.

#### Backing up `CORTEX_SECRET_KEY`

`CORTEX_SECRET_KEY` encrypts every user's provider key. Without it the stored keys cannot be read and users must re-enter them. Copy it off the box (a password manager is fine) when it is first generated. Database backups do not contain it.

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

### Benchmark: 2 users x 100K vectors on 512 MB (recorded 2026-10-22)

Run locally (not on the droplet) in a `pgvector/pgvector:pg17` container limited to `--memory=512m --memory-swap=2560m --cpus=1`, with the settings `setup-server.sh` computes for 512 MB RAM (`shared_buffers=128MB`, `effective_cache_size=307MB`, `maintenance_work_mem=64MB`, `work_mem=4MB`, `max_connections=20`). Test: `bench_test.go` (`go test -tags bench -run TestBenchTwoUsers`). Data: 2 users x 100K random normalised 512-dim halfvec memory embeddings (generation 1), index `emb_hnsw_memory_512` built through `EnsureVectorIndex`. 200 queries per measurement. Random vectors have no cluster structure, so this is a hard case for HNSW. The Docker VM's CPU is not the droplet's, so treat absolute timings as indicative.

| Measurement | p50 | p95 |
|---|---|---|
| `searchVectorRaw` (memory, top 10), user A alone | 2.2 ms | 2.9 ms |
| same, A with B loaded | 4.1 ms | 18.6 ms |
| same, B with A loaded | 4.0 ms | 6.4 ms |
| same, A with B, index rebuilt over 200K | 3.3 ms | 4.7 ms |
| `SearchMemoryVector` top 20 (with row hydration), A with B / B with A | 10.3 / 15.0 ms | 11.6 / 29.2 ms |
| `Recall` (stub LLM and embedder, default four-way plan), A alone | 16.5 ms | 21.3 ms |
| `Recall`, A with B / B with A | 20.3 / 34.8 ms | 33.7 / 53.7 ms |
| `Recall`, A with B, index rebuilt | 19.4 ms | 34.7 ms |

Targets held: vector p95 < 200 ms and recall p95 < 500 ms (excluding LLM and embedding API time), with no OOM kill. There was one 230 ms single-query outlier in `Recall` (max, not p95).

Build and memory, the real cost on this size:

- Index build for 100K rows: 3 min 17 s. Index size 130 MB (260 MB for 200K).
- Adding user B's 100K rows into the existing index (incremental inserts): 6 min 30 s.
- Cold rebuild over 200K rows: 11 min 56 s. The index (130 MB per 100K) exceeds `maintenance_work_mem` (64 MB), so pgvector builds the graph on disk-spilling pages. Run `ensure-index` jobs off-peak and expect minutes, not seconds.
- cgroup memory sat at the 512 MB limit during and after builds (peak 512 MB), but almost all of it was reclaimable page cache (`file_cache` about 480 MB, anon 6 to 32 MB). Postgres process RSS summed 265 to 473 MB (includes shared buffers counted per process). Swap use stayed at 0 to 13 MB. No OOM kill.
- Queries are fast because the 260 MB index mostly fits in page cache on this host; on a droplet with other processes (cortex app, Caddy) cache pressure will be higher, so first queries after a cold start can be slower.

Conclusion: 512 MB + 2 GB swap passes the targets at 2 x 100K, but index builds are slow (minutes) and run at the memory limit. Resize to 1 GB or more before loading much beyond 200K vectors in total, or if builds must be quick.

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
