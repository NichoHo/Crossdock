-- Migration 002 DOWN: drop movements and positions tables.
DROP TRIGGER IF EXISTS movements_no_delete ON movements;
DROP TRIGGER IF EXISTS movements_no_update ON movements;
DROP FUNCTION IF EXISTS movements_immutability_guard();

DROP INDEX IF EXISTS positions_lpn_idx;
DROP INDEX IF EXISTS positions_location_idx;
DROP INDEX IF EXISTS positions_sku_idx;
DROP TABLE IF EXISTS positions;

DROP INDEX IF EXISTS movements_shipment_idx;
DROP INDEX IF EXISTS movements_order_idx;
DROP INDEX IF EXISTS movements_replay_idx;
DROP INDEX IF EXISTS movements_idempotency_idx;
DROP TABLE IF EXISTS movements;

DROP SEQUENCE IF EXISTS movements_seq_global;
