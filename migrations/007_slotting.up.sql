-- Migration 007: Phase 5 slotting (spec 10).
-- A Python batch job writes proposals; the platform replays the historical waves
-- against both layouts with its own router and shows that figure.

CREATE TABLE slotting_proposals (
    id                TEXT        NOT NULL,
    warehouse_id      TEXT        NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    -- proposed: written by the job. replayed: the platform measured it.
    -- accepted: relocate tasks exist. completed: every move is done.
    state             TEXT        NOT NULL DEFAULT 'proposed' CHECK (state IN
                         ('proposed','replayed','accepted','completed','rejected','superseded')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    window_days       INT         NOT NULL,
    max_moves         INT         NOT NULL,
    solver            TEXT        NOT NULL CHECK (solver IN ('cpsat','greedy')),
    solver_status     TEXT        NOT NULL DEFAULT '',
    -- The solver's own objective (a weighted proxy, not metres). Shown as a footnote only.
    objective_before  DOUBLE PRECISION NOT NULL DEFAULT 0,
    objective_after   DOUBLE PRECISION NOT NULL DEFAULT 0,
    -- What the platform measured by replaying the waves through the router (metres).
    replay_waves      INT,
    replay_before_m   DOUBLE PRECISION,
    replay_after_m    DOUBLE PRECISION,
    replayed_at       TIMESTAMPTZ,
    accepted_by       TEXT        NOT NULL DEFAULT '',
    accepted_at       TIMESTAMPTZ,
    note              TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (warehouse_id, id)
);
CREATE INDEX slotting_proposals_idx ON slotting_proposals (warehouse_id, created_at DESC);

-- Every SKU the job considered, where it is and where the proposal puts it.
CREATE TABLE slotting_layout (
    warehouse_id      TEXT   NOT NULL,
    proposal_id       TEXT   NOT NULL,
    sku_id            TEXT   NOT NULL,
    current_location  TEXT   NOT NULL,
    proposed_location TEXT   NOT NULL,
    velocity          INT    NOT NULL DEFAULT 0,
    PRIMARY KEY (warehouse_id, proposal_id, sku_id),
    FOREIGN KEY (warehouse_id, proposal_id) REFERENCES slotting_proposals(warehouse_id, id) ON DELETE CASCADE
);

-- The moves, in the order they should be done.
CREATE TABLE slotting_moves (
    warehouse_id  TEXT NOT NULL,
    proposal_id   TEXT NOT NULL,
    seq           INT  NOT NULL,
    sku_id        TEXT NOT NULL,
    from_location TEXT NOT NULL,
    to_location   TEXT NOT NULL,
    reason        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (warehouse_id, proposal_id, seq),
    FOREIGN KEY (warehouse_id, proposal_id) REFERENCES slotting_proposals(warehouse_id, id) ON DELETE CASCADE
);
