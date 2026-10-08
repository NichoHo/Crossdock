-- Migration 004: Phase 2 event delivery (spec 7).

-- The outbox row remembers the trace that caused it, so one trace can follow
-- a scanner confirm through the relay, the bus and the webhook call.
ALTER TABLE outbox ADD COLUMN traceparent TEXT NOT NULL DEFAULT '';

-- Webhook endpoints registered in the console.
CREATE TABLE endpoints (
    id                   TEXT        NOT NULL,
    warehouse_id         TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    url                  TEXT        NOT NULL,
    -- Event filter: exact topics, "prefix.*", or "*".
    events               TEXT[]      NOT NULL,
    state                TEXT        NOT NULL DEFAULT 'active' CHECK (state IN ('active','disabled')),
    -- Circuit breaker (spec 7.2).
    breaker              TEXT        NOT NULL DEFAULT 'closed' CHECK (breaker IN ('closed','open','half_open')),
    consecutive_failures INT         NOT NULL DEFAULT 0,
    open_until           TIMESTAMPTZ,
    -- A worker holds a short lease while it drains an endpoint, so one
    -- endpoint is never worked by two workers and ordering holds.
    lease_until          TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (warehouse_id, id)
);

-- Signing secrets. The plaintext is shown once at creation. We keep a hash for
-- comparison and the secret encrypted (AES-GCM) because signing needs it back.
CREATE TABLE endpoint_secrets (
    id           TEXT        NOT NULL,
    warehouse_id TEXT        NOT NULL,
    endpoint_id  TEXT        NOT NULL,
    secret_enc   BYTEA       NOT NULL,
    secret_hash  TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- NULL = current. After rotation the previous secret gets an expiry (overlap window).
    expires_at   TIMESTAMPTZ,
    PRIMARY KEY (warehouse_id, id),
    FOREIGN KEY (warehouse_id, endpoint_id) REFERENCES endpoints(warehouse_id, id) ON DELETE CASCADE
);
CREATE INDEX endpoint_secrets_endpoint_idx ON endpoint_secrets (warehouse_id, endpoint_id);

-- Every event the fan-out consumer has seen. id is the outbox id, which is the
-- per-warehouse-monotonic event sequence.
CREATE TABLE events (
    id           BIGINT      PRIMARY KEY,
    warehouse_id TEXT        NOT NULL,
    topic        TEXT        NOT NULL,
    key          TEXT        NOT NULL DEFAULT '',
    payload      JSONB       NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL,
    traceparent  TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX events_warehouse_idx ON events (warehouse_id, id);

CREATE TABLE deliveries (
    id              TEXT        PRIMARY KEY,
    event_id        BIGINT      NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    warehouse_id    TEXT        NOT NULL,
    endpoint_id     TEXT        NOT NULL,
    state           TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','delivered','dead')),
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_status     INT         NOT NULL DEFAULT 0,
    last_error      TEXT        NOT NULL DEFAULT '',
    -- ordering_key copies events.key so the claim query avoids a join.
    ordering_key    TEXT        NOT NULL DEFAULT '',
    -- replay_of is the id of the delivery this one replays; '' for originals.
    replay_of       TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at    TIMESTAMPTZ,
    FOREIGN KEY (warehouse_id, endpoint_id) REFERENCES endpoints(warehouse_id, id) ON DELETE CASCADE
);
-- Fan-out is idempotent: an original delivery exists once per (event, endpoint).
CREATE UNIQUE INDEX deliveries_original_idx ON deliveries (event_id, endpoint_id) WHERE replay_of = '';
CREATE INDEX deliveries_due_idx ON deliveries (warehouse_id, endpoint_id, next_attempt_at, event_id) WHERE state = 'pending';
CREATE INDEX deliveries_key_idx ON deliveries (warehouse_id, endpoint_id, ordering_key, event_id) WHERE state = 'pending' AND ordering_key <> '';
CREATE INDEX deliveries_dead_idx ON deliveries (warehouse_id, endpoint_id) WHERE state = 'dead';

-- Every attempt, kept so a dead letter shows every response (spec 7.2).
CREATE TABLE delivery_attempts (
    delivery_id TEXT        NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    n           INT         NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status      INT         NOT NULL DEFAULT 0,
    error       TEXT        NOT NULL DEFAULT '',
    response    TEXT        NOT NULL DEFAULT '',
    duration_ms INT         NOT NULL DEFAULT 0,
    PRIMARY KEY (delivery_id, n)
);
