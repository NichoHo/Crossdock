-- Migration 006: Phase 4 returns and cash-on-delivery settlement (spec 9).
-- Money is stored in minor units (satang, cents) as BIGINT. One currency per
-- deployment.

-- Disposition is a task (spec 9.3).
ALTER TABLE tasks DROP CONSTRAINT tasks_kind_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_kind_check CHECK (kind IN
    ('putaway','pick','pack','count','return_intake','relocate','disposition'));

CREATE INDEX shipments_tracking_idx ON shipments (warehouse_id, carrier, tracking);

-- What was agreed with each courier.
CREATE TABLE carrier_terms (
    warehouse_id        TEXT   NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    carrier             TEXT   NOT NULL,
    -- COD fee the courier deducts: bps of the collected amount plus a flat part.
    cod_fee_bps         INT    NOT NULL DEFAULT 0 CHECK (cod_fee_bps >= 0 AND cod_fee_bps <= 10000),
    cod_fee_flat_minor  BIGINT NOT NULL DEFAULT 0 CHECK (cod_fee_flat_minor >= 0),
    -- What one shipment costs to send; lost when the parcel comes back rejected.
    shipping_cost_minor BIGINT NOT NULL DEFAULT 0 CHECK (shipping_cost_minor >= 0),
    -- Days after a delivered_collected outcome by which the courier should have remitted.
    settlement_days     INT    NOT NULL DEFAULT 7 CHECK (settlement_days >= 0),
    PRIMARY KEY (warehouse_id, carrier)
);

-- The courier's report on a shipment (spec 9.1). Append-only; a shipment can
-- have more than one (a courier may report rejected, then be paid anyway).
CREATE TABLE delivery_outcomes (
    id           TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL,
    shipment_id  TEXT        NOT NULL,
    outcome      TEXT        NOT NULL CHECK (outcome IN ('delivered_collected','rejected','lost')),
    reported_at  TIMESTAMPTZ NOT NULL,
    source       TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, shipment_id, outcome),
    FOREIGN KEY (warehouse_id, shipment_id) REFERENCES shipments(warehouse_id, id) ON DELETE RESTRICT
);

-- Money movements around a shipment, append-only, signed from the operator's
-- side (positive = the operator is owed or has received value).
CREATE TABLE ledger (
    id           TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL,
    shipment_id  TEXT        NOT NULL,
    kind         TEXT        NOT NULL CHECK (kind IN
                    ('cod_receivable','shipping_cost_lost','claim_owed','remitted','fee_deducted','writeoff','recovered')),
    amount_minor BIGINT      NOT NULL,
    ref          TEXT        NOT NULL DEFAULT '',
    note         TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id),
    -- A retried outcome or a re-ingested file never books the same entry twice.
    UNIQUE (warehouse_id, shipment_id, kind, ref),
    FOREIGN KEY (warehouse_id, shipment_id) REFERENCES shipments(warehouse_id, id) ON DELETE RESTRICT
);

-- Reverse logistics (spec 9.3).
CREATE TABLE return_authorisations (
    id           TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL,
    shipment_id  TEXT        NOT NULL,
    order_id     TEXT        NOT NULL,
    reason       TEXT        NOT NULL,
    state        TEXT        NOT NULL DEFAULT 'authorised' CHECK (state IN ('authorised','received','dispositioned','cancelled')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    received_at  TIMESTAMPTZ,
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, shipment_id),
    FOREIGN KEY (warehouse_id, shipment_id) REFERENCES shipments(warehouse_id, id) ON DELETE RESTRICT
);

CREATE TABLE return_lines (
    id                  TEXT        NOT NULL,
    warehouse_id        TEXT        NOT NULL,
    ra_id               TEXT        NOT NULL,
    sku_id              TEXT        NOT NULL,
    lot_id              TEXT        NOT NULL,
    qty                 BIGINT      NOT NULL CHECK (qty > 0),
    condition           TEXT        NOT NULL CHECK (condition IN ('good','damaged','unknown')),
    -- Where the units sit until someone decides (the returns zone).
    location            TEXT        NOT NULL,
    state               TEXT        NOT NULL DEFAULT 'awaiting_disposition' CHECK (state IN ('awaiting_disposition','dispositioned')),
    decision            TEXT        NOT NULL DEFAULT '' CHECK (decision IN ('','restock','quarantine','scrap')),
    destination         TEXT        NOT NULL DEFAULT '',
    intake_key          TEXT        NOT NULL,
    received_by         TEXT        NOT NULL DEFAULT '',
    received_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispositioned_at    TIMESTAMPTZ,
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, intake_key, sku_id, lot_id, condition),
    FOREIGN KEY (warehouse_id, ra_id) REFERENCES return_authorisations(warehouse_id, id) ON DELETE RESTRICT
);

-- Courier remittance files and their lines (spec 9.4).
CREATE TABLE remittances (
    id            TEXT        NOT NULL,
    warehouse_id  TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    carrier       TEXT        NOT NULL,
    filename      TEXT        NOT NULL DEFAULT '',
    sha256        TEXT        NOT NULL,
    received_by   TEXT        NOT NULL DEFAULT '',
    received_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    line_count    INT         NOT NULL DEFAULT 0,
    matched_count INT         NOT NULL DEFAULT 0,
    break_count   INT         NOT NULL DEFAULT 0,
    PRIMARY KEY (warehouse_id, id),
    -- Uploading the same file twice is a no-op.
    UNIQUE (warehouse_id, sha256)
);

CREATE TABLE remittance_lines (
    id              TEXT   NOT NULL,
    warehouse_id    TEXT   NOT NULL,
    remittance_id   TEXT   NOT NULL,
    line_no         INT    NOT NULL,
    courier_ref     TEXT   NOT NULL DEFAULT '',
    tracking        TEXT   NOT NULL,
    collected_minor BIGINT NOT NULL,
    fee_minor       BIGINT NOT NULL,
    net_minor       BIGINT NOT NULL,
    shipment_id     TEXT   NOT NULL DEFAULT '',
    state           TEXT   NOT NULL DEFAULT 'matched' CHECK (state IN ('matched','break')),
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, remittance_id, line_no),
    FOREIGN KEY (warehouse_id, remittance_id) REFERENCES remittances(warehouse_id, id) ON DELETE CASCADE
);
CREATE INDEX remittance_lines_shipment_idx ON remittance_lines (warehouse_id, shipment_id) WHERE shipment_id <> '';

-- A settlement break is a case (spec 9.2).
CREATE TABLE settlement_breaks (
    id                 TEXT        NOT NULL,
    warehouse_id       TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    kind               TEXT        NOT NULL CHECK (kind IN
                          ('short_remitted','unremitted','remitted_for_rejected','unknown_shipment','duplicate_remittance')),
    shipment_id        TEXT        NOT NULL DEFAULT '',
    tracking           TEXT        NOT NULL DEFAULT '',
    remittance_line_id TEXT        NOT NULL DEFAULT '',
    outcome_id         TEXT        NOT NULL DEFAULT '',
    expected_minor     BIGINT      NOT NULL DEFAULT 0,
    actual_minor       BIGINT      NOT NULL DEFAULT 0,
    -- expected - actual: positive means the operator is short.
    delta_minor        BIGINT      NOT NULL DEFAULT 0,
    detail             TEXT        NOT NULL DEFAULT '',
    state              TEXT        NOT NULL DEFAULT 'open' CHECK (state IN ('open','resolved','cleared')),
    resolution         TEXT        NOT NULL DEFAULT '' CHECK (resolution IN ('','recovered','written_off','no_error')),
    -- Money written off needs a classified reason (spec 9.2).
    writeoff_reason    TEXT        NOT NULL DEFAULT '' CHECK (writeoff_reason IN
                          ('','courier_fee_dispute','customer_short_paid','lost_in_transit','data_error','goodwill')),
    note               TEXT        NOT NULL DEFAULT '',
    resolved_by        TEXT        NOT NULL DEFAULT '',
    opened_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at          TIMESTAMPTZ,
    PRIMARY KEY (warehouse_id, id),
    UNIQUE (warehouse_id, kind, shipment_id, remittance_line_id),
    CHECK (resolution <> 'written_off' OR writeoff_reason <> '')
);
CREATE INDEX settlement_breaks_open_idx ON settlement_breaks (warehouse_id, state, opened_at);
