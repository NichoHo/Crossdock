# Crossdock

Warehouse platform where stock is conserved, every position is rebuildable from the movement log, and reservations are decided under row locks. The blueprint is `CROSSDOCK_SPEC.md`.

Status: Phases 0 to 7 are built (inventory core, inbound, outbound, task queue, REST API, floor console, offline scanner PWA, signed webhook delivery over Redpanda, cycle counting and reconciliation, returns and cash-on-delivery settlement, slotting, the single-host deployment, load tests, security hardening). It is not on a public URL yet, and sign-in is still a stand-in (see Security).

## Run it locally

You need Go, Node 20+, and Docker.

```bash
make db-up                 # PostgreSQL on 127.0.0.1:5433
make db-migrate
make generate              # a demo warehouse (id printed in the log)
```

Start the API (needs the three secrets below):

```bash
export CROSSDOCK_DATABASE_URL=postgres://crossdock:crossdock@127.0.0.1:5433/crossdock?sslmode=disable
export CROSSDOCK_SERVICE_TOKEN=<32+ characters>
export CROSSDOCK_IDENTITY_SIGNING_KEY=<32+ characters>
go run ./cmd/crossdock serve
```

Start the web app:

```bash
cd web && npm install
cp .env.example .env.local   # fill in; the two secrets must match the API
npm run build && npm start   # http://localhost:3000
```

Sign in with a user from `CROSSDOCK_WEB_USERS`. Supervisors get the console at `/console`; operators get the scanner at `/scan`. `node web/scripts/demo-seed.mjs <warehouse-id>` adds a sample ASN and orders.

## Configuration

| Variable | Used by | Meaning |
|---|---|---|
| `CROSSDOCK_DATABASE_URL` | API, CLI | PostgreSQL connection string |
| `CROSSDOCK_LISTEN` | API | Listen address, default `:8080` |
| `CROSSDOCK_SERVICE_TOKEN` | API, web | Token the web tier presents to the API (32+ chars) |
| `CROSSDOCK_IDENTITY_SIGNING_KEY` | API, web | Key that signs the forwarded user identity (32+ chars) |
| `CROSSDOCK_API_URL` | web | Where the API listens |
| `CROSSDOCK_SESSION_SECRET` | web | Signs the session cookie (32+ chars) |
| `CROSSDOCK_WEB_USERS` | web | `name:role:password,...` Stand-in for a managed auth provider |
| `CROSSDOCK_BROKERS` | API | Kafka API addresses of Redpanda, e.g. `127.0.0.1:19092`. Set together with the next variable to enable webhooks |
| `CROSSDOCK_SECRET_ENCRYPTION_KEY` | API | Base64 of 32 random bytes. Encrypts webhook signing secrets at rest. Losing it means rotating every secret |
| `CROSSDOCK_WEBHOOK_ALLOW_PRIVATE` | API | `true` lets webhooks target http and loopback/private addresses (dev only). Link-local, including cloud metadata, is always blocked |
| `CROSSDOCK_DELIVERY_WORKERS` | API | Endpoints delivered to at once, default 16 |
| `CROSSDOCK_BACKGROUND_INTERVAL` | API | How often counts are scheduled and receipts reconciled, default `1m`. Both are safe to repeat. Use `1h` when running the browser tests so no count task appears mid-test |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | API | Where to send traces (OTLP/HTTP). Unset: trace ids still propagate, spans are dropped |

The API refuses to start and lists every missing value.

## Webhooks (Phase 2)

```bash
docker compose -f deploy/docker-compose.dev.yml up -d redpanda   # Kafka API on 127.0.0.1:19092
export CROSSDOCK_BROKERS=127.0.0.1:19092
export CROSSDOCK_SECRET_ENCRYPTION_KEY=$(node -e "console.log(require('crypto').randomBytes(32).toString('base64'))")
```

An admin adds an endpoint in the console under Webhooks (URL, event filter). The signing secret appears once. Each delivery is a POST with:

| Header | Meaning |
|---|---|
| `X-Crossdock-Timestamp` | Unix seconds |
| `X-Crossdock-Signature` | `v1=<hex>`, HMAC-SHA256 of `timestamp + "." + body` with the secret. Two values (comma separated) during a secret rotation overlap |
| `X-Crossdock-Event-Id` | `evt_<sequence>`, same as `idempotency_key` in the body. Dedupe on it |
| `X-Crossdock-Replay-Of` | Present on replays |
| `traceparent` | W3C trace context of the request that caused the event |

Reject timestamps more than five minutes old. Delivery is at least once; one shipment's events arrive in order. After 10 failures in a row an endpoint pauses for five minutes, then one probe decides. After 20 attempts (backoff 1 s to 1 h with full jitter) an event becomes a dead letter that a supervisor can replay.

`node web/scripts/webhook-sink.mjs` is a ready receiver that checks the signature. `go run ./cmd/webhook-load` runs the fan-out load scenario; results are in `loadtest/results/`.

## Counting and reconciliation (Phase 3)

**Cycle counts.** Every minute (or on demand, Reconciliation > Schedule due counts) the system creates a count task for each location that is due: A-class weekly, B-class monthly, C-class quarterly by the fastest SKU stored there, and any location where a pick or putaway ended in an exception. The operator counts blind on the scanner: the sheet lists what to look for, never how many the system expects.

**Variances.** `variance = system - counted`, so a positive number means stock is missing. Each difference is classified by rules and confirmed by a supervisor in the workbench. An adjustment cannot post without a confirmed class.

| Class | Rule | Action when confirmed |
|---|---|---|
| `timing` | The count equals the shelf quantity at some moment while counting ran (start quantity plus any subset of the movements that committed meanwhile) | none |
| `miscount` | A recount within an hour returns the system quantity | none, first count discarded |
| `damaged` | The operator marked units damaged | `quarantine` movement |
| `shrinkage` | Recount agrees with a lower first count | `count_adjust` to `VARIANCE`, flagged for review |
| `found` | Recount agrees with a higher first count | `count_adjust` from `VARIANCE` |
| `unresolved` | Anything else, or a recount after an hour | stays open and ages |

A difference no in-flight movement explains is never judged from one count: it creates a recount task first. "In flight" is decided with Postgres snapshots recorded at count start and end, not sequence numbers, because a transaction takes its sequence number before it commits.

**Inbound breaks.** For each ASN line the system compares what the supplier said, what was scanned and what was put away: `short_shipped`, `over_shipped`, `putaway_incomplete` (after a 2 hour grace, or at once when a putaway is in exception), `lot_mismatch`. Breaks clear themselves when the cause goes away; a supervisor can waive one with a note.

**Ageing.** Open variances and breaks are bucketed under 7 days, 7 to 30, 30 to 90, over 90. The over 90 bucket is the number to bring to zero.

## Returns and cash on delivery (Phase 4)

Money is stored in minor units (satang, cents), one currency per deployment.

**The loop.** A shipment is dispatched with a COD amount. The courier reports an outcome (`POST /v1/warehouses/{wid}/delivery-outcomes`, by tracking number, with an `outcomes:write` token):

| Outcome | Effect |
|---|---|
| `delivered_collected` | The courier now owes the COD amount (ledger `cod_receivable`) |
| `rejected` | A return authorisation opens (reason `cod_rejected`), an intake task goes on the board, and the courier's shipping cost is booked as lost |
| `lost` | A claim is booked against the courier |

When the rejected parcel arrives, the operator scans its tracking number on the scanner (Return), then each kind of item with a condition (good, damaged, not sure). Each line is a `return_receive` movement from `RETURN_INTAKE` into the returns zone and creates a disposition task. The operator then restocks, quarantines or scraps it (`disposition` movement). Only units marked good can be restocked. A parcel cannot bring back more than was shipped.

**One timeline.** `Console > Settlement > COD shipments > (tracking number)` shows dispatch, outcome, return, where the units went, what it cost and what the remittance said, in time order.

**Remittance files.** Upload a CSV with columns `courier_ref, tracking, collected, fee, net` (Settlement > Remittances, or `POST /v1/warehouses/{wid}/remittances?carrier=JNT` with a `remittances:write` token). A file with any bad row is refused whole, with every bad row named. The same file twice does nothing. Each line is matched against the shipment and its outcome using the courier's agreed terms (Settlement > Courier terms: COD fee as a percentage plus a flat part, shipping cost, days to pay):

| Case | Rule |
|---|---|
| `short_remitted` | Net is less than COD minus the agreed fee |
| `unremitted` | Delivered, but no remittance line within the courier's window (found by a scan that runs every minute, or on demand) |
| `remitted_for_rejected` | Paid for a parcel the courier also reported rejected |
| `unknown_shipment` | A tracking number this warehouse never dispatched |
| `duplicate_remittance` | The shipment was already remitted |

A late payment clears its `unremitted` case by itself. A supervisor resolves any other case as recovered, written off or not an error, with a note; written-off money needs a classified reason (courier fee dispute, customer short paid, lost in transit, data error, goodwill). Open cases age in the workbench with the count variances and inbound breaks.

**Try it without a courier.**

```bash
crossdock courier simulate -warehouse <id>                      # give dispatched COD shipments outcomes
crossdock remittance generate -warehouse <id> -carrier JNT -out jnt.csv   # a file with a known set of breaks
```

`jnt.csv.expected.json` lists the breaks the file should produce. Upload `jnt.csv` in the console and compare.

## Slotting (Phase 5)

A Python job (`cmd/slotting/slotting.py`, OR-tools CP-SAT with a greedy fallback) reads 90 days of completed picks and proposes at most N moves of fast sellers toward the dock and of SKUs ordered together into the same aisle. It writes a proposal and nothing else.

**The platform measures the saving, not the solver.** `crossdock slotting replay` and the background tick replay the historical waves through the same router that builds real pick routes (nearest neighbour plus 2-opt), once on the layout as it is and once with the proposal applied. The headline number is that replayed distance. The solver's own objective is shown as a footnote because it is a weighted score, not metres. A proposal can be accepted only with a positive replayed saving. Accepting creates one `relocate` task per position; the scanner walks old location, product, then the exact new location. Reserved stock is never moved. After the moves, real waves are compared with the same waves before (Slotting > proposal > "What the floor did").

```bash
py -3.12 -m venv cmd/slotting/.venv && cmd/slotting/.venv/Scripts/pip install -r cmd/slotting/requirements.txt
crossdock generate history -warehouse <id>      # 120 waves of pick history over 60 days
crossdock slotting run -warehouse <id>          # solve, write a proposal, replay it
make test-py                                    # job unit tests
```

Console: Slotting (proposals, moves, accept or reject) and Floor map (pick heat per location; cell counts and dashed edges, so colour is not the only signal).

## Deployment, observability and load (Phase 6)

**Deploy.** `deploy/docker-compose.prod.yml` runs Caddy (TLS), the web app, the API, PostgreSQL, Redpanda, Prometheus, Grafana and Jaeger on one host. Only Caddy publishes ports. The runbook, settings table and upgrade path are in [deploy/README.md](deploy/README.md). I ran the stack on a laptop with a local certificate (`SITE_ADDRESS=localhost`) and checked: HTTPS answers, `/metrics` returns 404 through the proxy, PostgreSQL publishes no port, Prometheus scrapes the API, and the Grafana board loads. **It is not on a public URL yet.** That needs a VM and a domain.

**Synthetic operators.** `crossdock synthetic -warehouse W` runs scripted shifts over the real HTTP API: deliveries are received and put away, orders are taken and waved, operators claim and confirm pick, pack and count tasks at a human pace, packed orders ship. Returns are not scripted. Run it with `docker compose ... --profile synthetic up -d`. A 75 second run confirmed 51 tasks and shipped 12 orders with no errors.

**Dashboard.** `deploy/grafana/dashboards/crossdock.json`: the four ageing buckets as the top row, then movements by kind, tasks by operator, API latency, dead letter depth, delivery success rate and outbox lag. The API serves `/metrics` itself (`internal/api/metrics.go`, no client library). Missing: scanner offline queue depth, because scanners do not report it.

**Backups.** `deploy/backup.sh` (systemd timer, every 6 hours) and `deploy/restore-drill.sh`, which restores into a scratch database and checks conservation, projection fidelity, immutability and row counts. Drill log: [docs/restore-drills.md](docs/restore-drills.md). The one recorded drill ran on a laptop, not the production host.

### Load results

Run with `make load` against a running stack. Host for all rows: Windows 11, 16 CPUs, Docker Desktop for PostgreSQL and Redpanda, the API and the load generator on the same machine. The rows below were run before the code was in git, so their commit field says `unversioned`. `make load` now records the current commit. Raw results are in `loadtest/results/`.

| Scenario | Result | Target | Met |
|---|---|---|---|
| Allocation burst: 1,000 orders in 10 s, contention on 50 SKUs | 10.3 s total, 0 errors, 440 allocated, 1 partial, 559 unallocatable (supply was short on purpose), reservation p50 14 ms, p99 32 ms, invariants hold | p99 under 250 ms, conservation holds | Yes |
| Pick storm: 100 operators, 1 pick per second, 60 s, 500 SKUs | 6,000 picks, 0 errors, confirm p50 186 ms, p99 284 ms, invariants hold | no errors, p99 under 200 ms | **No** (p99) |
| Pick storm, same, but only 50 SKUs | 6,000 picks, 0 errors, p50 212 ms, p99 437 ms | | **No** (p99) |
| Offline sync wave: 50 scanners, 100 queued commands each, at once | 5,000 applied in 15.4 s, 50 retries answered as duplicates, 50 conflicts rejected with a reason | all processed in 60 s, every conflict rejected with a reason | Yes |
| Webhook fan-out: 10,000 deliveries to 100 endpoints, 10 flaky | Passed. Ordering held, healthy endpoints finished in 8.1 s, 100 dead letters as expected | see [spec](CROSSDOCK_SPEC.md) | Yes |
| Count under load: 100 pickers, counts running on 10 locations | 246 counts, 30 variances, all timing, 0 recounts, 0 adjustments. A one unit loss under the same load is never called timing | zero false shrinkage | Yes |

What the numbers do not show:

- The pick storm misses the 200 ms target on this host. With 50 SKUs, pickers queue on the same stock position row, and p99 rises from 284 to 437 ms. That is expected behaviour of the design: one position changes at a time. The remaining 186 ms median, against 37 ms with a single operator, is **not explained**. I suspect the single machine (about 2,000 statements per second through Docker Desktop's port proxy, with the load generator competing for CPU), but I have not shown it. The run should be repeated on the Linux VM before the number is quoted as the platform's.
- Two things the storm found and I fixed: a task claim for one kind read every open task of the warehouse (now an index read, `internal/tasks`), and the test's first version released one wave of 200 orders, which put all operators on the same position. Real operators work different waves, so waves are now 20 orders.
- Count under load uses 10 locations, not the 20 in the plan. Webhook fan-out and count under load run as Go tests (`cmd/webhook-load`, `internal/counting`) instead of `k6`, because they need the database inside the test. The other three are Go too (`loadtest/`, build tag `load`), because the invariant checks are Go. `k6` is not used.

### Write-up

[A cycle count cannot be compared to a number that keeps moving](docs/writeup-counting-under-load.md): how counts are classified as timing or loss while pickers work, and the PostgreSQL snapshot detail that makes it exact.

## Security and hardening (Phase 7)

Tier T2 (internal team, synthetic data). The control list, how each was checked, what the audit fixed and what is still open are in [docs/security.md](docs/security.md). Short version: rate limits, sign-in throttle, a script-nonce CSP and other headers, log redaction, a lint test against string-built SQL, dependency scans (clean after a Go patch bump), and sessions that end when a user is removed are done and tested. **The sign-in is still a stand-in** (users and passwords in an environment variable), two-factor and billing caps are yours to set, and the service worker is checked on localhost but not yet on the real host.

When load testing, start the API with `CROSSDOCK_RATE_LIMIT_USER=0` or the load generator, which acts as one user, will be limited.

## Tests

```bash
make test-race     # Go: needs CROSSDOCK_TEST_DB_URL (the dev database works)
make invariants    # the seven invariants through the real workflows
cd web && npm test # scan rules and offline queue
make e2e           # Playwright: scanner cycle, offline pick, count and recount, return and remittance, webhook trace. Needs API and web running
CROSSDOCK_TEST_BROKERS=127.0.0.1:19092 go test ./internal/delivery/   # adds the real Redpanda round trip
```

## Invariants (spec 6.1)

| # | Test |
|---|---|
| 1 Conservation, 2 Position identity, 3 Projection fidelity, 6 Immutability | `internal/inventory/invariants_test.go` |
| 4 Reservation bound | `TestInvariant4_ReservationBound`, `TestAllocationReservationBound` |
| 5 Release exactness | `TestInvariant5_ReleaseExactness`, `TestCancelReleasesExactlyOnce` |
| 7 Task-movement coupling | `TestInvariant7_TaskMovementCoupling` |

Every workflow test ends with `wmstest.CheckInvariants`, so a test that leaves the database inconsistent fails.

## API

`api/openapi.yaml` is the contract. A test fails if it and the router disagree on endpoints. The TypeScript types in `web/src/lib/api-schema.d.ts` come from it (`npm run gen:api`).
