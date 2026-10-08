-- Migration 001 DOWN: drop core schema tables in reverse dependency order.
DROP INDEX IF EXISTS lots_sku_idx;
DROP INDEX IF EXISTS skus_barcode_idx;
DROP INDEX IF EXISTS locations_zone_idx;

DROP TABLE IF EXISTS lpns;
DROP TABLE IF EXISTS lots;
DROP TABLE IF EXISTS skus;
DROP TABLE IF EXISTS locations;
DROP TABLE IF EXISTS zones;
DROP TABLE IF EXISTS warehouses;
