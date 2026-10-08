DROP TABLE IF EXISTS delivery_attempts;
DROP TABLE IF EXISTS deliveries;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS endpoint_secrets;
DROP TABLE IF EXISTS endpoints;
ALTER TABLE outbox DROP COLUMN IF EXISTS traceparent;
