-- Migration 003: Phase 1 workflows. Tasks, inbound, outbound, outbox, API
-- tokens and HTTP idempotency.

-- ---------------------------------------------------------------------------
-- Tasks: everything an operator does (spec 3.8).
-- ---------------------------------------------------------------------------
CREATE TABLE tasks (
    id               TEXT        NOT NULL,
    warehouse_id     TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    kind             TEXT        NOT NULL CHECK (kind IN
                        ('putaway','pick','pack','count','return_intake','relocate')),
    state            TEXT        NOT NULL DEFAULT 'open' CHECK (state IN
                        ('open','assigned','in_progress','completed','cancelled','exception')),
    assigned_to      TEXT        NOT NULL DEFAULT '',
    sku_id           TEXT        NOT NULL DEFAULT '',
    lot_id           TEXT        NOT NULL DEFAULT '',
    lpn_id           TEXT        NOT NULL DEFAULT '',
    from_location    TEXT        NOT NULL DEFAULT '',
    to_location      TEXT        NOT NULL DEFAULT '',
    qty              BIGINT      NOT NULL DEFAULT 0 CHECK (qty >= 0),
    order_id         TEXT        NOT NULL DEFAULT '',
    wave_id          TEXT        NOT NULL DEFAULT '',
    receipt_id       TEXT        NOT NULL DEFAULT '',
    -- ref de-duplicates task creation (one task per allocation, per receipt).
    ref              TEXT        NOT NULL DEFAULT '',
    -- route_seq orders pick tasks along the wave route.
    route_seq        INT         NOT NULL DEFAULT 0,
    exception_reason TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at     TIMESTAMPTZ,
    PRIMARY KEY (warehouse_id, id)
);
CREATE UNIQUE INDEX tasks_ref_idx ON tasks (warehouse_id, kind, ref) WHERE ref <> '';
CREATE INDEX tasks_queue_idx ON tasks (warehouse_id, state, kind, route_seq, created_at);
CREATE INDEX tasks_assignee_idx ON tasks (warehouse_id, assigned_to) WHERE assigned_to <> '';

-- ---------------------------------------------------------------------------
-- Inbound: ASN, ASN lines, receipts.
-- ---------------------------------------------------------------------------
CREATE TABLE asns (
    id           TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    ref          TEXT        NOT NULL,
    supplier     TEXT        NOT NULL DEFAULT '',
    state        TEXT        NOT NULL DEFAULT 'open' CHECK (state IN ('open','received')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, ref)
);

CREATE TABLE asn_lines (
    id           TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL,
    asn_id       TEXT        NOT NULL,
    sku_id       TEXT        NOT NULL,
    lot_id       TEXT        NOT NULL DEFAULT '',
    expected_qty BIGINT      NOT NULL CHECK (expected_qty > 0),
    received_qty BIGINT      NOT NULL DEFAULT 0 CHECK (received_qty >= 0),
    short        BOOLEAN     NOT NULL DEFAULT FALSE,
    PRIMARY KEY (warehouse_id, id),
    FOREIGN KEY (warehouse_id, asn_id) REFERENCES asns(warehouse_id, id) ON DELETE CASCADE,
    FOREIGN KEY (warehouse_id, sku_id) REFERENCES skus(warehouse_id, id) ON DELETE RESTRICT
);
CREATE INDEX asn_lines_asn_idx ON asn_lines (warehouse_id, asn_id);

CREATE TABLE receipts (
    id              TEXT        NOT NULL,
    warehouse_id    TEXT        NOT NULL,
    asn_id          TEXT        NOT NULL,
    asn_line_id     TEXT        NOT NULL,
    sku_id          TEXT        NOT NULL,
    lot_id          TEXT        NOT NULL,
    qty             BIGINT      NOT NULL CHECK (qty > 0),
    lpn_id          TEXT        NOT NULL DEFAULT '',
    location        TEXT        NOT NULL,
    idempotency_key TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, idempotency_key),
    FOREIGN KEY (warehouse_id, asn_line_id) REFERENCES asn_lines(warehouse_id, id) ON DELETE RESTRICT
);

-- ---------------------------------------------------------------------------
-- Outbound: orders, allocations, waves, shipments.
-- ---------------------------------------------------------------------------
CREATE TABLE orders (
    id           TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    ref          TEXT        NOT NULL,
    state        TEXT        NOT NULL DEFAULT 'new' CHECK (state IN (
                    'new','allocated','partially_allocated','unallocatable',
                    'waved','packed','shipped','cancelled')),
    wave_id      TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, ref)
);
CREATE INDEX orders_state_idx ON orders (warehouse_id, state);

CREATE TABLE order_lines (
    id            TEXT   NOT NULL,
    warehouse_id  TEXT   NOT NULL,
    order_id      TEXT   NOT NULL,
    sku_id        TEXT   NOT NULL,
    qty           BIGINT NOT NULL CHECK (qty > 0),
    allocated_qty BIGINT NOT NULL DEFAULT 0 CHECK (allocated_qty >= 0 AND allocated_qty <= qty),
    PRIMARY KEY (warehouse_id, id),
    FOREIGN KEY (warehouse_id, order_id) REFERENCES orders(warehouse_id, id) ON DELETE CASCADE,
    FOREIGN KEY (warehouse_id, sku_id)   REFERENCES skus(warehouse_id, id)   ON DELETE RESTRICT
);
CREATE INDEX order_lines_order_idx ON order_lines (warehouse_id, order_id);

CREATE TABLE allocations (
    id            TEXT        NOT NULL,
    warehouse_id  TEXT        NOT NULL,
    order_id      TEXT        NOT NULL,
    order_line_id TEXT        NOT NULL,
    sku_id        TEXT        NOT NULL,
    lot_id        TEXT        NOT NULL,
    location      TEXT        NOT NULL,
    lpn_id        TEXT        NOT NULL DEFAULT '',
    qty           BIGINT      NOT NULL CHECK (qty > 0),
    -- reserved -> picked -> packed -> shipped, or reserved -> released.
    state         TEXT        NOT NULL DEFAULT 'reserved' CHECK (state IN
                     ('reserved','picked','packed','shipped','released')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id),
    FOREIGN KEY (warehouse_id, order_id) REFERENCES orders(warehouse_id, id) ON DELETE CASCADE
);
CREATE INDEX allocations_order_idx ON allocations (warehouse_id, order_id);

CREATE TABLE waves (
    id             TEXT        NOT NULL,
    warehouse_id   TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    state          TEXT        NOT NULL DEFAULT 'released' CHECK (state IN ('released')),
    route_distance DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id)
);

CREATE TABLE shipments (
    id               TEXT        NOT NULL,
    warehouse_id     TEXT        NOT NULL,
    order_id         TEXT        NOT NULL,
    carrier          TEXT        NOT NULL,
    tracking         TEXT        NOT NULL,
    -- cod_amount_minor is NULL for prepaid shipments; minor units (cents).
    cod_amount_minor BIGINT CHECK (cod_amount_minor IS NULL OR cod_amount_minor >= 0),
    dispatched_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, order_id),
    FOREIGN KEY (warehouse_id, order_id) REFERENCES orders(warehouse_id, id) ON DELETE RESTRICT
);

-- ---------------------------------------------------------------------------
-- Transactional outbox (spec 5.2). Relayed in-process in Phase 1.
-- ---------------------------------------------------------------------------
CREATE TABLE outbox (
    id           BIGSERIAL   PRIMARY KEY,
    warehouse_id TEXT        NOT NULL,
    topic        TEXT        NOT NULL,
    key          TEXT        NOT NULL DEFAULT '',
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    relayed_at   TIMESTAMPTZ
);
CREATE INDEX outbox_pending_idx ON outbox (id) WHERE relayed_at IS NULL;

-- ---------------------------------------------------------------------------
-- External-system API tokens (hashed at rest) and HTTP idempotency records.
-- ---------------------------------------------------------------------------
CREATE TABLE api_tokens (
    id         TEXT        PRIMARY KEY,
    name       TEXT        NOT NULL,
    token_hash TEXT        NOT NULL UNIQUE,   -- hex sha256
    scopes     TEXT[]      NOT NULL,          -- 'orders:write','asn:write','events:read'
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE TABLE idempotency_keys (
    key         TEXT        NOT NULL,
    scope       TEXT        NOT NULL,          -- caller identity
    request_sig TEXT        NOT NULL,          -- method + path + body hash
    status      INT         NOT NULL,
    response    BYTEA       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (scope, key)
);
