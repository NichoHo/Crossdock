-- Migration 005: Phase 3 counting and reconciliation (spec 8).

-- Which transaction wrote each movement. A count asks "what had been committed
-- when I started, what committed while I counted", and sequence numbers cannot
-- answer that: a transaction takes its seq before it commits, so seq order is
-- not commit order. Snapshots can. (Existing rows get the migration's xid, which
-- is before any count exists.)
ALTER TABLE movements ADD COLUMN xid xid8 NOT NULL DEFAULT pg_current_xact_id();

-- Why a task exists (scheduled count, exception count, recount).
ALTER TABLE tasks ADD COLUMN reason TEXT NOT NULL DEFAULT '';

-- One row per count of one location (spec 8.1).
CREATE TABLE counts (
    id             TEXT        NOT NULL,
    warehouse_id   TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    task_id        TEXT        NOT NULL,
    location       TEXT        NOT NULL,
    reason         TEXT        NOT NULL CHECK (reason IN ('schedule','exception','recount')),
    -- The variance this count re-checks, for a recount.
    recount_of     TEXT        NOT NULL DEFAULT '',
    state          TEXT        NOT NULL DEFAULT 'counting' CHECK (state IN ('counting','counted')),
    operator       TEXT        NOT NULL DEFAULT '',
    -- Snapshots bound the window of "in flight" movements.
    start_snapshot TEXT        NOT NULL,
    start_seq      BIGINT      NOT NULL,
    end_snapshot   TEXT,
    end_seq        BIGINT,
    started_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at   TIMESTAMPTZ,
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, task_id)
);
CREATE INDEX counts_location_idx ON counts (warehouse_id, location, completed_at);

-- What the operator reported, per (sku, lot, lpn).
CREATE TABLE count_lines (
    warehouse_id TEXT   NOT NULL,
    count_id     TEXT   NOT NULL,
    sku_id       TEXT   NOT NULL,
    lot_id       TEXT   NOT NULL,
    lpn_id       TEXT   NOT NULL DEFAULT '',
    counted_qty  BIGINT NOT NULL CHECK (counted_qty >= 0),
    damaged_qty  BIGINT NOT NULL DEFAULT 0 CHECK (damaged_qty >= 0 AND damaged_qty <= counted_qty),
    PRIMARY KEY (warehouse_id, count_id, sku_id, lot_id, lpn_id),
    FOREIGN KEY (warehouse_id, count_id) REFERENCES counts(warehouse_id, id) ON DELETE CASCADE
);

-- A difference that needs a decision (spec 8.2).
CREATE TABLE variances (
    id                     TEXT        NOT NULL,
    warehouse_id           TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    count_id               TEXT        NOT NULL,
    location               TEXT        NOT NULL,
    sku_id                 TEXT        NOT NULL,
    lot_id                 TEXT        NOT NULL,
    lpn_id                 TEXT        NOT NULL DEFAULT '',
    system_qty             BIGINT      NOT NULL,
    counted_qty            BIGINT      NOT NULL,
    damaged_qty            BIGINT      NOT NULL DEFAULT 0,
    -- system - counted. Positive: stock is missing. Negative: extra stock was found.
    variance               BIGINT      NOT NULL,
    -- Movements that committed while the count ran, with their effect on the shelf.
    in_flight              JSONB       NOT NULL DEFAULT '[]',
    -- What the rules propose; classification is what a supervisor confirmed.
    proposed               TEXT        NOT NULL DEFAULT '' CHECK (proposed IN
                              ('','timing','miscount','damaged','shrinkage','found','unresolved')),
    classification         TEXT        NOT NULL DEFAULT '' CHECK (classification IN
                              ('','timing','miscount','damaged','shrinkage','found','unresolved')),
    state                  TEXT        NOT NULL DEFAULT 'open' CHECK (state IN ('awaiting_recount','open','closed')),
    -- Shrinkage is flagged for review (spec 8.2).
    flagged                BOOLEAN     NOT NULL DEFAULT FALSE,
    recount_task_id        TEXT        NOT NULL DEFAULT '',
    -- The count that produced the final numbers (the recount, when there was one).
    resolved_count_id      TEXT        NOT NULL DEFAULT '',
    opened_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    first_counted_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at              TIMESTAMPTZ,
    confirmed_by           TEXT        NOT NULL DEFAULT '',
    note                   TEXT        NOT NULL DEFAULT '',
    adjustment_movement_id TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (warehouse_id, id),
    FOREIGN KEY (warehouse_id, count_id) REFERENCES counts(warehouse_id, id) ON DELETE RESTRICT,
    -- An adjustment cannot post without a confirmed classification (spec 8.2).
    CHECK (adjustment_movement_id = '' OR classification IN ('shrinkage','found','damaged'))
);
CREATE INDEX variances_open_idx ON variances (warehouse_id, state, opened_at);

-- Three-way inbound reconciliation breaks (spec 8.3).
CREATE TABLE recon_breaks (
    id           TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    kind         TEXT        NOT NULL CHECK (kind IN ('short_shipped','over_shipped','putaway_incomplete','lot_mismatch')),
    asn_id       TEXT        NOT NULL,
    asn_ref      TEXT        NOT NULL,
    supplier     TEXT        NOT NULL DEFAULT '',
    asn_line_id  TEXT        NOT NULL,
    sku_id       TEXT        NOT NULL,
    expected_qty BIGINT      NOT NULL,
    received_qty BIGINT      NOT NULL,
    putaway_qty  BIGINT      NOT NULL,
    detail       TEXT        NOT NULL DEFAULT '',
    -- open: needs attention. cleared: the condition went away by itself (a late putaway).
    -- waived: a supervisor accepted it, with a note.
    state        TEXT        NOT NULL DEFAULT 'open' CHECK (state IN ('open','cleared','waived')),
    opened_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at    TIMESTAMPTZ,
    note         TEXT        NOT NULL DEFAULT '',
    closed_by    TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, asn_line_id, kind)
);
CREATE INDEX recon_breaks_open_idx ON recon_breaks (warehouse_id, state, opened_at);
