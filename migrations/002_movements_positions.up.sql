-- Migration 002: Movements log and positions projection.
--
-- The movements table is the append-only event log and source of truth for
-- all stock. Positions is the current-state projection, updated in the same
-- transaction as each movement.
--
-- Invariants enforced here (others are enforced in application code + tests):
--   - Movements cannot be updated or deleted (rule + trigger).
--   - qty > 0 enforced by CHECK.
--   - Idempotency key unique per warehouse.

-- ---------------------------------------------------------------------------
-- Per-warehouse sequence for monotonic seq numbers on movements.
-- We use a SEQUENCE for each warehouse created dynamically; for the migration
-- we create one shared sequence that is used as a fallback for non-partitioned
-- setups. Production code uses the warehouse-specific sequence.
-- ---------------------------------------------------------------------------
CREATE SEQUENCE IF NOT EXISTS movements_seq_global;

-- ---------------------------------------------------------------------------
-- Movements — the event log
-- ---------------------------------------------------------------------------
CREATE TABLE movements (
    id              TEXT        NOT NULL DEFAULT gen_random_uuid()::TEXT,
    warehouse_id    TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    -- seq: monotonically increasing per warehouse, assigned by the DB.
    -- In this single-warehouse-sequence approach, seq is global but we also
    -- carry warehouse_id for multi-warehouse correctness.
    seq             BIGINT      NOT NULL DEFAULT nextval('movements_seq_global'),
    kind            TEXT        NOT NULL CHECK (kind IN (
                                    'receive','putaway','reserve','release',
                                    'pick','pack','ship','count_adjust',
                                    'quarantine','return_receive','disposition','relocate'
                                )),
    from_location   TEXT        NOT NULL,
    from_state      TEXT        NOT NULL CHECK (from_state IN (
                                    'available','reserved','in_transit','quarantined','damaged'
                                )),
    to_location     TEXT        NOT NULL,
    to_state        TEXT        NOT NULL CHECK (to_state IN (
                                    'available','reserved','in_transit','quarantined','damaged'
                                )),
    sku_id          TEXT        NOT NULL,
    lot_id          TEXT        NOT NULL,
    lpn_id          TEXT        NOT NULL DEFAULT '',  -- empty = not LPN-tracked
    qty             BIGINT      NOT NULL CHECK (qty > 0),
    occurred_at     TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idempotency_key TEXT        NOT NULL DEFAULT '',
    -- Optional references to domain documents.
    task_id         TEXT        NOT NULL DEFAULT '',
    receipt_id      TEXT        NOT NULL DEFAULT '',
    order_id        TEXT        NOT NULL DEFAULT '',
    count_id        TEXT        NOT NULL DEFAULT '',
    return_id       TEXT        NOT NULL DEFAULT '',
    shipment_id     TEXT        NOT NULL DEFAULT '',

    PRIMARY KEY (warehouse_id, id),
    -- seq uniqueness: (warehouse_id, seq) ensures per-warehouse monotonicity.
    UNIQUE (warehouse_id, seq),
    -- Idempotency: non-empty keys must be unique per warehouse.
    -- We use a partial unique index rather than a check constraint so that
    -- the empty-string default does not cause conflicts.
    FOREIGN KEY (warehouse_id, sku_id)  REFERENCES skus(warehouse_id, id)  ON DELETE RESTRICT,
    FOREIGN KEY (warehouse_id, lot_id)  REFERENCES lots(warehouse_id, id)  ON DELETE RESTRICT
);

-- Partial unique index: enforce idempotency key uniqueness only when the key
-- is provided (non-empty).
CREATE UNIQUE INDEX movements_idempotency_idx
    ON movements (warehouse_id, idempotency_key)
    WHERE idempotency_key <> '';

-- Covering index for projection rebuilds: replay by warehouse in seq order.
CREATE INDEX movements_replay_idx ON movements (warehouse_id, seq ASC);

-- Index: look up movements by order (for event delivery ordering).
CREATE INDEX movements_order_idx ON movements (warehouse_id, order_id) WHERE order_id <> '';
CREATE INDEX movements_shipment_idx ON movements (warehouse_id, shipment_id) WHERE shipment_id <> '';

-- ---------------------------------------------------------------------------
-- Immutability rule for movements
--
-- A database-level trigger that REJECTs any UPDATE or DELETE on the movements
-- table. This implements Invariant 6: movement immutability.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION movements_immutability_guard()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'movements are immutable: UPDATE and DELETE are not permitted (invariant 6). '
                    'Corrections must be posted as new movements in the opposite direction.';
END;
$$;

CREATE TRIGGER movements_no_update
    BEFORE UPDATE ON movements
    FOR EACH ROW EXECUTE FUNCTION movements_immutability_guard();

CREATE TRIGGER movements_no_delete
    BEFORE DELETE ON movements
    FOR EACH ROW EXECUTE FUNCTION movements_immutability_guard();

-- ---------------------------------------------------------------------------
-- Positions — the current-state projection
--
-- Updated in the same transaction as the movement that changes it via
-- application-level upsert (INSERT … ON CONFLICT DO UPDATE).
-- ---------------------------------------------------------------------------
CREATE TABLE positions (
    warehouse_id    TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    sku_id          TEXT        NOT NULL,
    lot_id          TEXT        NOT NULL,
    location        TEXT        NOT NULL,
    lpn_id          TEXT        NOT NULL DEFAULT '',
    available       BIGINT      NOT NULL DEFAULT 0,
    reserved        BIGINT      NOT NULL DEFAULT 0,
    in_transit      BIGINT      NOT NULL DEFAULT 0,
    quarantined     BIGINT      NOT NULL DEFAULT 0,
    damaged         BIGINT      NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (warehouse_id, sku_id, lot_id, location, lpn_id),
    FOREIGN KEY (warehouse_id, sku_id)  REFERENCES skus(warehouse_id, id)  ON DELETE RESTRICT,
    FOREIGN KEY (warehouse_id, lot_id)  REFERENCES lots(warehouse_id, id)  ON DELETE RESTRICT,

    -- Invariant 2 (position identity): for real locations, no state quantity
    -- may be negative. Virtual locations (listed by code prefix convention)
    -- may hold negative available (representing "owed" to the outside world).
    -- This constraint is enforced in application code for virtual locations
    -- and by the trigger below for real ones.
    CONSTRAINT positions_non_negative_available   CHECK (location IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE') OR available   >= 0),
    CONSTRAINT positions_non_negative_reserved    CHECK (location IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE') OR reserved    >= 0),
    CONSTRAINT positions_non_negative_in_transit  CHECK (location IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE') OR in_transit  >= 0),
    CONSTRAINT positions_non_negative_quarantined CHECK (location IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE') OR quarantined >= 0),
    CONSTRAINT positions_non_negative_damaged     CHECK (location IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE') OR damaged     >= 0)
);

-- We relax non-negative for virtual locations at the DB level because
-- PostgreSQL CHECK constraints cannot call external functions safely.
-- The application enforces this; the invariant test enforces it for
-- real locations only.

-- Index: stock-by-SKU query (for the stock explorer).
CREATE INDEX positions_sku_idx ON positions (warehouse_id, sku_id);
-- Index: stock-at-location query (for putaway/allocation/counting).
CREATE INDEX positions_location_idx ON positions (warehouse_id, location);
-- Index: stock-by-LPN query.
CREATE INDEX positions_lpn_idx ON positions (warehouse_id, lpn_id) WHERE lpn_id <> '';
