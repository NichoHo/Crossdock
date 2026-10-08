-- Migration 001: Core schema — warehouses, zones, locations, SKUs, lots, LPNs
-- This migration establishes the physical and stock-unit models.
-- The movements and positions tables come in migration 002.

-- ---------------------------------------------------------------------------
-- Warehouses
-- ---------------------------------------------------------------------------
CREATE TABLE warehouses (
    id          TEXT        PRIMARY KEY,
    name        TEXT        NOT NULL,
    time_zone   TEXT        NOT NULL DEFAULT 'UTC',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- Zones
-- ---------------------------------------------------------------------------
CREATE TABLE zones (
    id           TEXT        PRIMARY KEY,
    warehouse_id TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    purpose      TEXT        NOT NULL CHECK (purpose IN (
                                'receiving','bulk','pick_face','packing','returns','quarantine'
                             )),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (warehouse_id, name)
);

-- ---------------------------------------------------------------------------
-- Locations
-- ---------------------------------------------------------------------------
CREATE TABLE locations (
    code            TEXT        NOT NULL,
    warehouse_id    TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE CASCADE,
    zone_id         TEXT        NOT NULL REFERENCES zones(id) ON DELETE RESTRICT,
    -- Physical capacity: 0 means unlimited (used for virtual locations).
    max_units       INT         NOT NULL DEFAULT 0 CHECK (max_units >= 0),
    max_volume_cm3  BIGINT      NOT NULL DEFAULT 0 CHECK (max_volume_cm3 >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, code)
);

-- Virtual locations are referenced by movements but do not live in the
-- locations table (they have no physical zone). We create them as a separate
-- concept enforced in application code.

-- Index: look up all locations in a zone.
CREATE INDEX locations_zone_idx ON locations (zone_id);

-- ---------------------------------------------------------------------------
-- SKUs
-- ---------------------------------------------------------------------------
CREATE TABLE skus (
    id              TEXT        NOT NULL,
    warehouse_id    TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE CASCADE,
    barcode         TEXT        NOT NULL,
    name            TEXT        NOT NULL,
    weight_g        INT         NOT NULL DEFAULT 0 CHECK (weight_g >= 0),
    volume_cm3      BIGINT      NOT NULL DEFAULT 0 CHECK (volume_cm3 >= 0),
    is_lot_tracked  BOOLEAN     NOT NULL DEFAULT FALSE,
    velocity_class  TEXT        NOT NULL DEFAULT 'C' CHECK (velocity_class IN ('A','B','C')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id)
);

-- Index: barcode lookup (per warehouse, since barcodes could overlap across
-- warehouses in multi-site deployments).
CREATE UNIQUE INDEX skus_barcode_idx ON skus (warehouse_id, barcode);

-- ---------------------------------------------------------------------------
-- Lots
-- ---------------------------------------------------------------------------
CREATE TABLE lots (
    id           TEXT        NOT NULL,
    sku_id       TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE CASCADE,
    received_at  TIMESTAMPTZ NOT NULL,
    -- expires_at is NULL for non-perishable stock.
    expires_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id),
    FOREIGN KEY (warehouse_id, sku_id) REFERENCES skus(warehouse_id, id) ON DELETE RESTRICT
);

CREATE INDEX lots_sku_idx ON lots (warehouse_id, sku_id);

-- ---------------------------------------------------------------------------
-- LPNs (Licence Plate Numbers)
-- ---------------------------------------------------------------------------
CREATE TABLE lpns (
    id                  TEXT        NOT NULL,
    warehouse_id        TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE CASCADE,
    -- current_location is the last-known location, kept updated by movement
    -- processing. May be a virtual location code.
    current_location    TEXT        NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id)
);
