# Crossdock Project Spec

**Status:** greenfield. This document is the blueprint for a new repository and
becomes that repository's `BLUEPRINT.md` on day one.

**What it is:** an event-driven warehouse and fulfilment platform. Inbound
receiving, putaway, picking, packing, shipping, cycle counting, returns, and
cash-on-delivery settlement, with inventory correctness proven rather than
assumed.

**What it is not:** an enterprise resource planning system, a transport
management system, or a marketplace. It is the system of record for what is
physically in a warehouse and where, plus the settlement plumbing that the
physical movements imply.

**Provenance:** the author built and operated a warehouse management system
professionally for two years. That system was a classic server-rendered
monolith with state held in session and correctness held in people's heads.
This project is what that system should have been.

---

## Table of contents

1. [Problem](#1-problem)
2. [Target state](#2-target-state)
3. [Domain model](#3-domain-model)
4. [Scope and non-goals](#4-scope-and-non-goals)
5. [Architecture](#5-architecture)
6. [Inventory core](#6-inventory-core)
7. [Event delivery](#7-event-delivery)
8. [Counting and reconciliation](#8-counting-and-reconciliation)
9. [Returns and cash on delivery](#9-returns-and-cash-on-delivery)
10. [Slotting](#10-slotting)
11. [Interfaces](#11-interfaces)
12. [Design system](#12-design-system)
13. [Cross-cutting concerns](#13-cross-cutting-concerns)
14. [Test strategy](#14-test-strategy)
15. [Load test plan](#15-load-test-plan)
16. [Phases](#16-phases)
17. [Security and hardening](#17-security-and-hardening)
18. [Documentation plan](#18-documentation-plan)

---

## 1. Problem

A warehouse management system has one job that matters: the number in the
system must equal the number on the shelf. Every other feature exists to keep
those two numbers together as things move.

Real systems fail this constantly, and they fail in the same few ways:

- **Double allocation.** Two pickers are sent for the last unit. One arrives at
  an empty slot. The order ships short and nobody knows why until the customer
  complains.
- **Phantom stock.** A cancelled order releases its reservation twice, or not at
  all. The system shows units that do not exist, or hides units that do.
- **Unreconcilable counts.** A physical count disagrees with the system. The
  adjustment is posted with no classification, so the same discrepancy recurs
  every quarter and nobody can say whether it is theft, damage, or a counting
  process that runs while picks are in flight.
- **Silent settlement breaks.** A cash-on-delivery parcel is rejected. The
  courier's remittance is short. The parcel comes back to the warehouse. Three
  systems each see one third of the event, and the money and the stock drift
  apart with no one responsible for the join.

Every one of these is a correctness problem under concurrency, and every one
of these is usually handled by a person with a spreadsheet.

---

## 2. Target state

**Crossdock** is a warehouse platform where every stock movement is a
double-entry event, every reservation is an atomic decision under lock, every
count produces a classified variance, and every settlement outcome joins back to
the physical parcel that caused it.

It runs as one deployable service with a floor console for supervisors, a
scanner interface for operators that works with no signal, an outbound event
feed for the systems around it, and a reconciliation workbench that turns
discrepancies into a queue someone can work through.

### Design commitments

1. **Stock is conserved.** Every movement debits one location and credits
   another. Virtual locations represent the outside world. The signed sum of all
   movements across all locations is zero at all times. Units are never created
   or destroyed by the system, only moved.
2. **Every position is derivable from the log.** The event log is the source of
   truth. Every cached position can be rebuilt from it and a build-breaking test
   proves the rebuild matches.
3. **Reservation is a serialised decision.** Two operators can never win the
   same unit. This is enforced by the database, not by application convention,
   and tested under the race detector.
4. **Every mutating operation is idempotent.** A retried scan, a retried API
   call, or a retried offline sync never has a second effect.
5. **State changes and their events commit together.** Transactional outbox,
   relayed at least once, consumed idempotently.
6. **Discrepancies are classified, not just posted.** A variance carries a
   reason and an age. An unclassified adjustment is a bug.
7. **The floor interface assumes gloves, glare, and no signal.** Anything that
   fails on a real warehouse floor is a defect, regardless of how it looks on a
   laptop.

### Honest framing

Crossdock is a demonstration platform with synthetic data. There is no real
inventory, no real money, no real courier. The cash-on-delivery model reflects
how the flow actually works in Southeast Asian e-commerce, because the author
has seen it, but the remittance files are generated, not received. The
slotting optimiser is illustrative and its improvement figures are measured
against the platform's own simulation, which is stated wherever they appear.

---

## 3. Domain model

Vocabulary is the vocabulary of the trade. It is not simplified.

### 3.1 Physical structure

| Term | Meaning |
|---|---|
| **Warehouse** | One physical site. The platform supports several. |
| **Zone** | A region of a warehouse with a purpose: receiving, bulk, pick face, packing, returns, quarantine. |
| **Location** | An addressable slot, coded `AISLE-BAY-LEVEL-POSITION`, for example `A07-12-C-2`. Belongs to one zone. Has a capacity in units or volume. |
| **Virtual location** | A location that stands for the outside world: `SUPPLIER`, `CUSTOMER`, `CARRIER`, `VARIANCE`, `SCRAP`, `RETURN_INTAKE`. These can hold a negative balance, which is how conservation works. |

### 3.2 Stock

| Term | Meaning |
|---|---|
| **SKU** | A stock keeping unit. A distinct product with a barcode. |
| **Lot** | A batch of one SKU that shares a receipt date and, if applicable, an expiry. Traceability is per lot. |
| **LPN** | Licence plate number. A physical container (pallet, tote, carton) with its own barcode. Stock moves by LPN on the floor; the system tracks what each LPN holds. |
| **Position** | The quantity of one (SKU, lot) at one location, optionally inside one LPN, split by state. |
| **State** | `available`, `reserved`, `in_transit`, `quarantined`, `damaged`. A position holds a quantity per state. |

### 3.3 Movement

Every change to stock is a **movement**: a quantity of one (SKU, lot) leaving one
location and arriving at another, with a state on each side. A movement is
immutable once written. Corrections are new movements in the other direction.

Movement kinds, with their from and to:

| Kind | From | To | Trigger |
|---|---|---|---|
| `receive` | `SUPPLIER` | receiving location | ASN line scanned |
| `putaway` | receiving location | storage location | putaway task confirmed |
| `reserve` | location (`available`) | same location (`reserved`) | order allocated |
| `release` | location (`reserved`) | same location (`available`) | order cancelled or reallocated |
| `pick` | storage location (`reserved`) | pick cart LPN (`in_transit`) | pick confirmed |
| `pack` | pick cart LPN | packing station | pack confirmed |
| `ship` | packing station | `CARRIER` | carrier handoff scanned |
| `count_adjust` | location or `VARIANCE` | `VARIANCE` or location | variance approved |
| `quarantine` | location (`available`) | same location (`quarantined`) | hold applied |
| `return_receive` | `RETURN_INTAKE` | returns location | return scanned |
| `disposition` | returns location | storage, quarantine, or `SCRAP` | disposition decided |
| `relocate` | location | location | slotting move confirmed |

A state change inside one location is still a movement. This keeps the model
uniform and makes the reserved-versus-available ledger auditable.

### 3.4 Inbound

**Purchase order** → **ASN** (advance shipping notice: what the supplier says is
coming, by SKU, lot, and quantity, often by LPN) → **receipt** (what actually
arrived, scanned line by line) → **putaway task** (where it should go, executed
by an operator).

The gap between ASN and receipt is the first reconciliation the platform does.

### 3.5 Outbound

**Order** → **allocation** (reservation of specific positions, the serialised
decision) → **wave** (a batch of orders released to the floor together, with a
pick route) → **pick tasks** → **pack** → **shipment** (carrier, tracking number,
optional COD amount) → **carrier handoff**.

### 3.6 Counting

**Cycle count task** (a location or a SKU, scheduled by ABC velocity class or
by exception) → **count** (what the operator saw) → **variance** (system minus
count, with in-flight tasks noted) → **classification** → **adjustment**
movement if approved.

### 3.7 Returns and settlement

**Return authorisation** → **return receipt** → **disposition**.

**COD shipment** → **delivery outcome** (`delivered_collected`, `rejected`,
`lost`) → **courier remittance** (a periodic file listing what the courier
collected and is paying over) → **remittance match** → **settlement break** if
the amounts disagree.

### 3.8 Tasks

Everything an operator does is a **task**: putaway, pick, pack, count, return
intake, relocate. Tasks have a state machine: `open` → `assigned` → `in_progress`
→ `completed` | `cancelled` | `exception`. Tasks are what the scanner interface
shows.

---

## 4. Scope and non-goals

### In scope

- One or more warehouses with the full physical model above.
- Inbound, outbound, counting, returns, and COD settlement flows end to end.
- A supervisor console (web) and an operator scanner interface (mobile web,
  offline first).
- An outbound webhook feed for order, shipment, and stock events.
- A reconciliation workbench with classified breaks and ageing.
- A slotting optimiser with simulated before-and-after evidence.
- An OpenAPI-described REST API with generated TypeScript and Go clients.
- Observability: traces, metrics, structured logs, one dashboard.
- A real deployment behind TLS with synthetic operators running scripted shifts.

### Non-goals

- Purchasing, supplier management, or pricing. Purchase orders are inputs.
- Carrier rate shopping, label generation, or tracking. Shipments carry a
  carrier code and a tracking string supplied by an upstream system.
- A marketplace, storefront, or order capture. Orders are inputs.
- Multi-tenancy. One operator organisation per deployment.
- Native mobile apps. The scanner interface is a progressive web app. See 11.2
  for why and for the upgrade path.
- Kubernetes. Single-host deployment. See 13.5.
- Real money, real couriers, real inventory.

---

## 5. Architecture

A modular monolith in Go, one PostgreSQL database, one event bus introduced in
Phase 2, one web application, one batch job.

```
crossdock/
  cmd/
    crossdock/          the service binary: API, workers, projections
    slotting/           Python batch job (Section 10)
  internal/
    inventory/          positions, movements, reservation, invariants
    inbound/            purchase orders, ASN, receipts, putaway tasks
    outbound/           orders, allocation, waves, picks, packs, shipments
    counting/           cycle count tasks, variances, classification
    returns/            authorisations, return receipts, dispositions
    settlement/         COD outcomes, remittance ingest, matching, breaks
    delivery/           webhook endpoints, signing, retry, DLQ, replay
    tasks/              the operator task queue shared by all domains
    eventstore/         append, stream, snapshot, rebuild
    projections/        read models, rebuilt from the log
    api/                HTTP handlers, OpenAPI, auth middleware
    telemetry/          otel, slog, prometheus
  web/                  Next.js: floor console + scanner PWA
  migrations/           golang-migrate
  api/openapi.yaml      the contract, clients generated from it
  deploy/               compose, Caddyfile, systemd unit
  loadtest/             k6 scenarios
```

### 5.1 Event sourcing, the practical kind

The `movements` table is an append-only event log and is the source of truth
for stock. Each row is one movement from 3.3 with a monotonic sequence number
per warehouse.

The `positions` table is a projection: current quantity per (SKU, lot, location,
LPN, state). It is updated in the same database transaction as the movement
that changes it, so it is never stale relative to the log. It exists because
querying "what is available at `A07-12-C-2`" by summing the log is too slow at
scale and too easy to get wrong in application code.

`crossdock rebuild positions` truncates the projection and replays the log. A
build-breaking test asserts the rebuilt table equals the live table row for row.
This is the proof that the projection is a cache and not a second source of
truth.

Other projections (task queues, order status, stock by SKU, ageing reports)
are rebuilt the same way. Some are updated transactionally, some asynchronously
from the bus, and the API documents which.

`ponytail:` no snapshotting of aggregates, no generic event store framework.
The log is a table with a sequence column. Add snapshots only when a rebuild
takes longer than a coffee.

### 5.2 Outbox and bus

Every state change that other systems need to know about writes an outbox row
in the same transaction. The author's existing `outboxkit` module relays outbox
rows to the bus at least once and provides the idempotent consumer guard.

**Phase 1 runs on PostgreSQL alone.** The outbox is relayed by a worker inside
the service binary to in-process consumers. This is enough for projections and
for the first version of the task queue.

**Phase 2 introduces Redpanda** when the webhook delivery subsystem needs
durable fan-out with per-endpoint consumer groups. Nothing above the outbox
changes; only the relay target does.

`ponytail:` do not add the bus until Phase 2 needs it. A bus with one consumer
is a queue with extra steps.

### 5.3 Concurrency model

Reservation, pick confirmation, and count adjustment take a row lock on the
affected `positions` rows with `SELECT … FOR UPDATE`, in a consistent order
(warehouse, location code, SKU, lot, LPN) to prevent deadlock, then append the
movement and update the position in one transaction.

This is the same pattern as a double-entry money ledger with account locks, and
it is chosen for the same reason: it is the simplest thing that is provably
correct, and PostgreSQL enforces it.

Offline scanner operations are commands with client-generated idempotency keys.
They are applied on sync with the same locks. A command that no longer applies
(the unit was picked by someone else while the scanner was offline) is rejected,
and the rejection is shown to the operator with the reason.

### 5.4 Request flow

```
scanner PWA ──┐
              ├──► Next.js (BFF, session) ──► Go API ──► domain package ──► tx { movement + position + outbox }
floor console ┘                                                                        │
                                                                                       ▼
external systems ◄── delivery workers ◄── Redpanda ◄── outboxkit relay ◄── outbox table
```

The Next.js layer holds the session and talks to the Go API with a service token.
The browser never holds an API token.

---

## 6. Inventory core

### 6.1 Invariants

These are the correctness proof and they are build-breaking tests.

1. **Conservation.** For every warehouse, the signed sum of quantities across
   all movements is zero. Equivalently, the sum of all positions across real and
   virtual locations is zero.
2. **Position identity.** For every (SKU, lot, location, LPN):
   `on_hand = available + reserved + in_transit + quarantined + damaged`, and
   every term is non-negative for real locations.
3. **Projection fidelity.** `rebuild positions` produces a table identical to
   the live one.
4. **Reservation bound.** Reserved quantity at a position never exceeds what
   was available at the moment of reservation. Fifty concurrent reservation
   attempts against one available unit produce exactly one success and
   forty-nine rejections, under the race detector, with the log showing exactly
   one `reserve` movement.
5. **Release exactness.** Cancelling an allocation releases exactly the reserved
   quantity, exactly once, regardless of how many times the cancel is retried.
6. **Movement immutability.** No `UPDATE` or `DELETE` is possible on
   `movements`. Enforced by a database rule and tested.
7. **Task-movement coupling.** Every `pick`, `putaway`, `count_adjust`, and
   `disposition` movement references a completed task, and every completed task
   of those kinds has exactly the movements it should.

### 6.2 Allocation strategy

When an order is allocated, the platform chooses positions by a documented
policy: earliest expiry first for lotted stock, then pick-face zone before bulk,
then lowest location code. The policy is a function with a table of test cases.
It is not clever, and it does not need to be.

Partial allocation is allowed and recorded. An order that cannot be fully
allocated stays `partially_allocated` and reappears in the next allocation
sweep.

### 6.3 Wave release and pick routing

A wave groups allocated orders. Releasing a wave creates pick tasks ordered by a
route through the warehouse. The route is computed over a floor graph (locations
as nodes, aisles as edges with distances) using a nearest-neighbour heuristic
with a 2-opt improvement pass. This is deliberately simple. The slotting work in
Section 10 is what changes the numbers; the router just needs to be consistent.

Route distance is recorded per wave. It is the metric the slotting optimiser is
measured against.

---

## 7. Event delivery

External systems subscribe to events by registering an endpoint.

### 7.1 Event catalogue

| Event | Emitted when |
|---|---|
| `receipt.completed` | all ASN lines for a receipt are scanned or marked short |
| `stock.moved` | any movement, with kind, from, to, quantity |
| `order.allocated`, `order.partially_allocated`, `order.unallocatable` | allocation sweep |
| `shipment.dispatched` | carrier handoff scanned |
| `shipment.delivery_outcome` | courier reports delivered, rejected, or lost |
| `count.variance_classified` | supervisor classifies a variance |
| `return.received`, `return.dispositioned` | returns flow |
| `settlement.break_opened`, `settlement.break_resolved` | remittance matching |

Every event carries an `id`, an `occurred_at`, a `sequence` per warehouse, and
an `idempotency_key` equal to its `id`.

### 7.2 Delivery semantics

- **Signing.** Each payload is signed with HMAC-SHA256 over
  `timestamp + "." + body` using the endpoint's secret. Headers carry the
  timestamp and the signature. Receivers reject timestamps outside a
  five-minute window, which defeats replay.
- **At least once.** Delivery is retried until the endpoint returns 2xx or the
  retry budget is exhausted.
- **Ordering.** Events for one shipment are delivered in sequence order to one
  endpoint. A `shipment.delivery_outcome` never arrives before its
  `shipment.dispatched`. Ordering key is the shipment or order ID; events with
  no natural key are unordered.
- **Backoff.** Exponential with full jitter, base one second, cap one hour,
  budget of twenty attempts across roughly three days.
- **Circuit breaker.** Per endpoint. After ten consecutive failures the endpoint
  is `open` for five minutes and its queue pauses. Half-open probes one event.
  One failing customer never starves the queue for the others.
- **Dead letter.** After the budget, the event moves to the dead letter store
  with every attempt's response recorded.
- **Replay.** A supervisor can replay one event, every dead-lettered event for
  an endpoint, or every event in a time range for an endpoint. Replays are
  ordinary deliveries with a `replay_of` header.

### 7.3 Endpoint management

Endpoints are created in the console with a URL, a generated secret shown once,
and an event filter. The secret is stored hashed for comparison and encrypted at
rest for signing. Rotation issues a second secret with an overlap window during
which both are accepted.

---

## 8. Counting and reconciliation

### 8.1 Cycle counting

Locations are counted on a schedule by velocity class: A-class weekly, B-class
monthly, C-class quarterly, plus any location touched by an exception (a short
pick, a putaway to a full slot). A count task assigns a location to an operator,
who scans the LPNs and confirms quantities.

A count is a snapshot with the sequence number of the log at the moment the
count started. This is what makes timing classification possible: any movement
at that location between the count's start sequence and its completion is
recorded against the count as `in_flight`.

### 8.2 Variance classification

`variance = system_quantity - counted_quantity`, per (SKU, lot, location).

| Classification | Rule | Action |
|---|---|---|
| `timing` | in-flight movements at the location explain the variance exactly | no adjustment, count closes |
| `miscount` | a recount within one hour returns the system quantity | no adjustment, first count discarded |
| `damaged` | operator marked units damaged during the count | `quarantine` movement, not an adjustment |
| `shrinkage` | negative variance, no in-flight explanation, recount agrees | `count_adjust` to `VARIANCE`, flagged for review |
| `found` | positive variance, recount agrees | `count_adjust` from `VARIANCE` |
| `unresolved` | none of the above | stays open, ages |

Classification is proposed by the rules and confirmed by a supervisor. An
adjustment cannot post without a classification.

### 8.3 Three-way inbound reconciliation

For every receipt: the ASN line (what the supplier said), the receipt line (what
was scanned), and the putaway confirmations (what reached a shelf). Breaks are
classified `short_shipped`, `over_shipped`, `putaway_incomplete`, `lot_mismatch`,
and surface in the workbench with the supplier and the ASN reference.

### 8.4 Ageing

Every unresolved variance and settlement break has an opened date. The
workbench groups them into buckets: under 7 days, 7 to 30, 30 to 90, over 90.
The over-90 bucket is the number a warehouse manager is judged on.

---

## 9. Returns and cash on delivery

This is where physical logistics and money meet, and it is the part of the
platform that most warehouse systems leave to someone else.

### 9.1 The COD lifecycle

1. A shipment is dispatched with `cod_amount` and the courier code.
2. The courier reports an outcome:
   - `delivered_collected`: the customer paid the courier. The courier now owes
     the platform's operator that amount.
   - `rejected`: the customer refused. The parcel is coming back. The shipping
     cost is lost. No money changes hands.
   - `lost`: the courier lost it. A claim is owed.
3. Periodically the courier sends a **remittance**: a file listing shipments,
   amounts collected, fees deducted, and the net paid over.
4. The platform matches remittance lines to `delivered_collected` outcomes.

### 9.2 Remittance matching and breaks

| Break | Meaning |
|---|---|
| `short_remitted` | courier remitted less than the COD amount minus agreed fees |
| `unremitted` | outcome is `delivered_collected` but no remittance line after the courier's settlement window |
| `remitted_for_rejected` | the courier paid for a parcel it also reported rejected |
| `unknown_shipment` | a remittance line for a tracking number the platform never dispatched |
| `duplicate_remittance` | the same shipment remitted twice |

Each break opens a settlement case with the shipment, the outcome event, the
remittance line, and the delta. Resolution is a supervisor action with a note
and, if money is written off, a classified reason.

### 9.3 Reverse logistics

A `rejected` outcome creates a **return authorisation** automatically with reason
`cod_rejected`. When the parcel arrives, an operator scans it at return intake
(`return_receive` movement from `RETURN_INTAKE`). Disposition is a task: restock
to a pick-face location, quarantine for inspection, or scrap.

The platform therefore shows, for one shipment, the full loop: dispatched,
rejected, returned, restocked, and the shipping cost lost, in one timeline.

### 9.4 Remittance file format

Synthetic, but shaped like the real thing: a CSV with a header row, one line per
shipment, the courier's own reference, the tracking number, the amount
collected, the fee, and the net. A generator produces files with a configurable
rate of each break type so the matching engine has something to find.

---

## 10. Slotting

Which SKU belongs where. Fast movers near the packing zone, slow movers in bulk,
heavy things low, items ordered together adjacent.

### 10.1 Approach

A Python batch job, run on demand or nightly, that:

1. Reads pick history from the log for a window (default 90 days).
2. Computes velocity per SKU and affinity between SKU pairs (frequency of
   appearing in the same order).
3. Formulates an assignment problem: SKUs to pick-face locations, minimising
   expected route distance for the historical waves, subject to capacity and a
   maximum number of moves per run.
4. Solves it with a constraint solver (`ortools`), with a time limit and a
   fallback to a greedy assignment if the solver times out.
5. Writes a **slotting proposal**: a list of `relocate` moves with the expected
   distance saving.

### 10.2 Evidence

The proposal is not trusted on the solver's word. The platform replays the
historical waves against the current layout and against the proposed layout
using the same router from 6.3 and reports both total distances. The saving
shown to the supervisor is the replayed figure, not the solver's objective.

Accepted proposals become `relocate` tasks. The platform records the actual
route distances for the following weeks so the prediction can be checked
against reality on the platform's own synthetic operators.

`ponytail:` this is a batch job that reads the database and writes a table. It
is not a service. It has no API. The console reads the proposals table.

---

## 11. Interfaces

### 11.1 Floor console

The supervisor's view. A Next.js application, desktop-first, dense.

Screens:

- **Stock explorer.** By SKU, by location, by LPN, by lot. Every quantity
  clickable to the movements that produced it. This is the view that makes the
  event log tangible.
- **Task board.** Open, assigned, in-progress, exception, by kind and operator.
- **Waves.** Release, route preview on the floor map, progress.
- **Receiving.** ASNs expected, in progress, three-way breaks.
- **Reconciliation workbench.** Variances and settlement breaks, ageing
  buckets, classification actions, notes.
- **Settlement.** COD outcomes, remittance uploads, matching results, cases.
- **Delivery.** Endpoints, per-endpoint health, dead letters, replay.
- **Slotting.** Proposals, replayed savings, accept and schedule.
- **Floor map.** A rendered plan of zones and aisles with heat by pick
  frequency. Used by waves, slotting, and the stock explorer.
- **Admin.** Warehouses, zones, locations, users, roles, API tokens.

### 11.2 Scanner interface

The operator's view. A progressive web app in the same Next.js codebase, served
at `/scan`, designed for a phone or a rugged handheld held in one hand.

**Why a PWA and not a native app.** Rugged warehouse handhelds present their
barcode scanner as a keyboard (a "wedge"): a scan types the code and presses
Enter. A web input field receives that with no integration work. Phone cameras
are handled by the browser barcode detection API with a library fallback.
Offline storage, background sync, and installability are all available to a
PWA. One codebase, one deploy, one design system.

`ponytail:` a native app is the upgrade path if hardware integration beyond the
keyboard wedge is needed (scanner SDKs, RFID, ruggedised Bluetooth peripherals).
Nothing in the API would change.

**Offline first.** The task list, the locations, and the SKU catalogue for the
operator's assigned zone are cached locally. Every operator action is a command
written to a local queue (IndexedDB) with an idempotency key, applied optimistically
to the local view, and synced when connectivity returns. The interface always
shows the queue depth and the last sync time. A command rejected on sync (the
unit was taken by someone else) surfaces as an exception task with the reason,
and the local view rolls back.

**Flows:** receive (scan ASN, scan LPN, confirm quantity), putaway (scan LPN,
scan destination, confirm), pick (scan location, scan SKU or LPN, confirm
quantity, next), pack (scan cart LPN, scan order, confirm), ship (scan carton,
scan carrier manifest), count (scan location, scan each LPN, enter quantities),
return intake (scan tracking, scan SKU, mark condition).

Every flow is scan-driven. Typing is the fallback, not the path.

### 11.3 API

REST, described in `api/openapi.yaml` first, handlers generated from it. Every
mutating endpoint takes an `Idempotency-Key` header. Every list endpoint is
cursor-paginated. Errors are RFC 9457 problem details.

TypeScript and Go clients are generated from the spec in CI and published as
packages. The Next.js layer uses the TypeScript client; nothing else may call
the API by hand-written fetch.

---

## 12. Design system

This is industrial software. The design decisions come from the conditions it
runs in: bright overhead lighting with glare, operators wearing gloves, devices
held at arm's length or mounted on a cart, people walking while reading, twelve
hour shifts including nights, and codes that must be compared character by
character while someone is talking.

Everything below is for both the console and the scanner, with the scanner
constraints taking precedence when they conflict.

The exact visual values (tokens, CSS, components, page layouts, icons) are in
[DESIGN.md](DESIGN.md). This section keeps the principles and the summary.

### 12.1 Principles

1. **Contrast over subtlety.** Minimum 7:1 for body text and codes. No light
   grey text on white. No thin weights.
2. **State by shape and colour, never colour alone.** Every status has an icon
   or a fill pattern as well as a hue.
3. **Touch targets of 48 pixels minimum, 56 for primary actions, on the
   scanner.** Gloved thumbs. The console is a desk tool with a mouse and
   keyboard, so its controls are 32 pixels; on a touch screen they grow to 44.
4. **The primary action lives at the bottom of the screen on the scanner.** It
   is reachable by thumb without changing grip.
5. **Confirm with sound and vibration.** A successful scan beeps and pulses. A
   rejected scan buzzes twice. Operators do not look at the screen for every
   scan and should not have to.
6. **Codes are monospace, always.** SKUs, LPNs, location codes, tracking
   numbers, and lot numbers are read aloud and compared. They must be in a face
   where `0` and `O`, `1` and `l` and `I` cannot be confused.
7. **Offline state is always visible.** A persistent indicator shows online or
   offline and the number of unsynced actions. It is never hidden behind a menu.
8. **Dark theme is a first-class theme.** Night shifts and low-light bulk
   areas. Both themes meet the same contrast bar.

### 12.2 Colour

A quiet neutral interface (white content, light grey sidebar, hairline borders,
one blue accent) with colour reserved for status. Full token table with
measured contrast: DESIGN.md section 3.

| Token | Light | Dark | Role |
|---|---|---|---|
| `--bg` | `#FFFFFF` | `#161618` | content, cards, tables |
| `--bg-subtle` | `#FAFAFA` | `#111113` | sidebar, table header, scanner ground |
| `--text` | `#1C1D1F` | `#EDEDEF` | text, codes |
| `--text-secondary` | `#505259` | `#A1A3AA` | secondary text, 7:1 on its grounds |
| `--border` | `#E9E9EC` | `#2A2A2E` | rules, borders |
| `--accent` | `#2563EB` | `#6EA8FF` | primary buttons, links, focus ring |
| `--ok` | `#166534` | `#4ADE80` | confirmed, complete, in stock |
| `--attention` | `#854D0E` | `#FACC15` | pending, partial, ageing 7 to 30 |
| `--stop` | `#991B1B` | `#FCA5A5` | rejected, exception, over 90 days |
| `--hold` | `#5B21B6` | `#C4B5FD` | quarantined, on hold |

Each status colour also has a tint (`--ok-bg` and so on) for the status pill.
`--attention` in dark mode is safety yellow at full strength. This is the one
place the palette is loud, and it is loud for the reason a hazard stripe is loud.

Status also carries a shape: `--ok` a filled circle, `--attention` a filled
triangle, `--stop` a filled octagon, `--hold` a filled diamond, pending an
outline circle. These render in monochrome print and on a scanner with a dim
screen.

### 12.3 Type

Two faces, chosen for legibility rather than character.

- **UI and prose:** Inter, weights 400, 500 and 600, with the character
  variants `cv05` (lower-case l with a tail) and `cv08` (upper-case I with
  serifs) turned on so `l`, `I` and `1` stay distinct at arm's length.
- **Codes and numbers:** JetBrains Mono. Slashed zero, distinct `1` `l` `I`,
  `tabular-nums` by default. Used for every SKU, LPN, location, lot, tracking
  number, quantity, and timestamp.

Scale, in pixels, fixed rather than fluid because the devices are known:

| Role | Console | Scanner |
|---|---|---|
| Code, primary (the thing being scanned) | 13 in tables, 15 in the page bar | 28 |
| Body | 14 | 18 |
| Label | 13, sentence case | 14, sentence case |
| Quantity, primary | 22 | 40 |
| Heading | 15 | 22 |

Nothing on the scanner is below 14 pixels. Labels are never uppercase.

### 12.4 Layout

**Console:** a fixed left sidebar with the warehouse switcher at the top and
grouped sections with monotone icons (no emoji), a slim page bar per page, and a
content area that is dense by design. Tables are the primary component and they
are built for scanning by eye: hairline row and column borders, a type icon in
each header, sticky headers, right-aligned numbers, codes in mono, status as a
shaped pill, and a footer with the row count. Rows expand in place for detail. No modal dialogs for reading; modals are
for confirmations only.

**Scanner:** one task per screen. Top: task kind and the location or LPN in
focus, in the 28-pixel code face. Middle: the scan target with a large
illustration of what to scan next. Bottom: one primary action button, full
width, 56 pixels tall, and a secondary text action beside it for exceptions
("short", "damaged", "skip"). The offline indicator is a persistent bar above
the primary action, not a toast.

### 12.5 Components

A small set, each with a documented state chart:

- **Status chip**: shape + colour + label, sizes for table and for scanner.
- **Code**: mono, copyable, with optional barcode render.
- **Quantity stepper**: scanner only, 56-pixel buttons, direct numeric entry,
  hard maximum from the task.
- **Scan input**: receives wedge input, shows the last scan and its result, sound
  and haptic on result.
- **Ageing bucket**: a horizontal bar with four segments coloured by bucket.
- **Floor map**: SVG, zones and aisles from the location model, heat overlay,
  route overlay, clickable locations.
- **Timeline**: the movement history of one thing, vertical, mono timestamps.
- **Break card**: one reconciliation or settlement break with its three sources
  side by side and the delta highlighted.

### 12.6 Motion and feedback

Almost none. A 120 millisecond fade on panel change. The scan result flashes the
whole screen edge in `--ok` or `--stop` for 200 milliseconds because peripheral
vision catches it while the eyes are on the shelf. `prefers-reduced-motion`
disables the flash and keeps the sound and haptic.

### 12.7 Accessibility

Contrast as stated. Every interactive element reachable by keyboard on the
console, because supervisors use keyboards. Every status announced to screen
readers by label, not by colour. Focus rings in `--action` at 3 pixels. Sound
has a haptic twin and a visual twin so no single channel is required.

---

## 13. Cross-cutting concerns

### 13.1 Authentication and authorisation

Users sign in to the Next.js layer with a managed authentication provider;
sessions are httpOnly cookies. The Next.js layer calls the Go API with a
service token and forwards the user's identity and role in a signed header the
API verifies.

Roles: `operator` (scanner flows for assigned tasks only), `supervisor`
(console, classification, replay, wave release), `admin` (structure, users,
endpoints, tokens). Every API handler declares the role it requires and the
test suite has one test per role per protected endpoint.

External systems authenticate with per-system API tokens, hashed at rest,
scoped to read events or to submit orders and ASNs.

### 13.2 Observability

- **Tracing:** OpenTelemetry from the browser through Next.js, the Go API, the
  database, the outbox relay, the bus, and the delivery worker. One trace shows
  a scan becoming a webhook.
- **Metrics:** Prometheus. Movement rate by kind, reservation contention
  (lock wait time), task throughput by operator, delivery success rate and
  latency by endpoint, dead letter depth, unresolved breaks by bucket, offline
  queue depth reported by scanners on sync.
- **Logs:** `slog` JSON with trace and span IDs. User input and scanned codes
  are logged as fields, never interpolated into the message.
- **Dashboard:** one Grafana board committed as JSON: the metrics above, plus
  the four ageing buckets as the headline row.

### 13.3 Configuration

Environment variables only, documented in `deploy/README.md`, validated at
startup with a clear failure listing every missing or malformed value.

### 13.4 Data and migrations

`golang-migrate`, forward-only, applied at deploy. Every migration is tested by
applying it to a snapshot of the previous schema with data present. The
`movements` table has a database rule rejecting `UPDATE` and `DELETE`.

Synthetic data comes from a generator that produces a warehouse with a plausible
layout, a catalogue with a velocity distribution, and a history of inbound and
outbound activity with configurable rates of every exception the platform
handles. The generator is what makes the demo, the tests, and the load tests
consistent.

### 13.5 Deployment

One virtual machine. Docker Compose with the service binary, PostgreSQL,
Redpanda from Phase 2, the Next.js application, and Caddy for TLS and routing.
A systemd unit brings Compose up on boot. Backups by `pg_dump` on a timer to
object storage, with a monthly restore drill recorded.

A set of **synthetic operators** run as a scheduled job: scripted shifts that
receive, put away, pick, pack, ship, count, and return at realistic rates, so
the deployed platform is always doing something and the dashboards show a
living system.

`ponytail:` no Kubernetes, no managed database, no multi-region. Compose on one
host is the honest scale for this system's real load. The upgrade path is
documented in one paragraph and not built.

---

## 14. Test strategy

### 14.1 Invariants

Section 6.1, all seven, as Go tests that run against a real PostgreSQL in CI.
Conservation and projection fidelity run after every other test suite as a
final gate, so any test that leaves the database inconsistent fails the build.

### 14.2 Concurrency

- Fifty goroutines reserve one available unit. Exactly one succeeds. Under
  `-race`.
- One hundred pickers confirm picks across ten locations while a cycle count
  runs on those locations. Every count classifies as `timing` or agrees with the
  system. No adjustment posts.
- Two scanners sync the same offline command with the same idempotency key.
  One movement.

### 14.3 Property-based

`rapid` on the movement model: random sequences of valid commands never violate
conservation or position identity; random interleavings of reserve, release,
and cancel with retries produce the same final positions as a serial execution.

### 14.4 Replay

For every fixture history, truncate every projection, rebuild, and diff. Then
apply one more random command and assert the incremental projection equals a
fresh rebuild.

### 14.5 Delivery

A fake endpoint server with programmable behaviour: succeed, fail N times then
succeed, time out, return 500 forever, accept out of order. Tests assert
ordering per shipment, backoff timing within tolerance, circuit breaker
transitions, dead-lettering after budget, and replay idempotency on the
receiving side.

### 14.6 Reconciliation and settlement

Fixture files for ASN, count, and remittance scenarios, each with the expected
break classification. The generator's configurable break rates are asserted
to produce the expected distribution.

### 14.7 Chaos

- Kill PostgreSQL mid-reservation. On restart, no partial movement exists and
  the reservation either fully happened or did not.
- Kill the outbox relay mid-batch. Every outbox row is eventually delivered
  exactly once to the projection and at least once to the bus.
- Kill Redpanda during delivery. Deliveries pause and resume with no loss and
  no reordering.
- Scanner offline for an hour with fifty queued commands, then sync. Every
  command is applied or rejected with a reason, in order.

### 14.8 End to end

Playwright against the deployed stack: a full inbound to outbound to return
cycle driven through the console and the scanner PWA (with the wedge simulated
as keyboard input), asserting the stock explorer, the timeline, and the
delivered webhook payloads at each step.

### 14.9 Contract

The OpenAPI document is validated in CI. Every handler is tested against the
schema for its request and response. The generated clients compile against
the current spec on every push.

---

## 15. Load test plan

`k6` scenarios against the deployed stack, results committed to the repository
and rendered in the README.

| Scenario | Shape | Success criteria |
|---|---|---|
| Allocation burst | 1,000 orders submitted in 10 seconds against a catalogue with contention on 50 SKUs | every order allocated or partially allocated within 30 seconds, conservation holds, p99 reservation latency under 250 ms |
| Pick storm | 100 synthetic operators confirming picks at one per second each for 10 minutes | no reservation conflicts surface as errors, p99 confirm latency under 200 ms, projection fidelity holds after |
| Webhook fan-out | 10,000 events to 100 endpoints, 10 of which fail 30 percent of requests | ordering holds per shipment, failing endpoints do not slow healthy ones, dead letter depth matches the expected failure count |
| Offline sync wave | 50 scanners each syncing 100 queued commands at once | all commands processed within 60 seconds, every conflict rejected with a reason |
| Count under load | cycle counts on 20 locations during the pick storm | every count classifies correctly, zero false `shrinkage` |

Every run records the commit, the synthetic data seed, and the host size. Runs
are compared release over release.

---

## 16. Phases

Ordered by dependency and by value per hour. Each phase ends with something
demonstrable. Estimates assume evenings and weekends alongside other
commitments.

### Phase 0: Model, log, and proof

Repository, CI, PostgreSQL, migrations. The domain model as Go types. The
`movements` log and the `positions` projection. The seven invariants as tests,
most failing. The synthetic data generator producing a warehouse and a catalogue.
`crossdock rebuild positions`.

No API, no UI. This phase exists so that the proof precedes the features.

**Exit criteria:** invariants 1, 2, 3, and 6 pass against generated data.
Estimated three weeks.

### Phase 1: Inventory core and the two interfaces

Inbound (ASN, receipt, putaway), outbound (order, allocation, wave, pick, pack,
ship), the task queue, the allocation policy, the router. OpenAPI and generated
clients. Authentication and roles. The floor console: stock explorer, task
board, waves, receiving. The scanner PWA: receive, putaway, pick, pack, ship,
with the offline queue and sync. The design system implemented.

**Exit criteria:** all seven invariants pass. The concurrency tests in 14.2 pass
under the race detector. A full inbound-to-ship cycle runs through the scanner
PWA in a browser with the wedge simulated. Estimated eight weeks. Largest phase.

### Phase 2: Event delivery

Redpanda. `outboxkit` relay to the bus. The delivery worker with signing,
ordering, backoff, circuit breaker, dead letters, replay. Endpoint management in
the console. The delivery tests in 14.5.

**Exit criteria:** the webhook fan-out load scenario passes. One OpenTelemetry
trace spans a scanner confirm to a delivered webhook. Estimated four weeks.

### Phase 3: Counting and reconciliation

Cycle count scheduling by velocity class, the count flow on the scanner,
variance classification, the three-way inbound reconciliation, the workbench,
ageing buckets.

**Exit criteria:** the count-under-load scenario passes with zero false
shrinkage. Fixture scenarios classify as expected. Estimated four weeks.

### Phase 4: Returns and settlement

COD on shipments, delivery outcomes, return authorisation, return intake on the
scanner, disposition, the remittance generator, ingest, matching, breaks, cases.
The settlement screens.

**Exit criteria:** the full COD loop for one shipment shows in one timeline:
dispatched, rejected, returned, restocked, cost recorded. Every break type in
9.2 is produced by the generator and caught by the matcher. Estimated four weeks.

### Phase 5: Slotting

The Python job, the solver, the replayed evidence, proposals in the console,
accepted proposals as relocate tasks, the floor map heat overlay.

**Exit criteria:** on generated history, a proposal's replayed saving is
positive and the figure shown is the replayed one, not the solver's. Estimated
three weeks.

### Phase 6: Deploy for real, load, write-up

The single-host deployment behind TLS. Synthetic operators on a schedule.
Backups and a restore drill. The full observability stack and the Grafana board.
The load test plan run and committed. One long-form write-up on the hardest
problem encountered. Candidates: timing classification of counts under
concurrent picks, offline conflict resolution, or the conservation invariant as
a testing tool.

**Exit criteria:** a public URL with the console and scanner live, dashboards
showing synthetic operators working, load results in the README, the write-up
published. Estimated three weeks.

### Phase 7: Security and hardening

See Section 17. Last phase by rule. Estimated two weeks.

### If time runs short

Phases 0 and 1 finished, deployed, and documented beat everything else
half-built. Phase 1 alone, with the invariants and the concurrency tests
passing and a scanner PWA that works offline, is a complete and credible
system. Phase 2 is the next most valuable because it makes the platform useful
to something other than itself. Phases 3 and 4 are where the domain depth
lives. Phase 5 is the most optional.

---

## 17. Security and hardening

**Tier: T2, Internal/Team.** Crossdock is a hosted application with
authenticated users in three roles, an external API, and a mobile interface. It
holds operational data with no regulated personal data and moves no real money.
T3 would apply if it ever held customer addresses for real shipments or real
COD amounts; re-check the tier if the deployment ever stops being synthetic.

**Scaling: none.** One operator organisation, a known small set of users, and
synthetic load. No scaling rows apply. The load plan in Section 15 is for
correctness under contention, not for capacity planning.

The T1 ground rules are enforced from Phase 0, not deferred: secrets out of git
from the first commit, parameterised queries only, authentication and
authorisation enforced in the API and never only in the browser. This phase is
the audit that proves they held, plus the T2 rows.

### Secrets and keys

- Every secret lives in the deployment environment, never in the repository or
  the browser bundle. The Next.js layer's service token is server-side only.
- Webhook endpoint secrets are shown once at creation, stored hashed for
  comparison and encrypted at rest for signing, and rotated with an overlap
  window.
- API tokens for external systems are hashed at rest and scoped.
- Rotate the database password, the service token, and the object storage
  credentials on a schedule and immediately on any suspected exposure or
  offboarding.

### Database and data access

- PostgreSQL is reachable only from the Compose network. It has no published
  port on the host.
- Row-level authorisation is enforced in the API layer per warehouse and per
  role. An operator can only read and complete tasks assigned to them. This is
  tested per role per endpoint.
- All queries go through parameterised statements. The codebase has a lint rule
  rejecting string-built SQL.
- Backups run on a timer to object storage. A restore is performed monthly and
  the date and duration recorded in the repository.

### Authentication and access control

- Authentication is delegated to a managed provider. Sessions are httpOnly,
  secure, same-site cookies. Nothing session-related touches `localStorage`.
- Every API handler declares a required role. A handler with no declaration
  fails to compile. The admin screens, the replay action, and the classification
  action get the same check as everything else and are tested by hand as well.
- Create and update endpoints accept only the fields the role may set. A
  `role` or `warehouse_id` field on a self-service update is rejected.
- Session invalidation on password change and on account deactivation takes
  effect on the next request.

### Rate limiting and abuse

- Rate limits on the API per token and per user, tighter on sign-in and on the
  external order and ASN submission endpoints.
- Billing caps and usage alerts on the host, object storage, and the
  authentication provider.

### Input and output handling

- Every scanned code and every uploaded file is validated server-side for
  type, length, and format before anything is written.
- Remittance uploads are restricted to CSV, size-capped, parsed with a
  streaming parser that rejects malformed rows with a line number, and stored
  in a private bucket. They are never served back as executable content.
- Scanned codes and user notes are logged as structured fields and rendered by
  the framework's default escaping. No raw HTML rendering anywhere in the
  console.
- API responses are shaped per endpoint. No handler returns a database row
  verbatim.

### Mobile interface

- The scanner PWA holds no API token. It talks to the Next.js layer with the
  session cookie.
- Offline queue contents are operational commands, not credentials. The queue
  is cleared on sign-out.
- Every offline command is re-validated and re-authorised on sync as if it
  were a fresh request.

### Dependencies and supply chain

- Dependency scanning for Go, Node, and Python in CI on every push.
- Lockfiles committed for all three ecosystems. Upgrades arrive by reviewed pull
  request.
- Before adding any package an AI assistant suggested, confirm it exists on the
  registry, check downloads and repository, and confirm the name is not a
  lookalike.

### Caching

- Role and permission data is never cached across requests. The signed identity
  header is verified on every API call.
- Any projection cache is keyed by warehouse and, where relevant, by user.

### Deployment and operations

- Caddy forces HTTPS and sets `Strict-Transport-Security`,
  `Content-Security-Policy`, `X-Content-Type-Options`, `Referrer-Policy`, and
  `frame-ancestors`.
- Production builds disable debug output and do not serve source maps or the
  `.git` directory.
- Users see a generic error and a trace ID. Full detail goes to logs. Secrets,
  tokens, and session values are redacted by the log handler.
- Two-factor authentication on the host provider, the domain registrar, the
  object storage account, the authentication provider, and every admin user in
  the platform.

---

## 18. Documentation plan

The README is problem-first. Its order:

1. One paragraph: the number in the system must equal the number on the shelf,
   and the four ways that fails.
2. A screenshot of the stock explorer showing one quantity expanded into its
   movements, and one of the scanner mid-pick.
3. The design commitments, verbatim from Section 2.
4. Run it: `make up`, the URLs, the synthetic operator toggle.
5. The load results table and the link to the live deployment.
6. How it works: one paragraph each on the log and projections, reservation
   under lock, delivery, counting, settlement, slotting, each linking to the
   docs.
7. Honest framing, verbatim from Section 2.
8. Contributing, licence, security.

`docs/` (mdBook) carries: the domain glossary from Section 3 in full, the
movement catalogue, the event catalogue and webhook signing guide with a
verification snippet in three languages, the OpenAPI reference, the design
system with rendered components, the deployment runbook, the restore drill
procedure, and the write-up from Phase 6.

Every API example in the docs is executed in CI against the running service
and its output compared, so the docs cannot drift from the system.
