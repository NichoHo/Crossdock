DROP TABLE IF EXISTS settlement_breaks;
DROP TABLE IF EXISTS remittance_lines;
DROP TABLE IF EXISTS remittances;
DROP TABLE IF EXISTS return_lines;
DROP TABLE IF EXISTS return_authorisations;
DROP TABLE IF EXISTS ledger;
DROP TABLE IF EXISTS delivery_outcomes;
DROP TABLE IF EXISTS carrier_terms;
DROP INDEX IF EXISTS shipments_tracking_idx;
ALTER TABLE tasks DROP CONSTRAINT tasks_kind_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_kind_check CHECK (kind IN
    ('putaway','pick','pack','count','return_intake','relocate'));
