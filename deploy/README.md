# Deploying Crossdock on one host

One virtual machine runs everything in Docker Compose. Only Caddy opens ports (80 and 443). PostgreSQL has no published port.

| Service | Job | Reachable from |
|---|---|---|
| caddy | TLS and routing | the internet |
| web | Console and scanner (Next.js) | caddy |
| api | Go API, background jobs, webhook delivery | caddy (`/v1/*` only), web |
| db | PostgreSQL 16 | api, migrate |
| redpanda | Event bus | api |
| prometheus | Metrics store (30 days) | grafana |
| grafana | Dashboards, served at `/grafana/` | caddy |
| jaeger | Trace viewer, served at `/jaeger` | caddy |
| synthetic | Scripted operators (profile `synthetic`) | api |

`/metrics` returns 404 through Caddy. Prometheus reads it on the Compose network.

## First deploy

1. Use a Linux VM with Docker and the Compose plugin. 4 GB of RAM is the minimum (Redpanda uses 768 MB).
2. Point a DNS name at the VM. Open ports 80 and 443.
3. Copy the code to `/opt/crossdock`.
4. Copy `deploy/.env.example` to `deploy/.env`. Fill in every value. Make secrets with `openssl rand -base64 36`.
5. Start the stack:

   ```bash
   docker compose -f deploy/docker-compose.prod.yml --env-file deploy/.env up -d --build
   ```

   The `migrate` service applies the migrations first. The API waits for it.
6. Make a warehouse of synthetic data:

   ```bash
   docker compose -f deploy/docker-compose.prod.yml --env-file deploy/.env run --rm api generate "Demo" wh-demo
   ```

7. Start the synthetic operators:

   ```bash
   docker compose -f deploy/docker-compose.prod.yml --env-file deploy/.env --profile synthetic up -d
   ```

8. Start on boot and back up on a timer:

   ```bash
   sudo cp deploy/systemd/* /etc/systemd/system/
   sudo systemctl enable --now crossdock.service crossdock-backup.timer
   ```

To try the stack on a laptop, set `SITE_ADDRESS=localhost`, `HTTP_PORT=8088`, `HTTPS_PORT=8443`. Caddy then makes its own local certificate.

## Settings

Every setting is an environment variable. The API checks all of them at start and lists every bad value in one message.

| Variable | Used by | Rule |
|---|---|---|
| `SITE_ADDRESS` | caddy | Public host name |
| `POSTGRES_PASSWORD` | db, api | Required |
| `GRAFANA_ADMIN_PASSWORD` | grafana | Required |
| `CROSSDOCK_SERVICE_TOKEN` | web, api | 32 characters or more |
| `CROSSDOCK_IDENTITY_SIGNING_KEY` | web, api | 32 characters or more |
| `CROSSDOCK_SESSION_SECRET` | web | 32 characters or more |
| `CROSSDOCK_SECRET_ENCRYPTION_KEY` | api | 32 bytes, base64. Encrypts webhook secrets |
| `CROSSDOCK_WEB_USERS` | web | `name:role:password`, comma separated |
| `CROSSDOCK_BROKERS` | api | Set by Compose to `redpanda:9092` |
| `CROSSDOCK_BACKGROUND_INTERVAL` | api | Default `1m` |
| `CROSSDOCK_DELIVERY_WORKERS` | api | 1 to 512. Default 16 |
| `SYNTHETIC_WAREHOUSE`, `SYNTHETIC_OPERATORS`, `SYNTHETIC_EVERY` | synthetic | Defaults `wh-demo`, 4, `20s` |

## Backups and the restore drill

`deploy/backup.sh` writes a compressed `pg_dump` to `backups/` every 6 hours and deletes dumps older than 14 days. If you set `BACKUP_S3_URI`, it also uploads the dump with the `aws` CLI. The upload step is not tested.

Once a month, run the drill:

```bash
deploy/restore-drill.sh
```

The script restores the newest dump into a scratch database. It then checks four things: all positions sum to zero, positions equal a replay of the movements log, the movements table still refuses `UPDATE`, and the main tables have rows. It exits with an error if any check fails. Add a line to `docs/restore-drills.md` with the result.

To restore for real: stop `api`, restore into an empty database with `pg_restore --no-owner`, then start `api`.

## Rotating secrets

Rotate the database password, the service token and the signing key on a schedule, and at once after any suspected leak. Change the value in `deploy/.env`, then run `up -d`. The service token and the signing key must change in `web` and `api` together. Existing browser sessions end when `CROSSDOCK_SESSION_SECRET` changes.

## Not built, on purpose

There is no Kubernetes, managed database or second region. One host handles this system's real load. If one host is not enough, the first steps are: move PostgreSQL to a managed service, run several `api` containers behind Caddy (the background jobs are safe to run twice), and move Redpanda to its own host.

## Known gaps

- Grafana allows anonymous viewing and `/jaeger` has no login. This is fine for synthetic data only. Phase 7 puts both behind authentication.
- The dashboard has no panel for scanner offline queue depth. Scanners do not report it yet.
- Metrics come from one API process. With several API processes, the request histograms need a per-instance label.
